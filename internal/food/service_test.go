package food

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"

	_ "modernc.org/sqlite"
)

type fakeProductGenerator struct{}

var fakeGeneratedProductPreview = ProductPreviewData{
	GeneralizedName: "Новый продукт",
	Kcals:           123,
	Protein:         4.5,
	Fat:             6.7,
	Carbs:           8.9,
	Fiber:           1.2,
	Description:     "Новый продукт для теста",
	Confidence:      0.9,
}

func (fakeProductGenerator) GenerateProduct(ctx context.Context, description string) (ProductPreviewData, error) {
	return fakeGeneratedProductPreview, nil
}

type fakeEmbeddingGenerator struct{}

type fixedFoodClock struct {
	now time.Time
}

func (c fixedFoodClock) Now() time.Time {
	return c.now
}

func (fakeEmbeddingGenerator) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	switch text {
	case "apple-semantic", "Apple", "Fruit":
		return []float64{1, 0}, nil
	default:
		return []float64{0, 1}, nil
	}
}

func TestGetCatalogueAndPersonalKcalsAndStats(t *testing.T) {
	db := openFoodTestDB(t)
	repo := NewRepository(db, sqlite.WriteDB{DB: db})
	service := NewService(repo, idempotency.NewStore(sqlite.WriteDB{DB: db}))

	catalogue, err := service.GetCatalogue(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogue() error = %v", err)
	}
	if len(catalogue) != 2 {
		t.Fatalf("len(catalogue) = %d, want 2", len(catalogue))
	}

	entry, err := service.GetCatalogueEntry(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetCatalogueEntry() error = %v", err)
	}
	if entry == nil || entry.CanDelete == nil || *entry.CanDelete {
		t.Fatalf("entry.CanDelete = %v, want false", entry)
	}

	personalKcals, err := service.GetPersonalKcalsNow(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetPersonalKcalsNow() error = %v", err)
	}
	if len(personalKcals) != 2 || personalKcals[1] != 50 || personalKcals[2] != 250 {
		t.Fatalf("personalKcals = %+v, want catalogue bootstrap {1:50, 2:250}", personalKcals)
	}

	stats, err := service.GetStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if len(stats.Days) == 0 {
		t.Fatal("stats.Days is empty")
	}
}

func TestWriteOperationsPersistData(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	created, applied, err := service.CreateDiaryEntry(context.Background(), 1, "op-create", "2026-06-18", 1, 120, []HistoryEntry{{Action: "init", Value: 120}})
	if err != nil {
		t.Fatalf("CreateDiaryEntry() error = %v", err)
	}
	if !applied {
		t.Fatal("CreateDiaryEntry() applied = false, want true")
	}
	if created.ID <= 0 {
		t.Fatalf("created.ID = %d, want > 0", created.ID)
	}
	if created.Version != 1 {
		t.Fatalf("created.Version = %d, want 1", created.Version)
	}

	updated, applied, err := service.EditDiaryEntry(context.Background(), 1, "op-edit", 10, 80, "set")
	if err != nil {
		t.Fatalf("EditDiaryEntry() error = %v", err)
	}
	if !applied {
		t.Fatal("EditDiaryEntry() applied = false, want true")
	}
	if updated == nil || updated.FoodWeight != 80 || len(updated.History) != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.Version != 1 {
		t.Fatalf("updated.Version = %d, want 1 (seeded at 0, incremented once)", updated.Version)
	}
	if updated.AppliedHistoryEntry == nil || updated.AppliedHistoryEntry.Action != "set" || updated.AppliedHistoryEntry.Value != 80 {
		t.Fatalf("updated.AppliedHistoryEntry = %+v", updated.AppliedHistoryEntry)
	}

	ok, applied, err := service.SetBodyWeight(context.Background(), 1, "op-weight", "2026-06-18", 81)
	if err != nil {
		t.Fatalf("SetBodyWeight() error = %v", err)
	}
	if !ok || !applied {
		t.Fatalf("SetBodyWeight() = (ok=%v, applied=%v), want (true, true)", ok, applied)
	}

	deletedCount, applied, err := service.DeleteDiaryEntriesForDay(context.Background(), 1, "op-day-delete", "2026-06-18")
	if err != nil {
		t.Fatalf("DeleteDiaryEntriesForDay() error = %v", err)
	}
	if !applied {
		t.Fatal("DeleteDiaryEntriesForDay() applied = false, want true")
	}
	if deletedCount != 1 {
		t.Fatalf("deletedCount = %d, want 1", deletedCount)
	}

	restored, applied, err := service.RestoreDiaryEntriesForDay(context.Background(), 1, "op-restore", "2026-06-18", []RestoreDiaryEntryInput{{
		FoodCatalogueID: 1,
		FoodWeight:      120,
		History:         []HistoryEntry{{Action: "init", Value: 120}},
	}})
	if err != nil {
		t.Fatalf("RestoreDiaryEntriesForDay() error = %v", err)
	}
	if !applied {
		t.Fatal("RestoreDiaryEntriesForDay() applied = false, want true")
	}
	if len(restored) != 1 || restored[0].ID <= 0 {
		t.Fatalf("restored = %+v", restored)
	}

	deleted, applied, err := service.DeleteDiaryEntry(context.Background(), 1, "op-delete", 10)
	if err != nil {
		t.Fatalf("DeleteDiaryEntry() error = %v", err)
	}
	if !deleted || !applied {
		t.Fatalf("DeleteDiaryEntry() = (deleted=%v, applied=%v), want (true, true)", deleted, applied)
	}
}

func TestEditDiaryEntrySameOperationIDReplaysWithoutReapplying(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	first, applied, err := service.EditDiaryEntry(context.Background(), 1, "op-retry", 10, 80, "subtract")
	if err != nil {
		t.Fatalf("EditDiaryEntry() first error = %v", err)
	}
	if !applied {
		t.Fatal("EditDiaryEntry() first applied = false, want true")
	}
	if first == nil || first.FoodWeight != 80 || len(first.History) != 2 {
		t.Fatalf("first = %+v", first)
	}
	if first.AppliedHistoryEntry == nil || first.AppliedHistoryEntry.Action != "subtract" || first.AppliedHistoryEntry.Value != 20 {
		t.Fatalf("first.AppliedHistoryEntry = %+v", first.AppliedHistoryEntry)
	}
	if first.Version != 1 {
		t.Fatalf("first.Version = %d, want 1 (seeded at 0, incremented once)", first.Version)
	}

	retry, applied, err := service.EditDiaryEntry(context.Background(), 1, "op-retry", 10, 80, "subtract")
	if err != nil {
		t.Fatalf("EditDiaryEntry() retry error = %v", err)
	}
	if applied {
		t.Fatal("EditDiaryEntry() retry applied = true, want false (replayed)")
	}
	if retry == nil || retry.FoodWeight != 80 || len(retry.History) != 2 {
		t.Fatalf("retry = %+v, want history unchanged at length 2", retry)
	}
	if retry.Version != first.Version {
		t.Fatalf("retry.Version = %d, want %d (replay echoes cached version, no further increment)", retry.Version, first.Version)
	}
	if retry.AppliedHistoryEntry == nil || retry.AppliedHistoryEntry.Action != "subtract" || retry.AppliedHistoryEntry.Value != 20 {
		t.Fatalf("retry.AppliedHistoryEntry = %+v, want the original applied entry echoed back", retry.AppliedHistoryEntry)
	}
}

func TestEditDiaryEntryDistinctOperationSameTargetIsRealNoOp(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	first, applied, err := service.EditDiaryEntry(context.Background(), 1, "op-a", 10, 80, "subtract")
	if err != nil || !applied || first == nil {
		t.Fatalf("EditDiaryEntry() first = (%+v, applied=%v, err=%v)", first, applied, err)
	}
	if first.Version != 1 {
		t.Fatalf("first.Version = %d, want 1 (seeded at 0, incremented once)", first.Version)
	}

	second, applied, err := service.EditDiaryEntry(context.Background(), 1, "op-b", 10, 80, "subtract")
	if err != nil {
		t.Fatalf("EditDiaryEntry() second error = %v", err)
	}
	if !applied {
		t.Fatal("EditDiaryEntry() second applied = false, want true (a distinct, freshly-evaluated operation)")
	}
	if second == nil || second.FoodWeight != 80 || len(second.History) != 2 {
		t.Fatalf("second = %+v, want history unchanged at length 2 (delta is 0)", second)
	}
	if second.AppliedHistoryEntry != nil {
		t.Fatalf("second.AppliedHistoryEntry = %+v, want nil (target equals current value)", second.AppliedHistoryEntry)
	}
	if second.Version != first.Version {
		t.Fatalf("second.Version = %d, want %d (genuine no-op must not bump version)", second.Version, first.Version)
	}
}

func TestEditDiaryEntryDerivesDirectionFromRealDelta(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	updated, _, err := service.EditDiaryEntry(context.Background(), 1, "op-1", 10, 120, "subtract")
	if err != nil {
		t.Fatalf("EditDiaryEntry() error = %v", err)
	}
	if updated == nil || updated.AppliedHistoryEntry == nil {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.AppliedHistoryEntry.Action != "add" || updated.AppliedHistoryEntry.Value != 20 {
		t.Fatalf("AppliedHistoryEntry = %+v, want add:20 regardless of the requested action", updated.AppliedHistoryEntry)
	}
}

func TestDeleteDiaryEntryRetrySucceedsInsteadOfNotFound(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	first, applied, err := service.DeleteDiaryEntry(context.Background(), 1, "op-delete", 10)
	if err != nil || !first || !applied {
		t.Fatalf("DeleteDiaryEntry() first = (%v, applied=%v, err=%v)", first, applied, err)
	}

	retry, applied, err := service.DeleteDiaryEntry(context.Background(), 1, "op-delete", 10)
	if err != nil {
		t.Fatalf("DeleteDiaryEntry() retry error = %v", err)
	}
	if applied {
		t.Fatal("DeleteDiaryEntry() retry applied = true, want false (replayed)")
	}
	if !retry {
		t.Fatal("DeleteDiaryEntry() retry = false, want true (echoes original success, not a 404)")
	}
}

func testPersonalKcalConfig() PersonalKcalConfig {
	return PersonalKcalConfig{
		LookbackMonths:          3,
		DecayRate:               0.6,
		CoverageThreshold:       0,
		MaxMonthlyChangePercent: 50,
		AnchorLambda:            1,
		EvidenceHalfKcal:        100,
		CoefLogStep:             0.05,
		NormStep:                50,
		XStep:                   50,
		Population:              8,
		MaxGenerations:          10,
		MaxStale:                5,
	}
}

func TestRunPersonalKcalJobStoresHistoryAndInvalidatesStats(t *testing.T) {
	db := openFoodTestDB(t)
	seedFoodDiaryAndWeightHistory(t, db, 1)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.July, 5, 12, 0, 0, 0, time.UTC)})
	service.SetPersonalKcalConfig(testPersonalKcalConfig())

	if _, err := service.GetStats(context.Background(), 1); err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if _, ok := service.statsCache.Get(1); !ok {
		t.Fatal("stats cache missing before job run")
	}

	result, err := service.RunPersonalKcalJob(context.Background())
	if err != nil {
		t.Fatalf("RunPersonalKcalJob() error = %v", err)
	}
	if result.SuccessCount != 1 || result.FailedCount != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Users[0].MonthsComputed != 1 {
		t.Fatalf("MonthsComputed = %d, want 1", result.Users[0].MonthsComputed)
	}
	if _, ok := service.statsCache.Get(1); ok {
		t.Fatal("stats cache still present after job run")
	}

	kcalHistory, err := NewRepository(db, sqlite.WriteDB{DB: db}).GetPersonalKcalHistory(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetPersonalKcalHistory() error = %v", err)
	}
	if len(kcalHistory) != 2 {
		t.Fatalf("len(kcalHistory) = %d, want 2 (one row per touched product)", len(kcalHistory))
	}
	normHistory, err := NewRepository(db, sqlite.WriteDB{DB: db}).GetPersonalNormHistory(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetPersonalNormHistory() error = %v", err)
	}
	if len(normHistory) != 1 || normHistory[0].YearMonth != "2026-06" {
		t.Fatalf("normHistory = %+v, want one 2026-06 row", normHistory)
	}

	rerun, err := service.RunPersonalKcalJob(context.Background())
	if err != nil {
		t.Fatalf("RunPersonalKcalJob() second run error = %v", err)
	}
	if rerun.Users[0].MonthsComputed != 0 {
		t.Fatalf("second run MonthsComputed = %d, want 0 (idempotent)", rerun.Users[0].MonthsComputed)
	}
}

func TestRunPersonalKcalJobProcessesAllUsers(t *testing.T) {
	db := openFoodTestDB(t)
	seedFoodDiaryAndWeightHistory(t, db, 1)
	if _, err := db.Exec(`INSERT INTO users(id, username, isAdmin) VALUES (2, 'bob', 0)`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.July, 5, 12, 0, 0, 0, time.UTC)})
	service.SetPersonalKcalConfig(testPersonalKcalConfig())

	result, err := service.RunPersonalKcalJob(context.Background())
	if err != nil {
		t.Fatalf("RunPersonalKcalJob() error = %v", err)
	}
	if result.ProcessedCount != 2 || result.SuccessCount != 2 || result.FailedCount != 0 {
		t.Fatalf("result = %+v", result)
	}
	if result.Users[0].MonthsComputed != 1 {
		t.Fatalf("first user MonthsComputed = %d, want 1", result.Users[0].MonthsComputed)
	}
	if result.Users[1].MonthsComputed != 0 {
		t.Fatalf("second user (no diary) MonthsComputed = %d, want 0", result.Users[1].MonthsComputed)
	}
}

func TestStatsCacheInvalidatesAfterWrites(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	before, err := service.GetStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	beforeValue, ok := before.Days["2026-06-17"]
	if !ok {
		t.Fatal("before stats missing 2026-06-17")
	}
	beforeKcals := beforeValue.ConsumedKcal

	if _, _, err := service.CreateDiaryEntry(context.Background(), 1, "op-1", "2026-06-17", 1, 100, []HistoryEntry{{Action: "init", Value: 100}}); err != nil {
		t.Fatalf("CreateDiaryEntry() error = %v", err)
	}

	after, err := service.GetStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	afterValue, ok := after.Days["2026-06-17"]
	if !ok {
		t.Fatal("after stats missing 2026-06-17")
	}
	afterKcals := afterValue.ConsumedKcal
	if afterKcals <= beforeKcals {
		t.Fatalf("afterKcals = %v, want > %v", afterKcals, beforeKcals)
	}
}

func TestRankProductsSortsAndLimitsToTenPerMetric(t *testing.T) {
	productKcal := map[int64]float64{
		1: 100, 2: 400, 3: 250, 4: 50, 5: 300, 6: 600,
		7: 700, 8: 800, 9: 900, 10: 1000, 11: 1100,
	}
	// Weight order deliberately differs from kcal order (e.g. low-kcal veggies weigh more than
	// calorie-dense products) to prove the two rankings are computed independently.
	productWeight := map[int64]float64{
		1: 1100, 2: 1000, 3: 900, 4: 800, 5: 700, 6: 600,
		7: 500, 8: 400, 9: 300, 10: 250, 11: 100,
	}
	names := map[int64]string{
		1: "One", 2: "Two", 3: "Three", 4: "Four", 5: "Five", 6: "Six",
		7: "Seven", 8: "Eight", 9: "Nine", 10: "Ten", 11: "Eleven",
	}

	products := buildProductStats(productKcal, productWeight, names)

	byKcal := rankProducts(products, func(p ProductStat) float64 { return p.Kcal })
	if len(byKcal) != 10 {
		t.Fatalf("len(byKcal) = %d, want 10", len(byKcal))
	}
	// descending kcal: 1100, 1000, 900, 800, 700, 600, 400, 300, 250, 100 (50 dropped)
	wantKcalOrder := []int64{11, 10, 9, 8, 7, 6, 2, 5, 3, 1}
	for i, id := range wantKcalOrder {
		if byKcal[i].CatalogueID != id {
			t.Fatalf("byKcal[%d].CatalogueID = %d, want %d (byKcal=%+v)", i, byKcal[i].CatalogueID, id, byKcal)
		}
	}

	byWeight := rankProducts(products, func(p ProductStat) float64 { return p.Weight })
	if len(byWeight) != 10 {
		t.Fatalf("len(byWeight) = %d, want 10", len(byWeight))
	}
	// descending weight: 1100, 1000, 900, 800, 700, 600, 500, 400, 300, 250 (100 dropped)
	wantWeightOrder := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for i, id := range wantWeightOrder {
		if byWeight[i].CatalogueID != id {
			t.Fatalf("byWeight[%d].CatalogueID = %d, want %d (byWeight=%+v)", i, byWeight[i].CatalogueID, id, byWeight)
		}
	}
}

func TestGetStatsTopProductsWindowAndTotalEntries(t *testing.T) {
	db := openFoodTestDB(t)
	// openFoodTestDB already seeds catalogue 1=Apple(50kcal/100g), 2=Bread(250kcal/100g) and one
	// diary row (id 10) on 2026-06-17, well outside the 30-day window used below.
	if _, err := db.Exec(`
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(20, '2026-07-10', 1, 200, '[{"action":"init","value":200}]', 1, 0, 0),
			(21, '2026-07-15', 2, 100, '[{"action":"init","value":100}]', 1, 0, 0),
			(22, '2026-07-24', 1, 300, '[{"action":"init","value":300}]', 1, 0, 0),
			(23, '2026-07-25', 2, 1000, '[{"action":"init","value":1000}]', 1, 0, 0);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)})

	stats, err := service.GetStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}

	// TotalEntries counts every diary row regardless of date: base row (10) + the four new ones.
	if stats.TotalEntries != 5 {
		t.Fatalf("TotalEntries = %d, want 5", stats.TotalEntries)
	}

	// Window is [2026-06-25, 2026-07-25): the base row on 2026-06-17 falls outside it (too old)
	// and row 23 falls on "today" (2026-07-25) — both excluded from top-products/window-totals even
	// though both are still part of TotalEntries/Days. Within window: id20 Apple 200g=100kcal,
	// id21 Bread 100g=250kcal, id22 Apple 300g=150kcal. Apple totals 250kcal/500g, Bread totals
	// 250kcal/100g — kcal ties broken by ascending catalogueID (Apple first), weight has Apple
	// first outright (500 > 100).
	if stats.TopProductsWindowTotalKcal != 500 {
		t.Fatalf("TopProductsWindowTotalKcal = %v, want 500", stats.TopProductsWindowTotalKcal)
	}
	if stats.TopProductsWindowTotalWeight != 600 {
		t.Fatalf("TopProductsWindowTotalWeight = %v, want 600", stats.TopProductsWindowTotalWeight)
	}
	if len(stats.TopProductsByKcal) != 2 {
		t.Fatalf("len(TopProductsByKcal) = %d, want 2, got %+v", len(stats.TopProductsByKcal), stats.TopProductsByKcal)
	}
	if stats.TopProductsByKcal[0] != (ProductStat{CatalogueID: 1, Name: "Apple", Kcal: 250, Weight: 500}) {
		t.Fatalf("TopProductsByKcal[0] = %+v, want {1 Apple 250 500}", stats.TopProductsByKcal[0])
	}
	if stats.TopProductsByKcal[1] != (ProductStat{CatalogueID: 2, Name: "Bread", Kcal: 250, Weight: 100}) {
		t.Fatalf("TopProductsByKcal[1] = %+v, want {2 Bread 250 100}", stats.TopProductsByKcal[1])
	}
	if len(stats.TopProductsByWeight) != 2 {
		t.Fatalf("len(TopProductsByWeight) = %d, want 2, got %+v", len(stats.TopProductsByWeight), stats.TopProductsByWeight)
	}
	if stats.TopProductsByWeight[0] != (ProductStat{CatalogueID: 1, Name: "Apple", Kcal: 250, Weight: 500}) {
		t.Fatalf("TopProductsByWeight[0] = %+v, want {1 Apple 250 500}", stats.TopProductsByWeight[0])
	}
	if stats.TopProductsByWeight[1] != (ProductStat{CatalogueID: 2, Name: "Bread", Kcal: 250, Weight: 100}) {
		t.Fatalf("TopProductsByWeight[1] = %+v, want {2 Bread 250 100}", stats.TopProductsByWeight[1])
	}
}

func TestGetStatsSummaryAllTimeRecordsSurviveWindowTrim(t *testing.T) {
	db := openFoodTestDB(t)
	// openFoodTestDB seeds user 1's first day as 2026-06-17 (foodDiary row 10, Bread 100g=250kcal,
	// foodBodyWeight 80kg). No personalNormHistory rows exist, so AppliedNorm falls back to the
	// package default of 2200 kcal for every month.
	if _, err := db.Exec(`
		INSERT INTO foodBodyWeight(dateISO, weight, usersId) VALUES
			('2026-07-25', 72, 1),
			('2026-08-01', 65, 1),
			('2027-01-01', 90, 1),
			('2027-07-25', 67, 1);
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(20, '2026-09-01', 2, 1000, '[{"action":"init","value":1000}]', 1, 0, 0),
			(21, '2026-09-02', 1, 40, '[{"action":"init","value":40}]', 1, 0, 0);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2027, time.July, 25, 12, 0, 0, 0, time.UTC)})

	stats, err := service.GetStats(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}

	summary := stats.Summary
	if summary.DaysInDiary != len(stats.Days) {
		t.Fatalf("DaysInDiary = %d, want %d (len(Days))", summary.DaysInDiary, len(stats.Days))
	}
	// Global min/max hold regardless of the linear interpolation prepareWeights fills between known
	// points, since interpolated values never leave the [min(neighbours), max(neighbours)] range.
	if summary.MinWeight == nil || summary.MinWeight.Weight != 65 || summary.MinWeight.DateISO != "2026-08-01" {
		t.Fatalf("MinWeight = %+v, want {65 2026-08-01}", summary.MinWeight)
	}
	if summary.MaxWeight == nil || summary.MaxWeight.Weight != 90 || summary.MaxWeight.DateISO != "2027-01-01" {
		t.Fatalf("MaxWeight = %+v, want {90 2027-01-01}", summary.MaxWeight)
	}
	// 2026-09-01: Bread 1000g = 2500kcal / 2200 target = 113.6% -> rounds to 114, the highest ratio.
	if summary.MostCaloricDay == nil || summary.MostCaloricDay.Percent != 114 || summary.MostCaloricDay.DateISO != "2026-09-01" {
		t.Fatalf("MostCaloricDay = %+v, want {114 2026-09-01}", summary.MostCaloricDay)
	}
	// 2026-09-02: Apple 40g = 20kcal / 2200 target = 0.9% -> rounds to 1, the lowest non-zero ratio.
	if summary.LeastCaloricDay == nil || summary.LeastCaloricDay.Percent != 1 || summary.LeastCaloricDay.DateISO != "2026-09-02" {
		t.Fatalf("LeastCaloricDay = %+v, want {1 2026-09-02}", summary.LeastCaloricDay)
	}
	// First weighted day is 2026-06-17 (80kg, the account's first day); today (2027-07-25) is 67kg.
	if summary.WeightChangeSinceStartKg == nil || *summary.WeightChangeSinceStartKg != -13 {
		t.Fatalf("WeightChangeSinceStartKg = %v, want -13", summary.WeightChangeSinceStartKg)
	}
	if summary.YearAgo == nil || summary.YearAgo.DateISO != "2026-07-25" || summary.YearAgo.WeightThen != 72 ||
		summary.YearAgo.WeightNow != 67 || summary.YearAgo.DeltaKg != -5 {
		t.Fatalf("YearAgo = %+v, want {2026-07-25 72 67 -5}", summary.YearAgo)
	}

	// TrimStatsToRecentWindow only narrows Days (last 90 days from "today") — Summary must stay the
	// full-history aggregate untouched, so milestones stay correct in the default windowed response
	// too, not just when the client opts into ?from=all.
	trimmed := service.TrimStatsToRecentWindow(stats)
	if trimmed.Summary != summary {
		t.Fatalf("TrimStatsToRecentWindow() changed Summary: got %+v, want unchanged %+v", trimmed.Summary, summary)
	}
	if _, ok := trimmed.Days["2026-06-17"]; ok {
		t.Fatal(`trimmed.Days still contains "2026-06-17", want it dropped by the 90-day window`)
	}
	if len(trimmed.Days) >= len(stats.Days) {
		t.Fatalf("len(trimmed.Days) = %d, want fewer than len(stats.Days) = %d", len(trimmed.Days), len(stats.Days))
	}
}

func TestSearchPreviewAndSaveProduct(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetProductGenerator(fakeProductGenerator{})
	service.SetEmbeddingGenerator(fakeEmbeddingGenerator{})

	results, err := service.SearchCatalogue(context.Background(), "apple-semantic")
	if err != nil {
		t.Fatalf("SearchCatalogue() error = %v", err)
	}
	if len(results) == 0 || results[0].Name != "Apple" {
		t.Fatalf("results = %+v, want Apple first", results)
	}

	preview, err := service.GenerateProductPreview(context.Background(), "Apple")
	if err != nil {
		t.Fatalf("GenerateProductPreview() error = %v", err)
	}
	if preview != fakeGeneratedProductPreview {
		t.Fatalf("preview = %+v", preview)
	}

	entry, applied, err := service.SaveProduct(context.Background(), 1, "op-save-orange", nil, ProductInput{
		Name:        "Orange",
		Kcals:       47,
		Protein:     1,
		Fat:         0,
		Carbs:       12,
		Fiber:       2,
		Description: "Orange fruit",
	})
	if err != nil {
		t.Fatalf("SaveProduct() error = %v", err)
	}
	if !applied {
		t.Fatal("SaveProduct() applied = false, want true")
	}
	if entry == nil || entry.ID <= 0 {
		t.Fatalf("entry = %+v", entry)
	}

	deleted, applied, err := service.DeleteProduct(context.Background(), 1, "op-delete-orange", entry.ID)
	if err != nil {
		t.Fatalf("DeleteProduct() error = %v", err)
	}
	if !applied {
		t.Fatal("DeleteProduct() applied = false, want true")
	}
	if !deleted {
		t.Fatal("DeleteProduct() = false, want true")
	}
}

type fixedEmbeddingGenerator struct {
	vectors map[string][]float64
}

func (g fixedEmbeddingGenerator) GenerateEmbedding(ctx context.Context, text string) ([]float64, error) {
	if vec, ok := g.vectors[text]; ok {
		return vec, nil
	}
	return []float64{0, 0}, nil
}

// Regression test for a bug where a query typed in the wrong keyboard layout (e.g. "rehbwf"
// instead of "курица") could return only whatever the semantic embedding search happened to
// find nearest — even an unrelated product — because that search had no relevance threshold
// and any non-empty result short-circuited the transliteration fallback before it ever ran.
// searchCatalogueIDs now always combines the text/transliteration score with the semantic
// score instead of choosing one or the other.
func TestSearchCombinesTransliterationWithSemanticScore(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	// 3 dimensions so these vectors can't accidentally collide with openFoodTestDB's
	// pre-seeded 2-dimensional Apple/Bread vectors (combinedDistance skips entries whose
	// vector length doesn't match the query's).
	service.SetEmbeddingGenerator(fixedEmbeddingGenerator{vectors: map[string][]float64{
		"Курица":  {1, 0, 0},
		"Тапочки": {0, 1, 0},
		// "rehbwf" is "курица" typed on a Latin keyboard layout — semantically meaningless,
		// but its embedding happens to land right on an unrelated product's vector, so a
		// naive semantic search would confidently report Тапочки as the match.
		"rehbwf": {0, 1, 0},
	}})

	saveProduct := func(name, operationID string) {
		t.Helper()
		if _, applied, err := service.SaveProduct(context.Background(), 1, operationID, nil, ProductInput{
			Name: name, Description: name, Kcals: 100, Protein: 10, Fat: 5, Carbs: 5, Fiber: 1,
		}); err != nil || !applied {
			t.Fatalf("SaveProduct(%q) error = %v applied = %v", name, err, applied)
		}
	}
	saveProduct("Курица", "op-chicken")
	saveProduct("Тапочки", "op-slippers")

	results, err := service.SearchCatalogue(context.Background(), "rehbwf")
	if err != nil {
		t.Fatalf("SearchCatalogue() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want both Курица (via transliteration) and Тапочки (via semantic match)", results)
	}
	if results[0].Name != "Курица" {
		t.Fatalf("results[0].Name = %q, want the transliterated exact match to outrank the semantic-only match", results[0].Name)
	}
}

func TestSaveProductSameOperationIDReplaysWithoutDuplicate(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetEmbeddingGenerator(fakeEmbeddingGenerator{})

	input := ProductInput{Name: "Kiwi", Kcals: 61, Protein: 1, Fat: 1, Carbs: 15, Fiber: 3, Description: "Kiwi fruit"}

	first, applied, err := service.SaveProduct(context.Background(), 1, "op-kiwi", nil, input)
	if err != nil {
		t.Fatalf("SaveProduct() first call error = %v", err)
	}
	if !applied {
		t.Fatal("SaveProduct() first call applied = false, want true")
	}

	second, applied, err := service.SaveProduct(context.Background(), 1, "op-kiwi", nil, input)
	if err != nil {
		t.Fatalf("SaveProduct() retry error = %v", err)
	}
	if applied {
		t.Fatal("SaveProduct() retry applied = true, want false (replay)")
	}
	if second.ID != first.ID {
		t.Fatalf("SaveProduct() retry created a different entry: first.ID = %d, second.ID = %d", first.ID, second.ID)
	}

	all, err := service.GetCatalogue(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogue() error = %v", err)
	}
	count := 0
	for _, e := range all {
		if e.Name == "Kiwi" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("catalogue has %d entries named Kiwi, want 1 (no duplicate from retry)", count)
	}
}

func TestDeleteProductRetrySucceedsInsteadOfNotFound(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetEmbeddingGenerator(fakeEmbeddingGenerator{})

	entry, _, err := service.SaveProduct(context.Background(), 1, "op-mango-create", nil, ProductInput{
		Name: "Mango", Kcals: 60, Protein: 1, Fat: 0, Carbs: 15, Fiber: 2, Description: "Mango fruit",
	})
	if err != nil {
		t.Fatalf("SaveProduct() error = %v", err)
	}

	deleted, applied, err := service.DeleteProduct(context.Background(), 1, "op-mango-delete", entry.ID)
	if err != nil {
		t.Fatalf("DeleteProduct() first call error = %v", err)
	}
	if !deleted || !applied {
		t.Fatalf("DeleteProduct() first call = (deleted=%v, applied=%v), want (true, true)", deleted, applied)
	}

	// Retry with the same operationId after the entry is already gone — must replay the
	// original success, not report "not found" for an entry that was in fact deleted.
	deleted, applied, err = service.DeleteProduct(context.Background(), 1, "op-mango-delete", entry.ID)
	if err != nil {
		t.Fatalf("DeleteProduct() retry error = %v", err)
	}
	if !deleted {
		t.Fatal("DeleteProduct() retry deleted = false, want true (replay of prior success)")
	}
	if applied {
		t.Fatal("DeleteProduct() retry applied = true, want false (replay)")
	}
}

func TestGetDiaryFullUpdateReturnsFoodAndNutrients(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	result, err := service.GetDiaryFullUpdate(context.Background(), 1, "2026-06-17", 1)
	if err != nil {
		t.Fatalf("GetDiaryFullUpdate() error = %v", err)
	}

	day, ok := result["2026-06-17"]
	if !ok {
		t.Fatal("day not found")
	}
	if len(day.Food) != 1 {
		t.Fatalf("len(day.Food) = %d, want 1", len(day.Food))
	}
	if day.Food[10].Version != 0 {
		t.Fatalf("Food[10].Version = %d, want 0 (seeded value, read back unchanged)", day.Food[10].Version)
	}
	if day.BodyWeight == nil || *day.BodyWeight != 80 {
		t.Fatalf("BodyWeight = %v, want 80", day.BodyWeight)
	}
	if day.Nutrients.ConsumedKcals == 250 {
		return
	}
	if day.Nutrients.ConsumedKcals != 250 {
		t.Fatalf("ConsumedKcals = %d, want 250", day.Nutrients.ConsumedKcals)
	}
}

func TestGetDiaryFullUpdateIgnoresRowsOutsideRequestedRange(t *testing.T) {
	db := openFoodTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(11, '2026-06-19', 1, 120, '[{"action":"init","value":120}]', 1, 0, 0);
		INSERT INTO foodBodyWeight(dateISO, weight, usersId) VALUES ('2026-06-19', 81, 1);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	result, err := service.GetDiaryFullUpdate(context.Background(), 1, "2026-06-17", 1)
	if err != nil {
		t.Fatalf("GetDiaryFullUpdate() error = %v", err)
	}

	if _, ok := result["2026-06-19"]; ok {
		t.Fatal("unexpected out-of-range day present")
	}
	day := result["2026-06-18"]
	if len(day.Food) != 0 {
		t.Fatalf("len(day.Food) = %d, want 0", len(day.Food))
	}
	if day.BodyWeight != nil {
		t.Fatalf("BodyWeight = %v, want nil", day.BodyWeight)
	}
}

func TestGetDiaryFullUpdateHandlesLargeOffset(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	result, err := service.GetDiaryFullUpdate(context.Background(), 1, "2026-06-17", 30)
	if err != nil {
		t.Fatalf("GetDiaryFullUpdate() error = %v", err)
	}

	if len(result) != 61 {
		t.Fatalf("len(result) = %d, want 61 (30 days either side of the center, inclusive)", len(result))
	}
	if len(result["2026-06-17"].Food) != 1 {
		t.Fatalf("len(result[2026-06-17].Food) = %d, want 1", len(result["2026-06-17"].Food))
	}
	if len(result["2026-05-18"].Food) != 0 {
		t.Fatalf("len(result[2026-05-18].Food) = %d, want 0 (start of window, no seeded data)", len(result["2026-05-18"].Food))
	}
	if len(result["2026-07-17"].Food) != 0 {
		t.Fatalf("len(result[2026-07-17].Food) = %d, want 0 (end of window, no seeded data)", len(result["2026-07-17"].Food))
	}
}

// The frontend no longer clamps its request window to "today" — a segment centered near today
// legitimately asks for a few days past it too, expecting nothing there rather than an error.
func TestGetDiaryFullUpdateHandlesOffsetPastLatestData(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	result, err := service.GetDiaryFullUpdate(context.Background(), 1, "2026-07-01", 7)
	if err != nil {
		t.Fatalf("GetDiaryFullUpdate() error = %v", err)
	}

	if len(result) != 15 {
		t.Fatalf("len(result) = %d, want 15", len(result))
	}
	for dateISO, day := range result {
		if len(day.Food) != 0 {
			t.Fatalf("result[%s].Food = %v, want empty (window entirely past any seeded data)", dateISO, day.Food)
		}
		if day.BodyWeight != nil {
			t.Fatalf("result[%s].BodyWeight = %v, want nil", dateISO, day.BodyWeight)
		}
	}
}

// seedProductHistoryRows adds, on top of openFoodTestDB's base row (id 10, 2026-06-17, Bread,
// user 1), four more Apple (catalogueId 1) events for user 1 spanning three dates including two
// events on the same day — every foodDiary row is its own event, never merged (see write_repo.go),
// so id20/id21 on the same date must both survive as distinct rows ordered by id DESC.
func seedProductHistoryRows(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(20, '2026-06-18', 1, 88, '[{"action":"init","value":88}]', 1, 0, 0),
			(21, '2026-06-18', 1, 176, '[{"action":"init","value":176}]', 1, 0, 0),
			(22, '2026-06-19', 1, 440, '[{"action":"init","value":440}]', 1, 0, 0),
			(23, '2026-06-16', 1, 10, '[{"action":"init","value":10}]', 1, 0, 0);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
}

func TestRepositoryGetProductHistoryKeysetPaginationAcrossProducts(t *testing.T) {
	db := openFoodTestDB(t)
	seedProductHistoryRows(t, db)
	repo := NewRepository(db, sqlite.WriteDB{DB: db})
	ctx := context.Background()

	// Both catalogue ids, newest first: id22(06-19) id21(06-18) id20(06-18) id10(06-17,Bread)
	// id23(06-16) — same-day rows (21,20) must break ties by id DESC, not insertion order alone.
	page1, err := repo.GetProductHistory(ctx, 1, []int64{1, 2}, nil, 2)
	if err != nil {
		t.Fatalf("GetProductHistory() page1 error = %v", err)
	}
	assertProductHistoryIDs(t, page1, []int64{22, 21})

	page2, err := repo.GetProductHistory(ctx, 1, []int64{1, 2}, &ProductHistoryCursor{DateISO: page1[1].DateISO, ID: page1[1].ID}, 2)
	if err != nil {
		t.Fatalf("GetProductHistory() page2 error = %v", err)
	}
	assertProductHistoryIDs(t, page2, []int64{20, 10})

	page3, err := repo.GetProductHistory(ctx, 1, []int64{1, 2}, &ProductHistoryCursor{DateISO: page2[1].DateISO, ID: page2[1].ID}, 2)
	if err != nil {
		t.Fatalf("GetProductHistory() page3 error = %v", err)
	}
	assertProductHistoryIDs(t, page3, []int64{23})

	// A single catalogue id excludes the other product entirely, no cross-contamination.
	appleOnly, err := repo.GetProductHistory(ctx, 1, []int64{1}, nil, 10)
	if err != nil {
		t.Fatalf("GetProductHistory() apple-only error = %v", err)
	}
	assertProductHistoryIDs(t, appleOnly, []int64{22, 21, 20, 23})

	empty, err := repo.GetProductHistory(ctx, 1, nil, nil, 10)
	if err != nil {
		t.Fatalf("GetProductHistory() empty ids error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("GetProductHistory() with no catalogue ids = %+v, want empty", empty)
	}
}

func assertProductHistoryIDs(t *testing.T, rows []ProductHistoryRow, wantIDs []int64) {
	t.Helper()
	if len(rows) != len(wantIDs) {
		t.Fatalf("len(rows) = %d, want %d (rows=%+v)", len(rows), len(wantIDs), rows)
	}
	for i, want := range wantIDs {
		if rows[i].ID != want {
			t.Fatalf("rows[%d].ID = %d, want %d (rows=%+v)", i, rows[i].ID, want, rows)
		}
	}
}

func TestServiceGetProductHistoryComputesKcalAndPercentAndPaginates(t *testing.T) {
	db := openFoodTestDB(t)
	seedProductHistoryRows(t, db)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	ctx := context.Background()

	// No personalKcalHistory/personalNormHistory rows seeded, so Apple stays at its catalogue
	// default (50 kcal/100g) and the norm stays at the package default (2200) for every month —
	// weights were chosen so kcal and percent both land on exact integers (no rounding ambiguity).
	page1, err := service.GetProductHistory(ctx, 1, []int64{1}, nil, 2)
	if err != nil {
		t.Fatalf("GetProductHistory() page1 error = %v", err)
	}
	wantPage1 := []ProductHistoryEntry{
		{DateISO: "2026-06-19", FoodCatalogueID: 1, FoodWeight: 440, PercentOfNorm: 10},
		{DateISO: "2026-06-18", FoodCatalogueID: 1, FoodWeight: 176, PercentOfNorm: 4},
	}
	if len(page1.Entries) != len(wantPage1) {
		t.Fatalf("page1.Entries = %+v, want %+v", page1.Entries, wantPage1)
	}
	for i, want := range wantPage1 {
		if page1.Entries[i] != want {
			t.Fatalf("page1.Entries[%d] = %+v, want %+v", i, page1.Entries[i], want)
		}
	}
	if page1.NextCursor == nil || page1.NextCursor.DateISO != "2026-06-18" || page1.NextCursor.ID != 21 {
		t.Fatalf("page1.NextCursor = %+v, want {2026-06-18 21}", page1.NextCursor)
	}

	page2, err := service.GetProductHistory(ctx, 1, []int64{1}, page1.NextCursor, 2)
	if err != nil {
		t.Fatalf("GetProductHistory() page2 error = %v", err)
	}
	wantPage2 := []ProductHistoryEntry{
		{DateISO: "2026-06-18", FoodCatalogueID: 1, FoodWeight: 88, PercentOfNorm: 2},
		{DateISO: "2026-06-16", FoodCatalogueID: 1, FoodWeight: 10, PercentOfNorm: 0},
	}
	if len(page2.Entries) != len(wantPage2) {
		t.Fatalf("page2.Entries = %+v, want %+v", page2.Entries, wantPage2)
	}
	for i, want := range wantPage2 {
		if page2.Entries[i] != want {
			t.Fatalf("page2.Entries[%d] = %+v, want %+v", i, page2.Entries[i], want)
		}
	}
	if page2.NextCursor != nil {
		t.Fatalf("page2.NextCursor = %+v, want nil (last page)", page2.NextCursor)
	}
}

func TestServiceGetProductHistoryEmptyCatalogueIDsReturnsEmptyPage(t *testing.T) {
	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))

	page, err := service.GetProductHistory(context.Background(), 1, nil, nil, 10)
	if err != nil {
		t.Fatalf("GetProductHistory() error = %v", err)
	}
	if len(page.Entries) != 0 || page.NextCursor != nil {
		t.Fatalf("GetProductHistory() with no ids = %+v, want empty page", page)
	}
}

func seedFoodDiaryAndWeightHistory(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(101, '2026-06-10', 1, 180, '[{"action":"init","value":180}]', ?, 0, 0),
			(102, '2026-06-11', 2, 140, '[{"action":"init","value":140}]', ?, 0, 0),
			(103, '2026-06-12', 1, 190, '[{"action":"init","value":190}]', ?, 0, 0),
			(104, '2026-06-13', 2, 150, '[{"action":"init","value":150}]', ?, 0, 0),
			(105, '2026-06-14', 1, 200, '[{"action":"init","value":200}]', ?, 0, 0),
			(106, '2026-06-15', 2, 160, '[{"action":"init","value":160}]', ?, 0, 0),
			(107, '2026-06-16', 1, 170, '[{"action":"init","value":170}]', ?, 0, 0);
		INSERT INTO foodBodyWeight(dateISO, weight, usersId) VALUES
			('2026-06-10', 80.8, ?),
			('2026-06-11', 80.6, ?),
			('2026-06-12', 80.4, ?),
			('2026-06-13', 80.2, ?),
			('2026-06-14', 80.1, ?),
			('2026-06-15', 79.9, ?),
			('2026-06-16', 79.8, ?);
	`, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
}

func openFoodTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT, hashedPassword TEXT, isAdmin BOOLEAN);
		CREATE TABLE auth_sessions (id TEXT PRIMARY KEY, secretHash BLOB NOT NULL, userId INTEGER NOT NULL, createdAt TEXT NOT NULL, expiresAt TEXT NOT NULL, renewedAt TEXT NOT NULL, revokedAt TEXT);
		CREATE TABLE settings (id INTEGER PRIMARY KEY AUTOINCREMENT, usersId INTEGER, goal TEXT, darkTheme BOOLEAN, selectedChapterFood BOOLEAN, selectedChapterMoney BOOLEAN, liteVersion BOOLEAN, height INTEGER);
		CREATE TABLE foodSettings (id INTEGER PRIMARY KEY AUTOINCREMENT, height INTEGER, useCoeffs BOOLEAN, coefficients TEXT, usersId INTEGER);
		CREATE TABLE foodCatalogue (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, kcals INTEGER, protein REAL, fat REAL, carbs REAL, fiber REAL, description TEXT, legacyName TEXT, nameVec BLOB, descriptionVec BLOB, archived INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE foodDiary (id INTEGER PRIMARY KEY AUTOINCREMENT, dateISO TEXT, foodCatalogueId INTEGER, foodWeight INTEGER, history TEXT, usersId INTEGER, ver INTEGER, del BOOLEAN);
		CREATE TABLE foodBodyWeight (id INTEGER PRIMARY KEY AUTOINCREMENT, dateISO TEXT, weight NUMERIC, usersId INTEGER);
		CREATE TABLE foodSearchQueryEmbeddings (query TEXT PRIMARY KEY, embedding BLOB, hitCount INTEGER, lastUsedAt INTEGER, createdAt INTEGER);
		CREATE TABLE foodPersonalKcalHistory (id INTEGER PRIMARY KEY AUTOINCREMENT, usersId INTEGER NOT NULL, foodCatalogueId INTEGER NOT NULL, yearMonth TEXT NOT NULL, kcalsPer100g REAL NOT NULL, createdAt TEXT NOT NULL, UNIQUE(usersId, foodCatalogueId, yearMonth));
		CREATE TABLE foodPersonalNormHistory (id INTEGER PRIMARY KEY AUTOINCREMENT, usersId INTEGER NOT NULL, yearMonth TEXT NOT NULL, normKcals REAL NOT NULL, kcalPerKg REAL NOT NULL, createdAt TEXT NOT NULL, UNIQUE(usersId, yearMonth));
		CREATE TABLE syncOperations (id TEXT PRIMARY KEY, userId INTEGER NOT NULL, createdAt TEXT NOT NULL, resultJSON TEXT NOT NULL);

		INSERT INTO users(id, username, hashedPassword, isAdmin) VALUES (1, 'alice', '', 0);
		INSERT INTO settings(usersId, goal, darkTheme, selectedChapterFood, selectedChapterMoney, liteVersion, height) VALUES (1, 'lose', 0, 1, 0, 0, 180);
		INSERT INTO foodCatalogue(id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec) VALUES
			(1, 'Apple', 50, 1, 0, 10, 2, 'Fruit', 'Apple', X'0000803F00000000', X'0000803F00000000'),
			(2, 'Bread', 250, 9, 2, 49, 3, 'Bread', 'Bread', X'000000000000803F', X'000000000000803F');
		INSERT INTO foodDiary(id, dateISO, foodCatalogueId, foodWeight, history, usersId, ver, del) VALUES
			(10, '2026-06-17', 2, 100, '[{"action":"init","value":100}]', 1, 0, 0);
		INSERT INTO foodBodyWeight(dateISO, weight, usersId) VALUES ('2026-06-17', 80, 1);
		INSERT INTO foodSearchQueryEmbeddings(query, embedding, hitCount, lastUsedAt, createdAt) VALUES ('apple-semantic', X'0000803F00000000', 1, 0, 0);
	`); err != nil {
		_ = db.Close()
		t.Fatalf("Exec() error = %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}
