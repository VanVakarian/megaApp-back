package httpx

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/backup"
	"megaapp-back/internal/config"
	"megaapp-back/internal/food"
	"megaapp-back/internal/ingest"
	"megaapp-back/internal/jobs"
	"megaapp-back/internal/metrics"
	"megaapp-back/internal/money"
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/idempotency"
	s3platform "megaapp-back/internal/platform/s3"
	"megaapp-back/internal/platform/sqlite"
	"megaapp-back/internal/quotes"
	"megaapp-back/internal/settings"
	"megaapp-back/internal/ws"
)

type authModule struct {
	service *auth.Service
	handler *auth.Handler
}

type settingsModule struct {
	service *settings.Service
	handler *settings.Handler
}

type wsModule struct {
	hub     *ws.Hub
	handler *ws.Handler
}

type moneyModule struct {
	service *money.Service
	handler *money.Handler
}

type ingestModule struct {
	handler *ingest.Handler
}

type quotesModule struct {
	service      *quotes.Service
	debugHandler *quotes.DebugHandler
}

type backupModule struct {
	service      *backup.Service
	debugHandler *backup.DebugHandler
}

type metricsModule struct {
	service        *metrics.Service
	historyHandler *metrics.HistoryHandler
	realtime       *metrics.Realtime
	poller         *metrics.Poller
	processSampler *metrics.ProcessSampler
}

type foodModule struct {
	service          *food.Service
	readHandler      *food.Handler
	writeHandler     *food.WriteHandler
	catalogueHandler *food.CatalogueHandler
	imageHandler     *food.ImageHandler
	labHandler       *food.LabHandler
	debugHandler     *food.DebugHandler
	backgrounds      []interface{ Close() error }
}

func buildAuthModule(read *sql.DB, write sqlite.WriteDB, cfg config.Config) authModule {
	repo := auth.NewRepository(read, write)
	service := auth.NewService(repo, auth.SessionConfig{TTL: cfg.SessionTTL, RenewWindow: cfg.SessionRenewWindow})
	return authModule{service: service, handler: auth.NewHandler(service)}
}

func buildSettingsModule(read *sql.DB, write sqlite.WriteDB, hub *ws.Hub) settingsModule {
	repo := settings.NewRepository(read, write)
	service := settings.NewService(repo, idempotency.NewStore(write))
	realtime := settings.NewWSRealtimePublisher(hub)
	return settingsModule{service: service, handler: settings.NewHandler(service, realtime)}
}

func buildWSModule(cfg config.Config, authService *auth.Service, logger *slog.Logger) wsModule {
	hub := ws.NewHub(30*time.Second, ws.NewSyncState())
	hub.SetLogger(logger)
	hub.SetReadLimitBytes(cfg.WSReadLimitBytes)
	hub.SetWriteTimeout(cfg.WSWriteTimeout)
	return wsModule{hub: hub, handler: ws.NewHandler(authService, hub)}
}

func buildIngestModule(cfg config.Config, logger *slog.Logger) ingestModule {
	return ingestModule{handler: ingest.NewHandler(cfg.DataDir, cfg.IngestSources, cfg.IngestKeys, logger)}
}

func buildMoneyModule(read *sql.DB, write sqlite.WriteDB) moneyModule {
	repo := money.NewRepository(read, write)
	service := money.NewService(repo, idempotency.NewStore(write))
	return moneyModule{service: service, handler: money.NewHandler(service)}
}

func buildQuotesModule(read *sql.DB, write sqlite.WriteDB, cfg config.Config, logger *slog.Logger, clk clockplatform.Clock, runtime *jobs.Runtime) (quotesModule, error) {
	repo := quotes.NewRepository(read, write)
	service := quotes.NewService(repo, quotes.Config{
		FetchDays:      cfg.QuotesFetchDays,
		RetryAttempts:  cfg.QuotesRetryAttempts,
		RetryDelay:     cfg.QuotesRetryDelay,
		RequestTimeout: cfg.QuotesRequestTimeout,
	}, clk, quotes.NewMarketFetcher(cfg.QuotesRequestTimeout))
	if cfg.QuotesJobEnabled {
		if err := runtime.Register("quotes", cfg.QuotesJobSchedule, func(ctx context.Context) error {
			result, err := service.Run(ctx)
			if err != nil {
				return err
			}
			for _, failure := range result.Failures {
				logger.Warn("quotes ticker fetch failed", "kind", failure.Kind, "ticker", failure.Ticker, "sources", failure.Sources)
			}
			for _, degraded := range result.Degraded {
				logger.Warn("quotes ticker fetch degraded", "kind", degraded.Kind, "ticker", degraded.Ticker, "sources", degraded.Sources)
			}
			logger.Info("quotes job summary", "upserted", result.UpsertedCount, "failedTickers", len(result.Failures), "degradedTickers", len(result.Degraded))
			return nil
		}); err != nil {
			return quotesModule{}, err
		}
	}
	return quotesModule{service: service, debugHandler: quotes.NewDebugHandler(service)}, nil
}

func buildBackupModule(db sqlite.WriteDB, cfg config.Config, logger *slog.Logger, clk clockplatform.Clock, runtime *jobs.Runtime, metricsRecorder *metrics.Service) (backupModule, error) {
	var uploader *s3platform.Client
	if cfg.BackupStorageEnabled {
		uploader = s3platform.NewClient(s3platform.Config{
			Region:          cfg.BackupStorageRegion,
			Bucket:          cfg.BackupStorageBucket,
			Endpoint:        cfg.BackupStorageEndpoint,
			ForcePathStyle:  cfg.BackupStorageForcePathStyle,
			StorageClass:    cfg.BackupStorageClass,
			AccessKeyID:     cfg.BackupStorageAccessKeyID,
			SecretAccessKey: cfg.BackupStorageSecretAccessKey,
		})
	}
	service := backup.NewService(db, backup.Config{
		DatabaseName:   cfg.DatabaseName,
		DatabaseEnv:    cfg.AppEnv,
		BackupsDir:     cfg.BackupsDir,
		StorageEnabled: cfg.BackupStorageEnabled,
		StorageClass:   cfg.BackupStorageClass,
	}, clk, logger, uploader)
	if cfg.BackupJobEnabled {
		if err := runtime.Register("backup", cfg.BackupJobSchedule, func(ctx context.Context) error {
			backupCtx, cancel := context.WithTimeout(ctx, cfg.BackupOperationTimeout)
			defer cancel()
			if _, err := service.Run(backupCtx); err != nil {
				return err
			}
			metricsRecorder.Increment(backup.MetricJobRan)
			return nil
		}); err != nil {
			return backupModule{}, err
		}
	}
	return backupModule{service: service, debugHandler: backup.NewDebugHandler(service, logger)}, nil
}

func buildMetricsModule(cfg config.Config, logger *slog.Logger, hub *ws.Hub, authService *auth.Service, clk clockplatform.Clock, runtime *jobs.Runtime) (metricsModule, error) {
	service := metrics.NewService(cfg.MetricsServiceKey, clk, authService)
	realtime := metrics.NewRealtime(hub)
	hub.RegisterDisconnectHandler(realtime.Unsubscribe)
	flatlineClient := metrics.NewFlatlineClient(cfg.FlatlineBaseURL, cfg.FlatlinePushTimeout)
	historyClient := metrics.NewFlatlineClient(cfg.FlatlineBaseURL, cfg.HTTPWriteTimeout)

	exporter, err := metrics.NewExporter(metrics.ExporterConfig{
		Service:    cfg.MetricsServiceKey,
		NDJSONPath: filepath.Join(cfg.DataDir, "metrics-outbox.ndjson"),
		AckPath:    filepath.Join(cfg.DataDir, "metrics-outbox.ack.json"),
	}, flatlineClient, logger)
	if err != nil {
		return metricsModule{}, err
	}

	poller := metrics.NewPoller(flatlineClient, realtime, authService, cfg.FlatlinePollInterval, cfg.FlatlinePollInitialLookback, cfg.FlatlinePollMaxCatchUp, clk, logger)

	hub.RegisterHandler("METRICS_SUBSCRIBE", metrics.NewSubscribeHandler(service, realtime))
	hub.RegisterHandler("METRICS_UNSUBSCRIBE", metrics.NewUnsubscribeHandler(realtime))

	// Non-fatal on non-linux (dev machines) — same disable-on-error pattern
	// as Flatline's own HardwareCollector wiring.
	processSampler, err := metrics.NewProcessSampler(clk, logger)
	if err != nil {
		logger.Warn("process_metrics_disabled", "error", err)
		processSampler = nil
	} else {
		processSampler.Start()
	}

	if err := runtime.Register("metrics", "* * * * *", func(ctx context.Context) error {
		if points := service.Flush(); len(points) > 0 {
			exporter.FlushAndPush(ctx, points[0].Bucket, points)
		}
		// Pushed as its own snapshot, not merged into the business-counter
		// one above: the sampler closes its minute bucket on its own 5s
		// timer, independent of this cron tick, so its bucket can legally
		// differ by one minute — mixing it into a single MinuteSnapshot
		// (one bucket field for all points) would mislabel it.
		if processSampler != nil {
			if points := processSampler.TakeCompleted(cfg.MetricsServiceKey); len(points) > 0 {
				exporter.FlushAndPush(ctx, points[0].Bucket, points)
			}
		}
		return nil
	}); err != nil {
		if processSampler != nil {
			_ = processSampler.Close()
		}
		return metricsModule{}, err
	}

	poller.Start()

	return metricsModule{
		service:        service,
		historyHandler: metrics.NewHistoryHandler(service, historyClient),
		realtime:       realtime,
		poller:         poller,
		processSampler: processSampler,
	}, nil
}

func buildFoodModule(read *sql.DB, write sqlite.WriteDB, cfg config.Config, logger *slog.Logger, hub *ws.Hub, authService *auth.Service, clk clockplatform.Clock, metricsRecorder food.MetricsRecorder) (foodModule, error) {
	repo := food.NewRepository(read, write)
	service := food.NewService(repo, idempotency.NewStore(write))
	service.SetClock(clk)
	service.SetAdminChecker(authService)
	service.SetPersonalKcalConfig(food.PersonalKcalConfig{
		LookbackMonths:          cfg.PersonalKcalLookbackMonths,
		DecayRate:               cfg.PersonalKcalDecayRate,
		CoverageThreshold:       cfg.PersonalKcalCoverageThreshold,
		MaxMonthlyChangePercent: cfg.PersonalKcalMaxMonthlyChangePercent,
		AnchorLambda:            cfg.PersonalKcalAnchorLambda,
		EvidenceHalfKcal:        cfg.PersonalKcalEvidenceHalfKcal,
		CoefLogStep:             cfg.PersonalKcalCoefLogStep,
		NormStep:                cfg.PersonalKcalNormStep,
		XStep:                   cfg.PersonalKcalXStep,
		Population:              cfg.PersonalKcalPopulation,
		MaxGenerations:          cfg.PersonalKcalMaxGenerations,
		MaxStale:                cfg.PersonalKcalMaxStale,
	})
	var mediaClient *food.OpenRouterMediaClient
	if strings.TrimSpace(cfg.OpenRouterAPIKey) != "" {
		productGenerator, err := food.NewOpenRouterProductGenerator(food.OpenRouterProductGeneratorConfig{
			APIKey:  cfg.OpenRouterAPIKey,
			Model:   cfg.OpenRouterModel,
			Timeout: cfg.OpenRouterTimeout,
			Logger:  logger,
		})
		if err != nil {
			return foodModule{}, err
		}
		service.SetProductGenerator(productGenerator)
		mediaClient, err = food.NewOpenRouterMediaClient(food.OpenRouterMediaClientConfig{
			APIKey:     cfg.OpenRouterAPIKey,
			ImageModel: cfg.OpenRouterImageModel,
			Timeout:    cfg.OpenRouterTimeout,
			Logger:     logger,
		})
		if err != nil {
			return foodModule{}, err
		}
	}
	if strings.TrimSpace(cfg.OpenAIAPIKey) != "" {
		embeddingGenerator, err := food.NewOpenAIEmbeddingGenerator(food.OpenAIEmbeddingGeneratorConfig{
			APIKey:     cfg.OpenAIAPIKey,
			Model:      cfg.OpenAIEmbeddingModel,
			Dimensions: cfg.OpenAIEmbeddingDims,
			Timeout:    cfg.OpenAITimeout,
			Logger:     logger,
		})
		if err != nil {
			return foodModule{}, err
		}
		service.SetEmbeddingGenerator(embeddingGenerator)
	}
	imageStore, err := food.NewImageStore(cfg.PublicDir)
	if err != nil {
		return foodModule{}, err
	}
	service.SetImageVersionProvider(imageStore)
	realtime := food.NewWSRealtimePublisher(hub, clk)
	imagePipeline := food.NewImagePipeline(imageStore, mediaClient, realtime, cfg.ImageGenerationMaxAttempts, logger)
	service.SetImageGenerationRequester(imagePipeline)
	hub.RegisterHandler("SEARCH_QUERY", food.NewSearchWSHandler(service, clk))
	return foodModule{
		service:          service,
		readHandler:      food.NewHandler(service, realtime),
		writeHandler:     food.NewWriteHandler(service, realtime, metricsRecorder),
		catalogueHandler: food.NewCatalogueHandler(service, realtime, metricsRecorder),
		imageHandler:     food.NewImageHandler(imageStore),
		labHandler:       food.NewLabHandler(food.NewLabService(repo, service, imagePipeline)),
		debugHandler:     food.NewDebugHandler(food.NewDebugService(repo, cfg.BackupsDir, mediaClient, service, realtime)),
		backgrounds:      []interface{ Close() error }{imagePipeline, imageStore},
	}, nil
}
