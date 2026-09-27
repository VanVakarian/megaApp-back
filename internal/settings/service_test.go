package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"

	_ "modernc.org/sqlite"
)

func newTestService(db *sql.DB) *Service {
	return NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
}

func rawFields(t *testing.T, values map[string]any) map[string]json.RawMessage {
	t.Helper()

	fields := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%s) error = %v", key, err)
		}
		fields[key] = encoded
	}
	return fields
}

func TestGetReturnsNamespaceDefaultsForNewUser(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if core.SelectedChapterFood || core.SelectedChapterMoney {
		t.Fatal("default core settings should all be false")
	}
}

func TestGetWithProfileMergesUserNameAndAdminOntoCore(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", true)

	raw, err := service.GetWithProfile(context.Background(), userID, NamespaceCore, "alice")
	if err != nil {
		t.Fatalf("GetWithProfile() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	var userName string
	if err := json.Unmarshal(fields["userName"], &userName); err != nil {
		t.Fatalf("Unmarshal(userName) error = %v", err)
	}
	if userName != "alice" {
		t.Fatalf("userName = %q, want alice", userName)
	}

	var isAdmin bool
	if err := json.Unmarshal(fields["isUserAdmin"], &isAdmin); err != nil {
		t.Fatalf("Unmarshal(isUserAdmin) error = %v", err)
	}
	if !isAdmin {
		t.Fatal("isUserAdmin = false, want true")
	}
}

func TestGetWithProfileDoesNotMergeProfileForOtherNamespaces(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", true)

	raw, err := service.GetWithProfile(context.Background(), userID, NamespaceFood, "alice")
	if err != nil {
		t.Fatalf("GetWithProfile() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := fields["userName"]; ok {
		t.Fatal("food namespace response should not contain userName")
	}
}

func TestPutUpdatesSingleField(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	applied, updatedAtMillis, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"selectedChapterFood": true}))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if !applied {
		t.Fatal("Put() applied = false, want true")
	}
	if updatedAtMillis <= 0 {
		t.Fatalf("Put() updatedAtMillis = %d, want > 0", updatedAtMillis)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !core.SelectedChapterFood {
		t.Fatal("SelectedChapterFood = false, want true")
	}
}

func TestPutMergesMultipleFieldsInOneCall(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	applied, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{
		"selectedChapterFood":  true,
		"selectedChapterMoney": true,
	}))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if !applied {
		t.Fatal("Put() applied = false, want true")
	}

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !core.SelectedChapterFood || !core.SelectedChapterMoney {
		t.Fatal("both merged fields should be applied")
	}
}

func TestPutPreservesFieldsNotIncludedInThisCall(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	if _, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"selectedChapterFood": true})); err != nil {
		t.Fatalf("Put() first error = %v", err)
	}
	if _, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-2", rawFields(t, map[string]any{"selectedChapterMoney": true})); err != nil {
		t.Fatalf("Put() second error = %v", err)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !core.SelectedChapterFood {
		t.Fatal("SelectedChapterFood from the first call should still be true")
	}
	if !core.SelectedChapterMoney {
		t.Fatal("SelectedChapterMoney from the second call should be true")
	}
}

func TestPutSameOperationIDReplaysWithoutReapplying(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	if applied, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-retry", rawFields(t, map[string]any{"selectedChapterFood": true})); err != nil || !applied {
		t.Fatalf("Put() first = (applied=%v, err=%v), want (true, nil)", applied, err)
	}

	applied, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-retry", rawFields(t, map[string]any{"selectedChapterMoney": true}))
	if err != nil {
		t.Fatalf("Put() retry error = %v", err)
	}
	if applied {
		t.Fatal("Put() retry applied = true, want false (replayed)")
	}

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !core.SelectedChapterFood {
		t.Fatal("SelectedChapterFood = false, want true (from the first, actually-applied call)")
	}
	if core.SelectedChapterMoney {
		t.Fatal("SelectedChapterMoney = true, want false (retry was replayed, not reapplied)")
	}
}

func TestPutRejectsUnknownField(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"notARealField": true}))
	if !errors.Is(err, ErrInvalidSettingPayload) {
		t.Fatalf("Put() error = %v, want ErrInvalidSettingPayload", err)
	}
}

func TestPutRejectsNullForNonPointerField(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"selectedChapterFood": nil}))
	if !errors.Is(err, ErrInvalidSettingPayload) {
		t.Fatalf("Put() error = %v, want ErrInvalidSettingPayload", err)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceCore)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var core CoreSettings
	if err := json.Unmarshal(raw, &core); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if core.SelectedChapterFood {
		t.Fatal("rejected PUT should not have been applied")
	}
}

func TestPutAllowsNullForPointerField(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	if _, _, err := service.Put(context.Background(), userID, NamespaceFood, "op-1", rawFields(t, map[string]any{"height": 185})); err != nil {
		t.Fatalf("Put() first error = %v", err)
	}
	if _, _, err := service.Put(context.Background(), userID, NamespaceFood, "op-2", rawFields(t, map[string]any{"height": nil})); err != nil {
		t.Fatalf("Put() error = %v, want nil (height is a pointer field, null clears it)", err)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceFood)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var food FoodSettings
	if err := json.Unmarshal(raw, &food); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if food.Height != nil {
		t.Fatalf("Height = %v, want nil", food.Height)
	}
}

// isUserAdmin must never be writable through the generic namespace PUT — it lives on the users
// table as an authorization flag, not inside userSettings.payload. Allowing it through here would
// let anyone self-escalate to admin with a single PUT to /api/settings/core.
func TestPutRejectsIsUserAdminField(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"isUserAdmin": true}))
	if !errors.Is(err, ErrInvalidSettingPayload) {
		t.Fatalf("Put() error = %v, want ErrInvalidSettingPayload", err)
	}

	isAdmin, _, err := NewRepository(db, sqlite.WriteDB{DB: db}).GetUserAdminAndName(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetUserAdminAndName() error = %v", err)
	}
	if isAdmin {
		t.Fatal("isAdmin = true, want false — PUT must not be able to grant admin")
	}
}

func TestPutRejectsWrongType(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", rawFields(t, map[string]any{"darkTheme": "not-a-bool"}))
	if !errors.Is(err, ErrInvalidSettingPayload) {
		t.Fatalf("Put() error = %v, want ErrInvalidSettingPayload", err)
	}
}

func TestPutRejectsUnknownNamespace(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, "not-a-namespace", "op-1", rawFields(t, map[string]any{"x": true}))
	if !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("Put() error = %v, want ErrInvalidNamespace", err)
	}
}

func TestPutRejectsEmptyPayload(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	_, _, err := service.Put(context.Background(), userID, NamespaceCore, "op-1", map[string]json.RawMessage{})
	if !errors.Is(err, ErrInvalidSettingPayload) {
		t.Fatalf("Put() error = %v, want ErrInvalidSettingPayload", err)
	}
}

func TestFoodHeightRoundTrips(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	if _, _, err := service.Put(context.Background(), userID, NamespaceFood, "op-1", rawFields(t, map[string]any{"height": 185})); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceFood)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var food FoodSettings
	if err := json.Unmarshal(raw, &food); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if food.Height == nil || *food.Height != 185 {
		t.Fatalf("Height = %v, want 185", food.Height)
	}
}

func TestFoodStatsSettingsDefaultsAndRoundTrip(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	raw, err := service.Get(context.Background(), userID, NamespaceFood)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var food FoodSettings
	if err := json.Unmarshal(raw, &food); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if food.StatsDateRange != nil {
		t.Fatalf("StatsDateRange = %v, want nil", food.StatsDateRange)
	}
	if food.StatsTopProductsMetric != "kcal" {
		t.Fatalf("StatsTopProductsMetric = %q, want kcal", food.StatsTopProductsMetric)
	}
	wantBlocks := []string{"streak", "milestones", "charts", "topProducts"}
	if !slices.Equal(food.StatsAccordionOpenBlocks, wantBlocks) {
		t.Fatalf("StatsAccordionOpenBlocks = %v, want %v", food.StatsAccordionOpenBlocks, wantBlocks)
	}

	if _, _, err := service.Put(context.Background(), userID, NamespaceFood, "op-1", rawFields(t, map[string]any{
		"statsDateRange":           map[string]any{"start": "2026-01-01", "end": "2026-02-01"},
		"statsTopProductsMetric":   "weight",
		"statsAccordionOpenBlocks": []string{"streak"},
	})); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	raw, err = service.Get(context.Background(), userID, NamespaceFood)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := json.Unmarshal(raw, &food); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if food.StatsDateRange == nil || food.StatsDateRange.Start != "2026-01-01" || food.StatsDateRange.End != "2026-02-01" {
		t.Fatalf("StatsDateRange = %+v, want {2026-01-01 2026-02-01}", food.StatsDateRange)
	}
	if food.StatsTopProductsMetric != "weight" {
		t.Fatalf("StatsTopProductsMetric = %q, want weight", food.StatsTopProductsMetric)
	}
	if !slices.Equal(food.StatsAccordionOpenBlocks, []string{"streak"}) {
		t.Fatalf("StatsAccordionOpenBlocks = %v, want [streak]", food.StatsAccordionOpenBlocks)
	}
}

func TestMoneySettingsDefaultsAndRoundTrip(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	raw, err := service.Get(context.Background(), userID, NamespaceMoney)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var money MoneySettings
	if err := json.Unmarshal(raw, &money); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if money.DisplayCurrency != "RUB" {
		t.Fatalf("DisplayCurrency = %q, want RUB", money.DisplayCurrency)
	}
	if money.ConvertToUnifiedCurrency {
		t.Fatal("ConvertToUnifiedCurrency default should be false")
	}

	if _, _, err := service.Put(context.Background(), userID, NamespaceMoney, "op-1", rawFields(t, map[string]any{
		"displayCurrency":     "USD",
		"disabledCategoryIds": []any{1, 2, nil},
	})); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	raw, err = service.Get(context.Background(), userID, NamespaceMoney)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := json.Unmarshal(raw, &money); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if money.DisplayCurrency != "USD" {
		t.Fatalf("DisplayCurrency = %q, want USD", money.DisplayCurrency)
	}
	if len(money.DisabledCategoryIds) != 3 || money.DisabledCategoryIds[2] != nil {
		t.Fatalf("DisabledCategoryIds = %v, want [1,2,null]", money.DisabledCategoryIds)
	}
}

func TestMetricsSettingsDefaultsAndRoundTrip(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	raw, err := service.Get(context.Background(), userID, NamespaceMetrics)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var metrics MetricsSettings
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if metrics.CardSize.WidthPx != 304 {
		t.Fatalf("CardSize.WidthPx = %v, want 304", metrics.CardSize.WidthPx)
	}

	batch := map[string]any{
		"cardSize":             map[string]any{"widthPx": 400, "heightPx": 150, "expandedHeightPx": 500},
		"syncCrosshairEnabled": true,
	}
	if _, _, err := service.Put(context.Background(), userID, NamespaceMetrics, "op-1", rawFields(t, batch)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	raw, err = service.Get(context.Background(), userID, NamespaceMetrics)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if metrics.CardSize.WidthPx != 400 {
		t.Fatalf("CardSize.WidthPx = %v, want 400", metrics.CardSize.WidthPx)
	}
	if !metrics.SyncCrosshairEnabled {
		t.Fatal("SyncCrosshairEnabled = false, want true")
	}
}

func TestServiceCustomLabelsAcceptsLegacyStringAndNewObjectShape(t *testing.T) {
	db := openSettingsTestDB(t)
	service := newTestService(db)
	userID := insertSettingsTestUser(t, db, "alice", false)

	if _, err := db.Exec(
		`INSERT INTO userSettings (usersId, namespace, payload, updatedAt) VALUES (?, ?, ?, datetime('now'))`,
		userID, NamespaceMetrics, `{"serviceCustomLabels":{"legacy-svc":"Old Label"}}`,
	); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	raw, err := service.Get(context.Background(), userID, NamespaceMetrics)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var metrics MetricsSettings
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := metrics.ServiceCustomLabels["legacy-svc"]; got.Short != "Old Label" || got.Long != "Old Label" {
		t.Fatalf("ServiceCustomLabels[legacy-svc] = %+v, want {Old Label, Old Label}", got)
	}

	if _, _, err := service.Put(context.Background(), userID, NamespaceMetrics, "op-1", rawFields(t, map[string]any{
		"serviceCustomLabels": map[string]any{
			"new-svc": map[string]any{"short": "N", "long": "New Service"},
		},
	})); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	raw, err = service.Get(context.Background(), userID, NamespaceMetrics)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := metrics.ServiceCustomLabels["new-svc"]; got.Short != "N" || got.Long != "New Service" {
		t.Fatalf("ServiceCustomLabels[new-svc] = %+v, want {N, New Service}", got)
	}
	if got := metrics.ServiceCustomLabels["legacy-svc"]; got.Short != "Old Label" || got.Long != "Old Label" {
		t.Fatalf("legacy-svc should survive the merge, got %+v", got)
	}
}

func openSettingsTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT,
			hashedPassword TEXT,
			isAdmin BOOLEAN
		);

		CREATE TABLE userSettings (
			usersId INTEGER NOT NULL,
			namespace TEXT NOT NULL,
			payload TEXT NOT NULL,
			updatedAt TEXT NOT NULL,
			PRIMARY KEY (usersId, namespace)
		);

		CREATE TABLE syncOperations (id TEXT PRIMARY KEY, userId INTEGER NOT NULL, createdAt TEXT NOT NULL, resultJSON TEXT NOT NULL);
	`); err != nil {
		_ = db.Close()
		t.Fatalf("Exec() error = %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func insertSettingsTestUser(t *testing.T, db *sql.DB, username string, isAdmin bool) int64 {
	t.Helper()

	result, err := db.Exec(`INSERT INTO users (username, hashedPassword, isAdmin) VALUES (?, ?, ?)`, username, "hash", isAdmin)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}

	return id
}
