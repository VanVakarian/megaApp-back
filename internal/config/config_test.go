package config

import (
	"testing"
	"time"
)

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("APP_PORT", "")
	t.Setenv("APP_HOST", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("DB_NAME", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("MIGRATIONS_DIR", "")
	t.Setenv("PUBLIC_DIR", "")
	t.Setenv("BACKUPS_DIR", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_MODEL", "")
	t.Setenv("OPENROUTER_IMAGE_MODEL", "")
	t.Setenv("OPENROUTER_TIMEOUT_SECONDS", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_EMBEDDING_MODEL", "")
	t.Setenv("OPENAI_EMBEDDING_DIMENSIONS", "")
	t.Setenv("OPENAI_TIMEOUT_SECONDS", "")
	t.Setenv("COEFFICIENTS_JOB_ENABLED", "")
	t.Setenv("COEFFICIENTS_JOB_SCHEDULE", "")
	t.Setenv("COEFFICIENTS_START_WITH_ZEROS", "")
	t.Setenv("COEFFICIENTS_DIFFERENT_TRIES_PER_ROUND", "")
	t.Setenv("COEFFICIENTS_CHILDREN_AMT", "")
	t.Setenv("COEFFICIENTS_BEST_AMT", "")
	t.Setenv("COEFFICIENTS_DAYS_7", "")
	t.Setenv("COEFFICIENTS_DAYS_60", "")
	t.Setenv("COEFFICIENTS_MAX_TRIES_IF_UNCHANGED", "")
	t.Setenv("QUOTES_JOB_ENABLED", "")
	t.Setenv("QUOTES_JOB_SCHEDULE", "")
	t.Setenv("QUOTES_FETCH_DAYS", "")
	t.Setenv("QUOTES_RETRY_ATTEMPTS", "")
	t.Setenv("QUOTES_RETRY_DELAY_SECONDS", "")
	t.Setenv("QUOTES_REQUEST_TIMEOUT_SECONDS", "")
	t.Setenv("BACKUP_JOB_ENABLED", "")
	t.Setenv("BACKUP_JOB_SCHEDULE", "")
	t.Setenv("BACKUP_STORAGE_ENABLED", "")
	t.Setenv("BACKUP_STORAGE_REGION", "")
	t.Setenv("BACKUP_STORAGE_BUCKET", "")
	t.Setenv("BACKUP_STORAGE_ENDPOINT", "")
	t.Setenv("BACKUP_STORAGE_FORCE_PATH_STYLE", "")
	t.Setenv("BACKUP_STORAGE_STORAGE_CLASS", "")
	t.Setenv("BACKUP_STORAGE_ACCESS_KEY_ID", "")
	t.Setenv("BACKUP_STORAGE_SECRET_ACCESS_KEY", "")
	t.Setenv("BACKUP_OPERATION_TIMEOUT_SECONDS", "")
	t.Setenv("METRICS_SERVICE_KEY", "")
	t.Setenv("FLATLINE_BASE_URL", "http://127.0.0.1:4000")
	t.Setenv("FLATLINE_PUSH_TIMEOUT_SECONDS", "")
	t.Setenv("FLATLINE_POLL_INTERVAL_SECONDS", "")
	t.Setenv("FLATLINE_POLL_INITIAL_LOOKBACK_SECONDS", "")
	t.Setenv("FLATLINE_POLL_MAX_CATCHUP_SECONDS", "")
	t.Setenv("HTTP_READ_TIMEOUT_SECONDS", "")
	t.Setenv("HTTP_WRITE_TIMEOUT_SECONDS", "")
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "")
	t.Setenv("SHUTDOWN_TIMEOUT_SECONDS", "")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "")
	t.Setenv("MAX_MULTIPART_BODY_BYTES", "")
	t.Setenv("WS_READ_LIMIT_BYTES", "")
	t.Setenv("WS_WRITE_TIMEOUT_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.AppPort != 3000 {
		t.Fatalf("AppPort = %d, want 3000", cfg.AppPort)
	}
	if cfg.AppHost != "127.0.0.1" {
		t.Fatalf("AppHost = %q, want 127.0.0.1", cfg.AppHost)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("DataDir = %q, want ./data", cfg.DataDir)
	}
	if cfg.DatabaseName != "megaapp" {
		t.Fatalf("DatabaseName = %q, want megaapp", cfg.DatabaseName)
	}
	if cfg.DatabasePath != "./data/megaapp-test.db" && cfg.DatabasePath != "data/megaapp-test.db" {
		t.Fatalf("DatabasePath = %q, want ./data/megaapp-test.db", cfg.DatabasePath)
	}
	if cfg.MigrationsDir != "./migrations" {
		t.Fatalf("MigrationsDir = %q, want ./migrations", cfg.MigrationsDir)
	}
	if cfg.PublicDir != "./public" {
		t.Fatalf("PublicDir = %q, want ./public", cfg.PublicDir)
	}
	if cfg.BackupsDir != "./backups" {
		t.Fatalf("BackupsDir = %q, want ./backups", cfg.BackupsDir)
	}
	if cfg.OpenRouterAPIKey != "" {
		t.Fatalf("OpenRouterAPIKey = %q, want empty", cfg.OpenRouterAPIKey)
	}
	if cfg.OpenRouterModel != "google/gemini-2.5-pro" {
		t.Fatalf("OpenRouterModel = %q, want google/gemini-2.5-pro", cfg.OpenRouterModel)
	}
	if cfg.OpenRouterImageModel != "google/gemini-2.5-flash-image" {
		t.Fatalf("OpenRouterImageModel = %q, want google/gemini-2.5-flash-image", cfg.OpenRouterImageModel)
	}
	if cfg.OpenRouterTimeout != 60*time.Second {
		t.Fatalf("OpenRouterTimeout = %v, want 60s", cfg.OpenRouterTimeout)
	}
	if cfg.OpenAIAPIKey != "" {
		t.Fatalf("OpenAIAPIKey = %q, want empty", cfg.OpenAIAPIKey)
	}
	if cfg.OpenAIEmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("OpenAIEmbeddingModel = %q, want text-embedding-3-small", cfg.OpenAIEmbeddingModel)
	}
	if cfg.OpenAIEmbeddingDims != 768 {
		t.Fatalf("OpenAIEmbeddingDims = %d, want 768", cfg.OpenAIEmbeddingDims)
	}
	if cfg.OpenAITimeout != 60*time.Second {
		t.Fatalf("OpenAITimeout = %v, want 60s", cfg.OpenAITimeout)
	}
	if cfg.PersonalKcalJobEnabled {
		t.Fatal("PersonalKcalJobEnabled = true, want false")
	}
	if cfg.PersonalKcalJobSchedule != "0 2 1 * *" {
		t.Fatalf("PersonalKcalJobSchedule = %q, want 0 2 1 * *", cfg.PersonalKcalJobSchedule)
	}
	if cfg.PersonalKcalLookbackMonths != 3 {
		t.Fatalf("PersonalKcalLookbackMonths = %d, want 3", cfg.PersonalKcalLookbackMonths)
	}
	if cfg.PersonalKcalDecayRate != 0.6 {
		t.Fatalf("PersonalKcalDecayRate = %v, want 0.6", cfg.PersonalKcalDecayRate)
	}
	if cfg.PersonalKcalCoverageThreshold != 0.5 {
		t.Fatalf("PersonalKcalCoverageThreshold = %v, want 0.5", cfg.PersonalKcalCoverageThreshold)
	}
	if cfg.PersonalKcalMaxMonthlyChangePercent != 10 {
		t.Fatalf("PersonalKcalMaxMonthlyChangePercent = %v, want 10", cfg.PersonalKcalMaxMonthlyChangePercent)
	}
	if cfg.PersonalKcalAnchorLambda != 3 {
		t.Fatalf("PersonalKcalAnchorLambda = %v, want 3", cfg.PersonalKcalAnchorLambda)
	}
	if cfg.PersonalKcalEvidenceHalfKcal != 333 {
		t.Fatalf("PersonalKcalEvidenceHalfKcal = %v, want 333", cfg.PersonalKcalEvidenceHalfKcal)
	}
	if cfg.PersonalKcalCoefLogStep != 0.03 {
		t.Fatalf("PersonalKcalCoefLogStep = %v, want 0.03", cfg.PersonalKcalCoefLogStep)
	}
	if cfg.PersonalKcalNormStep != 33 {
		t.Fatalf("PersonalKcalNormStep = %v, want 33", cfg.PersonalKcalNormStep)
	}
	if cfg.PersonalKcalXStep != 33 {
		t.Fatalf("PersonalKcalXStep = %v, want 33", cfg.PersonalKcalXStep)
	}
	if cfg.PersonalKcalPopulation != 33 {
		t.Fatalf("PersonalKcalPopulation = %d, want 33", cfg.PersonalKcalPopulation)
	}
	if cfg.PersonalKcalMaxGenerations != 333 {
		t.Fatalf("PersonalKcalMaxGenerations = %d, want 333", cfg.PersonalKcalMaxGenerations)
	}
	if cfg.PersonalKcalMaxStale != 33 {
		t.Fatalf("PersonalKcalMaxStale = %d, want 33", cfg.PersonalKcalMaxStale)
	}
	if cfg.QuotesJobEnabled {
		t.Fatal("QuotesJobEnabled = true, want false")
	}
	if cfg.QuotesJobSchedule != "0 3 * * *" {
		t.Fatalf("QuotesJobSchedule = %q, want 0 3 * * *", cfg.QuotesJobSchedule)
	}
	if cfg.QuotesFetchDays != 7 {
		t.Fatalf("QuotesFetchDays = %d, want 7", cfg.QuotesFetchDays)
	}
	if cfg.QuotesRetryAttempts != 3 {
		t.Fatalf("QuotesRetryAttempts = %d, want 3", cfg.QuotesRetryAttempts)
	}
	if cfg.QuotesRetryDelay != 30*time.Second {
		t.Fatalf("QuotesRetryDelay = %v, want 30s", cfg.QuotesRetryDelay)
	}
	if cfg.QuotesRequestTimeout != 20*time.Second {
		t.Fatalf("QuotesRequestTimeout = %v, want 20s", cfg.QuotesRequestTimeout)
	}
	if cfg.BackupJobEnabled {
		t.Fatal("BackupJobEnabled = true, want false")
	}
	if cfg.BackupJobSchedule != "0 2 * * *" {
		t.Fatalf("BackupJobSchedule = %q, want 0 2 * * *", cfg.BackupJobSchedule)
	}
	if cfg.BackupStorageEnabled {
		t.Fatal("BackupStorageEnabled = true, want false")
	}
	if cfg.BackupOperationTimeout != 300*time.Second {
		t.Fatalf("BackupOperationTimeout = %v, want 300s", cfg.BackupOperationTimeout)
	}
	if cfg.MetricsServiceKey != "megaapp" {
		t.Fatalf("MetricsServiceKey = %q, want megaapp", cfg.MetricsServiceKey)
	}
	if cfg.FlatlineBaseURL != "http://127.0.0.1:4000" {
		t.Fatalf("FlatlineBaseURL = %q, want http://127.0.0.1:4000", cfg.FlatlineBaseURL)
	}
	if cfg.FlatlinePushTimeout != 5*time.Second {
		t.Fatalf("FlatlinePushTimeout = %v, want 5s", cfg.FlatlinePushTimeout)
	}
	if cfg.FlatlinePollInterval != 10*time.Second {
		t.Fatalf("FlatlinePollInterval = %v, want 10s", cfg.FlatlinePollInterval)
	}
	if cfg.FlatlinePollInitialLookback != 120*time.Second {
		t.Fatalf("FlatlinePollInitialLookback = %v, want 120s", cfg.FlatlinePollInitialLookback)
	}
	if cfg.FlatlinePollMaxCatchUp != 600*time.Second {
		t.Fatalf("FlatlinePollMaxCatchUp = %v, want 600s", cfg.FlatlinePollMaxCatchUp)
	}
	if cfg.HTTPReadTimeout != 15*time.Second {
		t.Fatalf("HTTPReadTimeout = %v, want 15s", cfg.HTTPReadTimeout)
	}
	if cfg.HTTPWriteTimeout != 30*time.Second {
		t.Fatalf("HTTPWriteTimeout = %v, want 30s", cfg.HTTPWriteTimeout)
	}
	if cfg.HTTPIdleTimeout != 60*time.Second {
		t.Fatalf("HTTPIdleTimeout = %v, want 60s", cfg.HTTPIdleTimeout)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.MaxRequestBodyBytes != 1<<20 {
		t.Fatalf("MaxRequestBodyBytes = %d, want %d", cfg.MaxRequestBodyBytes, 1<<20)
	}
	if cfg.MaxMultipartBodyBytes != 8<<20 {
		t.Fatalf("MaxMultipartBodyBytes = %d, want %d", cfg.MaxMultipartBodyBytes, 8<<20)
	}
	if cfg.WSReadLimitBytes != 64<<10 {
		t.Fatalf("WSReadLimitBytes = %d, want %d", cfg.WSReadLimitBytes, 64<<10)
	}
	if cfg.WSWriteTimeout != 5*time.Second {
		t.Fatalf("WSWriteTimeout = %v, want 5s", cfg.WSWriteTimeout)
	}
	if len(cfg.IngestSources) != 0 {
		t.Fatalf("IngestSources = %v, want none by default", cfg.IngestSources)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("APP_PORT", "bad")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
}

func TestValidateRejectsInvalidLogLevel(t *testing.T) {
	cfg := validTestConfig()
	cfg.LogLevel = "trace"

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestValidateRejectsInvalidSessionRenewWindow(t *testing.T) {
	cfg := validTestConfig()
	cfg.SessionTTL = time.Hour
	cfg.SessionRenewWindow = time.Hour

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestValidateRejectsInvalidPersonalKcalConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.PersonalKcalDecayRate = 1.5

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestValidateRejectsInvalidQuotesConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.QuotesFetchDays = 0

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestValidateRejectsInvalidBackupConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.BackupJobEnabled = true
	cfg.BackupStorageEnabled = false

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestValidateAcceptsProdLikeConfig(t *testing.T) {
	cfg := validTestConfig()
	cfg.AppEnv = "prod"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func validTestConfig() Config {
	return Config{
		AppEnv:                              "test",
		AppHost:                             "127.0.0.1",
		AppPort:                             3001,
		LogLevel:                            "info",
		DataDir:                             "./data",
		DatabaseName:                        "megaapp",
		DatabasePath:                        "./data/megaapp-test.db",
		MigrationsDir:                       "./migrations",
		PublicDir:                           "./public",
		BackupsDir:                          "./backups",
		OpenRouterTimeout:                   time.Second,
		ImageGenerationMaxAttempts:          3,
		OpenAIEmbeddingDims:                 768,
		OpenAITimeout:                       time.Second,
		PersonalKcalJobSchedule:             "0 2 1 * *",
		PersonalKcalLookbackMonths:          3,
		PersonalKcalDecayRate:               0.6,
		PersonalKcalCoverageThreshold:       0.5,
		PersonalKcalMaxMonthlyChangePercent: 10,
		PersonalKcalAnchorLambda:            3,
		PersonalKcalEvidenceHalfKcal:        333,
		PersonalKcalCoefLogStep:             0.03,
		PersonalKcalNormStep:                33,
		PersonalKcalXStep:                   33,
		PersonalKcalPopulation:              33,
		PersonalKcalMaxGenerations:          333,
		PersonalKcalMaxStale:                33,
		QuotesJobSchedule:                   "0 3 * * *",
		QuotesFetchDays:                     7,
		QuotesRetryAttempts:                 3,
		QuotesRetryDelay:                    30 * time.Second,
		QuotesRequestTimeout:                20 * time.Second,
		BackupJobSchedule:                   "0 2 * * *",
		BackupStorageEnabled:                true,
		BackupStorageRegion:                 "eu-north-1",
		BackupStorageBucket:                 "bucket",
		BackupStorageAccessKeyID:            "key",
		BackupStorageSecretAccessKey:        "secret",
		BackupOperationTimeout:              300 * time.Second,
		MetricsServiceKey:                   "megaapp",
		FlatlineBaseURL:                     "http://127.0.0.1:4000",
		FlatlinePushTimeout:                 time.Second,
		FlatlinePollInterval:                10 * time.Second,
		FlatlinePollInitialLookback:         120 * time.Second,
		FlatlinePollMaxCatchUp:              600 * time.Second,
		HTTPReadTimeout:                     time.Second,
		HTTPWriteTimeout:                    time.Second,
		HTTPIdleTimeout:                     time.Second,
		ShutdownTimeout:                     time.Second,
		MaxRequestBodyBytes:                 1024,
		MaxMultipartBodyBytes:               8 * 1024,
		WSReadLimitBytes:                    1024,
		WSWriteTimeout:                      time.Second,
	}
}
