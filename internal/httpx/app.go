package httpx

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/backup"
	"megaapp-back/internal/config"
	"megaapp-back/internal/food"
	"megaapp-back/internal/httpx/legacy"
	"megaapp-back/internal/jobs"
	"megaapp-back/internal/metrics"
	"megaapp-back/internal/money"
	clockplatform "megaapp-back/internal/platform/clock"
	sqliteplatform "megaapp-back/internal/platform/sqlite"
	"megaapp-back/internal/quotes"
	"megaapp-back/internal/settings"
	"megaapp-back/internal/telemetry"
	"megaapp-back/internal/ws"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type App struct {
	Config      config.Config
	Logger      *slog.Logger
	Observer    Observer
	DB          *sqliteplatform.DB
	WSHub       interface{ Close() error }
	Backgrounds []interface{ Close() error }
	Handler     http.Handler
	Server      *http.Server
}

func NewApp(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	return newApp(ctx, cfg, logger, clockplatform.NewRealClock())
}

func newApp(ctx context.Context, cfg config.Config, logger *slog.Logger, clk clockplatform.Clock) (*App, error) {
	legacy.SetLogger(logger)
	observer := NoopObserver{}

	db, err := sqliteplatform.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}

	if err := sqliteplatform.ApplyMigrations(ctx, db.SQL(), cfg.MigrationsDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	jobRuntime := jobs.NewRuntime(logger)

	authModule := buildAuthModule(db.Read(), db.Write(), cfg)
	wsModule := buildWSModule(cfg, authModule.service, logger)
	settingsModule := buildSettingsModule(db.Read(), db.Write(), wsModule.hub)
	moneyModule := buildMoneyModule(db.Read(), db.Write())
	telemetryModule := buildTelemetryModule(cfg)
	authModule.handler.SetSessionRevoker(func(sessionID string) {
		wsModule.hub.CloseSession(sessionID, 4001, "Session revoked")
	})
	authModule.handler.SetSessionRenewer(func(sessionID string) {
		wsModule.hub.CloseSession(sessionID, 4003, "Session renewed")
	})
	metricsModule, err := buildMetricsModule(cfg, logger, wsModule.hub, authModule.service, clk, jobRuntime)
	if err != nil {
		_ = wsModule.hub.Close()
		_ = jobRuntime.Close()
		_ = db.Close()
		return nil, err
	}
	quotesModule, err := buildQuotesModule(db.Read(), db.Write(), cfg, logger, clk, jobRuntime)
	if err != nil {
		_ = metricsModule.poller.Close()
		_ = metricsModule.processSampler.Close()
		_ = wsModule.hub.Close()
		_ = jobRuntime.Close()
		_ = db.Close()
		return nil, err
	}
	backupModule, err := buildBackupModule(db.Write(), cfg, logger, clk, jobRuntime, metricsModule.service)
	if err != nil {
		_ = metricsModule.poller.Close()
		_ = metricsModule.processSampler.Close()
		_ = wsModule.hub.Close()
		_ = jobRuntime.Close()
		_ = db.Close()
		return nil, err
	}
	foodModule, err := buildFoodModule(db.Read(), db.Write(), cfg, logger, wsModule.hub, authModule.service, clk, metricsModule.service)
	if err != nil {
		_ = metricsModule.poller.Close()
		_ = metricsModule.processSampler.Close()
		_ = wsModule.hub.Close()
		_ = jobRuntime.Close()
		_ = db.Close()
		return nil, err
	}
	if cfg.PersonalKcalJobEnabled {
		if err := jobRuntime.Register("personalKcal", cfg.PersonalKcalJobSchedule, func(ctx context.Context) error {
			result, err := foodModule.service.RunPersonalKcalJob(ctx)
			if err != nil {
				return err
			}
			if result.FailedCount > 0 {
				logger.Warn("personal kcal job completed with user failures", "failedCount", result.FailedCount)
			}
			metricsModule.service.Increment(food.MetricPersonalKcalJobRan)
			return nil
		}); err != nil {
			for _, background := range foodModule.backgrounds {
				_ = background.Close()
			}
			_ = metricsModule.poller.Close()
		_ = metricsModule.processSampler.Close()
			_ = wsModule.hub.Close()
			_ = jobRuntime.Close()
			_ = db.Close()
			return nil, err
		}
	}
	jobRuntime.Start()

	router := chiRouter(logger, observer, cfg.MaxRequestBodyBytes, cfg.MaxMultipartBodyBytes)
	router.Get("/health", HealthHandler())
	router.Get("/readiness", ReadinessHandler(db.PingContext))
	router.Get("/build-info", BuildInfoHandler(cfg))
	router.Get("/api/debug/commit-info", CommitInfoHandler(cfg))

	auth.RegisterRoutes(router, authModule.handler)
	settings.RegisterRoutes(router, authModule.service, settingsModule.handler)
	money.RegisterRoutes(router, authModule.service, moneyModule.handler)
	telemetry.RegisterRoutes(router, authModule.service, telemetryModule.handler)
	food.RegisterRoutes(router, authModule.service, foodModule.readHandler)
	food.RegisterWriteRoutes(router, authModule.service, foodModule.writeHandler)
	food.RegisterCatalogueRoutes(router, authModule.service, foodModule.catalogueHandler)
	food.RegisterImageRoutes(router, foodModule.imageHandler)
	food.RegisterLabRoutes(router, foodModule.labHandler)
	food.RegisterDebugRoutes(router, foodModule.debugHandler)
	quotes.RegisterDebugRoutes(router, quotesModule.debugHandler)
	backup.RegisterDebugRoutes(router, backupModule.debugHandler)
	metrics.RegisterRoutes(router, authModule.service, metricsModule.historyHandler)
	ws.RegisterRoutes(router, wsModule.handler)

	server := &http.Server{
		Addr:              cfg.HTTPAddress(),
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout(cfg.HTTPReadTimeout),
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	return &App{
		Config:      cfg,
		Logger:      logger,
		Observer:    observer,
		DB:          db,
		WSHub:       wsModule.hub,
		Backgrounds: append(foodModule.backgrounds, jobRuntime, metricsModule.poller, metricsModule.processSampler),
		Handler:     router,
		Server:      server,
	}, nil
}

func chiRouter(logger *slog.Logger, observer Observer, maxRequestBodyBytes int64, maxMultipartBodyBytes int64) chi.Router {
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(chimiddleware.RealIP)
	router.Use(chimiddleware.Recoverer)
	router.Use(RequestBodyLimitMiddleware(maxRequestBodyBytes, maxMultipartBodyBytes))
	router.Use(LoggingMiddleware(logger, observer))
	return router
}

func readHeaderTimeout(readTimeout time.Duration) time.Duration {
	if readTimeout <= 5*time.Second {
		return readTimeout
	}
	return 5 * time.Second
}

func (a *App) Serve(listener net.Listener) error {
	err := a.Server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Start() error {
	err := a.Server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Shutdown(ctx context.Context) error {
	var errs []error
	if err := a.Server.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	for _, background := range a.Backgrounds {
		if err := background.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := a.WSHub.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := a.DB.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *App) SQLDB() *sql.DB {
	return a.DB.SQL()
}
