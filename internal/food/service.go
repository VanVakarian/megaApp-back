package food

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"megaapp-back/internal/httpx/legacy"
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/idempotency"
)

const kcalsIn1KG = 7700

// AdminChecker looks the user's role up at call time (not cached), so a demoted admin loses
// the archive rights immediately.
type AdminChecker interface {
	IsAdmin(ctx context.Context, userID int64) (bool, error)
}

type Service struct {
	repo                   *Repository
	idempotency            *idempotency.Store
	statsCache             *StatsCache
	catalogueCache         *CatalogueCache
	searchCache            *SearchCache
	productGenerator       ProductGenerator
	embeddingGenerator     EmbeddingGenerator
	adminChecker           AdminChecker
	imageGenerationRequest ImageGenerationRequester
	imageVersions          ImageVersionProvider
	clock                  clockplatform.Clock
	personalKcalConfig     PersonalKcalConfig
	personalKcalRunMu      sync.Mutex
	personalKcalJobActive  bool
}

type DiaryEntry struct {
	ID                  int64          `json:"id"`
	DateISO             string         `json:"dateISO"`
	FoodCatalogueID     int64          `json:"foodCatalogueId"`
	FoodWeight          int64          `json:"foodWeight"`
	Kcals               int64          `json:"kcals"`
	History             []HistoryEntry `json:"history"`
	Version             int64          `json:"version"`
	AppliedHistoryEntry *HistoryEntry  `json:"appliedHistoryEntry,omitempty"`
}

const (
	historyActionInit     = "init"
	historyActionSet      = "set"
	historyActionAdd      = "add"
	historyActionSubtract = "subtract"
)

type RestoreDiaryEntryInput struct {
	FoodCatalogueID int64
	FoodWeight      int64
	History         []HistoryEntry
}

type HistoryEntry struct {
	Action string `json:"action"`
	Value  int64  `json:"value"`
}

type DayNutrients struct {
	TargetKcals     *int64 `json:"targetKcals"`
	ConsumedKcals   int64  `json:"consumedKcals"`
	TargetProtein   int64  `json:"targetProtein"`
	TargetFat       int64  `json:"targetFat"`
	TargetCarbs     int64  `json:"targetCarbs"`
	TargetFiber     int64  `json:"targetFiber"`
	ConsumedProtein int64  `json:"consumedProtein"`
	ConsumedFat     int64  `json:"consumedFat"`
	ConsumedCarbs   int64  `json:"consumedCarbs"`
	ConsumedFiber   int64  `json:"consumedFiber"`
}

type DiaryDay struct {
	Food       map[int64]DiaryEntry `json:"food"`
	BodyWeight *float64             `json:"bodyWeight"`
	Nutrients  DayNutrients         `json:"nutrients"`
}

type DayStats struct {
	Weight       float64 `json:"weight"`
	WeightAvg    float64 `json:"weightAvg"`
	ConsumedKcal float64 `json:"consumedKcal"`
	TargetKcal   float64 `json:"targetKcal"`
	HasNoData    bool    `json:"hasNoData"`
}

type ProductStat struct {
	CatalogueID int64   `json:"catalogueId"`
	Name        string  `json:"name"`
	Kcal        float64 `json:"kcal"`
	Weight      float64 `json:"weight"`
}

type WeightRecord struct {
	Weight  float64 `json:"weight"`
	DateISO string  `json:"dateISO"`
}

type CaloricDayRecord struct {
	Percent float64 `json:"percent"`
	DateISO string  `json:"dateISO"`
}

type YearAgoRecord struct {
	DateISO    string  `json:"dateISO"`
	WeightThen float64 `json:"weightThen"`
	WeightNow  float64 `json:"weightNow"`
	DeltaKg    float64 `json:"deltaKg"`
}

// StatsSummary holds all-time scalar aggregates (records, "since start" delta, year-ago
// comparison) computed once over the account's full history — unlike Days, it is never trimmed
// by TrimStatsToRecentWindow, so it's correct in every response regardless of the requested
// window. Exists so the frontend's "milestones" UI doesn't need the full day-level history loaded
// client-side just to derive seven scalars.
type StatsSummary struct {
	DaysInDiary              int               `json:"daysInDiary"`
	MinWeight                *WeightRecord     `json:"minWeight"`
	MaxWeight                *WeightRecord     `json:"maxWeight"`
	MostCaloricDay           *CaloricDayRecord `json:"mostCaloricDay"`
	LeastCaloricDay          *CaloricDayRecord `json:"leastCaloricDay"`
	WeightChangeSinceStartKg *float64          `json:"weightChangeSinceStartKg"`
	YearAgo                  *YearAgoRecord    `json:"yearAgo"`
}

type StatsResponse struct {
	Days                         map[string]DayStats `json:"days"`
	Summary                      StatsSummary        `json:"summary"`
	TopProductsByKcal            []ProductStat       `json:"topProductsByKcal"`
	TopProductsByWeight          []ProductStat       `json:"topProductsByWeight"`
	TopProductsWindowTotalKcal   float64             `json:"topProductsWindowTotalKcal"`
	TopProductsWindowTotalWeight float64             `json:"topProductsWindowTotalWeight"`
	TotalEntries                 int                 `json:"totalEntries"`
}

const topProductsCount = 10
const topProductsWindowDays = 30

type CatalogueEntry struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	LegacyName   *string `json:"legacyName,omitempty"`
	Kcals        int64   `json:"kcals"`
	Protein      float64 `json:"protein"`
	Fat          float64 `json:"fat"`
	Carbs        float64 `json:"carbs"`
	Fiber        float64 `json:"fiber"`
	Description  string  `json:"description"`
	Archived     bool    `json:"archived"`
	ImageVersion *int64  `json:"imageVersion,omitempty"`
	CanDelete    *bool   `json:"canDelete,omitempty"`
}

func NewService(repo *Repository, idempotencyStore *idempotency.Store) *Service {
	return &Service{repo: repo, idempotency: idempotencyStore, statsCache: NewStatsCache(), catalogueCache: NewCatalogueCache(), searchCache: NewSearchCache(), clock: clockplatform.NewRealClock(), personalKcalConfig: DefaultPersonalKcalConfig()}
}

func (s *Service) SetProductGenerator(generator ProductGenerator) {
	s.productGenerator = generator
}

func (s *Service) SetEmbeddingGenerator(generator EmbeddingGenerator) {
	s.embeddingGenerator = generator
}

func (s *Service) SetAdminChecker(checker AdminChecker) {
	s.adminChecker = checker
}

func (s *Service) SetImageGenerationRequester(requester ImageGenerationRequester) {
	s.imageGenerationRequest = requester
}

func (s *Service) SetImageVersionProvider(provider ImageVersionProvider) {
	s.imageVersions = provider
}

func (s *Service) SetClock(clk clockplatform.Clock) {
	if clk == nil {
		return
	}

	s.clock = clk
}

// buildPersonalKcalResolver loads a user's full personal-kcal history (and the shared
// catalogue) once and returns a resolver that can answer "applied value as of month X" for
// any product/norm/X without further DB round-trips (§7.1, §7.3).
func (s *Service) buildPersonalKcalResolver(ctx context.Context, userID int64) (*PersonalKcalResolver, map[int64]string, error) {
	firstDate, err := s.repo.GetUserFirstDate(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	catalogueRows, err := s.repo.GetCatalogue(ctx)
	if err != nil {
		return nil, nil, err
	}
	catalogueKcals := make(map[int64]float64, len(catalogueRows))
	catalogueNameByID := make(map[int64]string, len(catalogueRows))
	for _, row := range catalogueRows {
		catalogueKcals[row.ID] = float64(row.Kcals)
		catalogueNameByID[row.ID] = row.Name
	}
	kcalHistoryRows, err := s.repo.GetPersonalKcalHistory(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	normHistoryRows, err := s.repo.GetPersonalNormHistory(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	currentYearMonth := s.clock.Now().UTC().Format("2006-01")
	firstYearMonth := currentYearMonth
	if firstDate != "" {
		firstYearMonth = firstDate[:7]
	}
	allMonths := sequentialYearMonths(firstYearMonth, currentYearMonth)
	resolver := NewPersonalKcalResolver(allMonths, catalogueKcals, kcalHistoryRows, normHistoryRows, s.personalKcalConfig)
	return resolver, catalogueNameByID, nil
}

// GetPersonalKcalsNow is the direct replacement for GetCoefficients: a complete
// catalogueId->kcal/100g map for the CURRENT month, used only for live client-side preview of
// an unsaved diary entry (§7.3, §7.4) — every other consumer resolves "at the entry's own
// month" through the resolver instead.
func (s *Service) GetPersonalKcalsNow(ctx context.Context, userID int64) (map[int64]float64, error) {
	resolver, _, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return nil, err
	}
	currentYearMonth := s.clock.Now().UTC().Format("2006-01")
	return resolver.AppliedKcalsNow(currentYearMonth), nil
}

const productHistoryDefaultLimit = 30
const productHistoryMaxLimit = 100

// ProductHistoryCursor marks a position in the product-history feed (the (dateISO, id) of the
// last row already delivered) — sent back to the client as NextCursor and echoed back on the
// following page request. Shared as-is between the repository query param and the API response.
type ProductHistoryCursor struct {
	DateISO string `json:"dateISO"`
	ID      int64  `json:"id"`
}

// ProductHistoryEntry is one consumption event for a tracked product — grams are only ever
// meaningful together with which catalogue id they belong to, since a page can interleave
// several tracked products sorted purely by date. Kcal is deliberately not exposed here — the
// frontend only ever shows grams and percent-of-norm, kcal is purely an intermediate value for
// computing PercentOfNorm below.
type ProductHistoryEntry struct {
	DateISO         string  `json:"dateISO"`
	FoodCatalogueID int64   `json:"foodCatalogueId"`
	FoodWeight      int64   `json:"foodWeight"`
	PercentOfNorm   float64 `json:"percentOfNorm"`
}

type ProductHistoryPage struct {
	Entries    []ProductHistoryEntry `json:"entries"`
	NextCursor *ProductHistoryCursor `json:"nextCursor,omitempty"`
}

// GetProductHistory answers "every time the user ate any of these products, newest first" — no
// caching, a live query on every call (unlike GetCatalogue/GetStats), since the tracked-product
// set changes on every add/remove and stale results would be actively wrong here.
func (s *Service) GetProductHistory(ctx context.Context, userID int64, catalogueIDs []int64, cursor *ProductHistoryCursor, limit int) (ProductHistoryPage, error) {
	if limit <= 0 {
		limit = productHistoryDefaultLimit
	}
	if limit > productHistoryMaxLimit {
		limit = productHistoryMaxLimit
	}
	if len(catalogueIDs) == 0 {
		return ProductHistoryPage{Entries: []ProductHistoryEntry{}}, nil
	}

	// Fetch one extra row to know whether a next page exists without a separate COUNT query.
	rows, err := s.repo.GetProductHistory(ctx, userID, catalogueIDs, cursor, limit+1)
	if err != nil {
		return ProductHistoryPage{}, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	resolver, _, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return ProductHistoryPage{}, err
	}

	entries := make([]ProductHistoryEntry, 0, len(rows))
	for _, row := range rows {
		yearMonth := row.DateISO[:7]
		kcal := resolvePersonalKcalsForEntry(resolver, row.FoodCatalogueID, row.DateISO, row.FoodWeight)
		norm := resolver.AppliedNorm(yearMonth)
		var percent float64
		if norm > 0 {
			percent = roundFloat(float64(kcal)/norm*100, 0)
		}
		entries = append(entries, ProductHistoryEntry{
			DateISO:         row.DateISO,
			FoodCatalogueID: row.FoodCatalogueID,
			FoodWeight:      row.FoodWeight,
			PercentOfNorm:   percent,
		})
	}

	page := ProductHistoryPage{Entries: entries}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.NextCursor = &ProductHistoryCursor{DateISO: last.DateISO, ID: last.ID}
	}
	return page, nil
}

func (s *Service) GetDiaryFullUpdate(ctx context.Context, userID int64, dateISO string, offsetDays int) (map[string]DiaryDay, error) {
	dates := getDateRange(dateISO, offsetDays)
	startDate, endDate := getStartAndEndDates(dateISO, offsetDays)
	result := make(map[string]DiaryDay, len(dates))
	for _, date := range dates {
		result[date] = DiaryDay{Food: map[int64]DiaryEntry{}}
	}

	diaryRows, err := s.repo.GetDiaryRange(ctx, userID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	weights, err := s.repo.GetWeightRange(ctx, userID, startDate, endDate)
	if err != nil {
		return nil, err
	}
	stats, err := s.GetStats(ctx, userID)
	if err != nil {
		return nil, err
	}
	// The "goal" concept never got a UI or write path — always the same default.
	goal := "lose"
	catalogue, err := s.GetCatalogue(ctx)
	if err != nil {
		return nil, err
	}
	resolver, _, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return nil, err
	}

	catalogueMap := make(map[int64]CatalogueEntry, len(catalogue))
	for id, entry := range catalogue {
		catalogueMap[id] = entry
	}

	for _, row := range diaryRows {
		day, ok := result[row.DateISO]
		if !ok {
			continue
		}
		if day.Food == nil {
			day.Food = map[int64]DiaryEntry{}
		}
		day.Food[row.ID] = DiaryEntry{
			ID:              row.ID,
			DateISO:         row.DateISO,
			FoodCatalogueID: row.FoodCatalogueID,
			FoodWeight:      row.FoodWeight,
			Kcals:           resolvePersonalKcalsForEntry(resolver, row.FoodCatalogueID, row.DateISO, row.FoodWeight),
			History:         parseHistory(row.History),
			Version:         row.Version,
		}
		result[row.DateISO] = day
	}

	bodyWeightMap := make(map[string]float64, len(weights))
	for _, row := range weights {
		day, ok := result[row.DateISO]
		if !ok {
			continue
		}
		bodyWeightMap[row.DateISO] = row.Weight
		weight := row.Weight
		day.BodyWeight = &weight
		result[row.DateISO] = day
	}

	targetKcals := buildTargetKcalsForRange(dates, stats.Days)
	targetNutrients := calculateTargetNutrientsForRange(dates, bodyWeightMap, stats.Days, goal, targetKcals)
	consumedNutrients := calculateConsumedNutrientsForRange(dates, result, catalogueMap)
	consumedKcals := calculateConsumedKcalsForRange(dates, result)

	for _, date := range dates {
		day := result[date]
		target := targetNutrients[date]
		consumed := consumedNutrients[date]
		day.Nutrients = DayNutrients{
			TargetKcals:     targetKcals[date],
			ConsumedKcals:   consumedKcals[date],
			TargetProtein:   target.TargetProtein,
			TargetFat:       target.TargetFat,
			TargetCarbs:     target.TargetCarbs,
			TargetFiber:     target.TargetFiber,
			ConsumedProtein: consumed.Protein,
			ConsumedFat:     consumed.Fat,
			ConsumedCarbs:   consumed.Carbs,
			ConsumedFiber:   consumed.Fiber,
		}
		result[date] = day
	}

	return result, nil
}

func (s *Service) GetCatalogue(ctx context.Context) (map[int64]CatalogueEntry, error) {
	base, err := s.getCachedCatalogueBase(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[int64]CatalogueEntry, len(base))
	for id, entry := range base {
		entry.ImageVersion = s.imageVersion(id)
		result[id] = entry
	}
	return result, nil
}

// getCachedCatalogueBase returns every catalogue entry without ImageVersion resolved — the part
// that's expensive to (re)build (a full-table read) and changes only on an explicit product
// write, unlike ImageVersion which is a cheap in-memory lookup applied fresh in GetCatalogue.
func (s *Service) getCachedCatalogueBase(ctx context.Context) (map[int64]CatalogueEntry, error) {
	if cached, ok := s.catalogueCache.Get(); ok {
		return cached, nil
	}

	rows, err := s.repo.GetCatalogue(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[int64]CatalogueEntry, len(rows))
	for _, row := range rows {
		result[row.ID] = CatalogueEntry{
			ID:          row.ID,
			Name:        row.Name,
			LegacyName:  nullableStringPtr(row.LegacyName),
			Kcals:       row.Kcals,
			Protein:     nullableFloat64Value(row.Protein),
			Fat:         nullableFloat64Value(row.Fat),
			Carbs:       nullableFloat64Value(row.Carbs),
			Fiber:       nullableFloat64Value(row.Fiber),
			Description: nullableStringValue(row.Description),
			Archived:    row.Archived,
		}
	}
	s.catalogueCache.Set(result)
	return result, nil
}

// CatalogueVersion is a cheap "did the shared catalogue change" signal for reconnect catch-up —
// see GET /api/food/catalogue/version.
func (s *Service) CatalogueVersion() int64 {
	return s.catalogueCache.Version()
}

func (s *Service) GetCatalogueEntry(ctx context.Context, catalogueID int64) (*CatalogueEntry, error) {
	row, err := s.repo.GetCatalogueEntry(ctx, catalogueID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}

	count, err := s.repo.CountDiaryEntriesByCatalogueID(ctx, catalogueID)
	if err != nil {
		return nil, err
	}
	canDelete := count == 0

	return &CatalogueEntry{
		ID:           row.ID,
		Name:         row.Name,
		LegacyName:   nullableStringPtr(row.LegacyName),
		Kcals:        row.Kcals,
		Protein:      nullableFloat64Value(row.Protein),
		Fat:          nullableFloat64Value(row.Fat),
		Carbs:        nullableFloat64Value(row.Carbs),
		Fiber:        nullableFloat64Value(row.Fiber),
		Description:  nullableStringValue(row.Description),
		Archived:     row.Archived,
		ImageVersion: s.imageVersion(row.ID),
		CanDelete:    &canDelete,
	}, nil
}

func (s *Service) imageVersion(catalogueID int64) *int64 {
	if s.imageVersions == nil {
		return nil
	}
	return s.imageVersions.ImageVersion(catalogueID)
}

func (s *Service) CreateDiaryEntry(ctx context.Context, userID int64, operationID string, dateISO string, foodCatalogueID int64, foodWeight int64, history []HistoryEntry) (entry DiaryEntry, applied bool, err error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return DiaryEntry{}, false, err
	}

	cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID)
	if err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}
	if found {
		_ = tx.Rollback()
		var cached DiaryEntry
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return DiaryEntry{}, false, fmt.Errorf("decode cached diary create: %w", err)
		}
		kcals, err := s.resolvePersonalKcalsForCurrentMonth(ctx, userID, cached.FoodCatalogueID, cached.FoodWeight)
		if err != nil {
			return DiaryEntry{}, false, err
		}
		cached.Kcals = kcals
		return cached, false, nil
	}

	if err := s.requireCatalogueEntry(ctx, tx, foodCatalogueID, false); err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}

	historyJSON, err := toHistoryJSON(history)
	if err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}
	id, err := s.repo.CreateDiaryEntry(ctx, tx, userID, dateISO, foodCatalogueID, foodWeight, historyJSON)
	if err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}

	result := DiaryEntry{ID: id, DateISO: dateISO, FoodCatalogueID: foodCatalogueID, FoodWeight: foodWeight, History: history, Version: 1}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(resultJSON)); err != nil {
		_ = tx.Rollback()
		return DiaryEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return DiaryEntry{}, false, fmt.Errorf("commit create diary entry: %w", err)
	}

	kcals, err := s.resolvePersonalKcalsForCurrentMonth(ctx, userID, foodCatalogueID, foodWeight)
	if err != nil {
		return DiaryEntry{}, false, err
	}
	result.Kcals = kcals
	s.InvalidateStats(userID)
	return result, true, nil
}

// requireCatalogueEntry rejects a product that does not exist, or is archived unless
// allowArchived — a new diary entry may not use an archived product, while restoring a deleted
// day may bring back entries that already had one.
func (s *Service) requireCatalogueEntry(ctx context.Context, tx *sql.Tx, catalogueID int64, allowArchived bool) error {
	archived, found, err := s.repo.GetCatalogueArchived(ctx, tx, catalogueID)
	if err != nil {
		return err
	}
	if !found || (archived && !allowArchived) {
		return legacy.NewError(legacy.ErrorKindValidation, "Product is not available")
	}
	return nil
}

func (s *Service) EditDiaryEntry(ctx context.Context, userID int64, operationID string, diaryID int64, targetFoodWeight int64, requestedAction string) (entry *DiaryEntry, applied bool, err error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return nil, false, err
	}

	cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, err
	}

	var result DiaryEntry
	if found {
		_ = tx.Rollback()
		if err := json.Unmarshal([]byte(cachedJSON), &result); err != nil {
			return nil, false, fmt.Errorf("decode cached diary edit: %w", err)
		}
	} else {
		existing, err := s.repo.GetDiaryEntryForEdit(ctx, tx, diaryID, userID)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if existing == nil {
			_ = tx.Rollback()
			return nil, false, nil
		}

		delta := targetFoodWeight - existing.FoodWeight
		result = DiaryEntry{ID: diaryID, FoodCatalogueID: existing.FoodCatalogueID, FoodWeight: existing.FoodWeight, History: parseHistory(existing.History), Version: existing.Version}
		if delta != 0 {
			appliedEntry := buildHistoryEntry(requestedAction, targetFoodWeight, delta)
			result.FoodWeight = targetFoodWeight
			result.History = append(result.History, appliedEntry)
			result.Version = existing.Version + 1
			result.AppliedHistoryEntry = &appliedEntry

			updatedHistoryJSON, err := toHistoryJSON(result.History)
			if err != nil {
				_ = tx.Rollback()
				return nil, false, err
			}
			if err := s.repo.UpdateDiaryEntry(ctx, tx, diaryID, userID, result.FoodWeight, updatedHistoryJSON, result.Version); err != nil {
				_ = tx.Rollback()
				return nil, false, err
			}
		}

		resultJSON, err := json.Marshal(result)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := s.idempotency.Record(ctx, tx, userID, operationID, string(resultJSON)); err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit diary entry edit: %w", err)
		}
		applied = true
	}

	kcals, err := s.resolvePersonalKcalsForCurrentMonth(ctx, userID, result.FoodCatalogueID, result.FoodWeight)
	if err != nil {
		return nil, false, err
	}
	result.Kcals = kcals
	if applied && result.AppliedHistoryEntry != nil {
		s.InvalidateStats(userID)
	}
	return &result, applied, nil
}

func buildHistoryEntry(requestedAction string, targetFoodWeight int64, delta int64) HistoryEntry {
	if requestedAction == historyActionSet {
		return HistoryEntry{Action: historyActionSet, Value: targetFoodWeight}
	}
	if delta > 0 {
		return HistoryEntry{Action: historyActionAdd, Value: delta}
	}
	return HistoryEntry{Action: historyActionSubtract, Value: -delta}
}

func (s *Service) resolvePersonalKcalsForCurrentMonth(ctx context.Context, userID int64, foodCatalogueID int64, foodWeight int64) (int64, error) {
	resolver, _, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return 0, err
	}
	nowISO := s.clock.Now().UTC().Format("2006-01-02")
	return resolvePersonalKcalsForEntry(resolver, foodCatalogueID, nowISO, foodWeight), nil
}

func resolvePersonalKcalsForEntry(resolver *PersonalKcalResolver, foodCatalogueID int64, dateISO string, foodWeight int64) int64 {
	kcalPer100g := resolver.AppliedKcal(foodCatalogueID, dateISO[:7])
	return int64(math.Round(kcalPer100g * float64(foodWeight) / 100))
}

func (s *Service) DeleteDiaryEntry(ctx context.Context, userID int64, operationID string, diaryID int64) (deleted bool, applied bool, err error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return false, false, err
	}

	if cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID); err != nil {
		_ = tx.Rollback()
		return false, false, err
	} else if found {
		_ = tx.Rollback()
		var cached struct {
			Deleted bool `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return false, false, fmt.Errorf("decode cached diary delete: %w", err)
		}
		return cached.Deleted, false, nil
	}

	deleted, err = s.repo.DeleteDiaryEntry(ctx, tx, diaryID, userID)
	if err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	payload, err := json.Marshal(struct {
		Deleted bool `json:"deleted"`
	}{deleted})
	if err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(payload)); err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("commit diary entry delete: %w", err)
	}

	if deleted {
		s.InvalidateStats(userID)
	}
	return deleted, true, nil
}

func (s *Service) DeleteDiaryEntriesForDay(ctx context.Context, userID int64, operationID string, dateISO string) (deletedCount int64, applied bool, err error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return 0, false, err
	}

	if cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID); err != nil {
		_ = tx.Rollback()
		return 0, false, err
	} else if found {
		_ = tx.Rollback()
		var cached struct {
			DeletedCount int64 `json:"deletedCount"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return 0, false, fmt.Errorf("decode cached diary day delete: %w", err)
		}
		return cached.DeletedCount, false, nil
	}

	deletedCount, err = s.repo.DeleteDiaryEntriesByDate(ctx, tx, dateISO, userID)
	if err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}
	if deletedCount == 0 {
		_ = tx.Rollback()
		return 0, false, legacy.NewError(legacy.ErrorKindNotFound, "Entries not found")
	}
	payload, err := json.Marshal(struct {
		DeletedCount int64 `json:"deletedCount"`
	}{deletedCount})
	if err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(payload)); err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit diary day delete: %w", err)
	}

	s.InvalidateStats(userID)
	return deletedCount, true, nil
}

func (s *Service) RestoreDiaryEntriesForDay(ctx context.Context, userID int64, operationID string, dateISO string, entries []RestoreDiaryEntryInput) (restored []DiaryEntry, applied bool, err error) {
	if len(entries) == 0 {
		return nil, false, legacy.NewError(legacy.ErrorKindValidation, "Entries not found")
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return nil, false, err
	}

	cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, err
	}
	if found {
		_ = tx.Rollback()
		if err := json.Unmarshal([]byte(cachedJSON), &restored); err != nil {
			return nil, false, fmt.Errorf("decode cached diary day restore: %w", err)
		}
	} else {
		normalized := make([]DiaryEntry, 0, len(entries))
		for _, entry := range entries {
			if err := s.requireCatalogueEntry(ctx, tx, entry.FoodCatalogueID, true); err != nil {
				_ = tx.Rollback()
				return nil, false, err
			}
			history := entry.History
			if len(history) == 0 {
				history = []HistoryEntry{{Action: historyActionInit, Value: entry.FoodWeight}}
			}
			normalized = append(normalized, DiaryEntry{DateISO: dateISO, FoodCatalogueID: entry.FoodCatalogueID, FoodWeight: entry.FoodWeight, History: history, Version: 1})
		}
		restored, err = s.repo.CreateDiaryEntriesBatch(ctx, tx, userID, normalized)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		payload, err := json.Marshal(restored)
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := s.idempotency.Record(ctx, tx, userID, operationID, string(payload)); err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit diary day restore: %w", err)
		}
		applied = true
	}

	resolver, _, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	for i := range restored {
		restored[i].Kcals = resolvePersonalKcalsForEntry(resolver, restored[i].FoodCatalogueID, restored[i].DateISO, restored[i].FoodWeight)
	}
	if applied {
		s.InvalidateStats(userID)
	}
	return restored, applied, nil
}

func (s *Service) SetBodyWeight(ctx context.Context, userID int64, operationID string, dateISO string, bodyWeight float64) (okResult bool, applied bool, err error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return false, false, err
	}

	if cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID); err != nil {
		_ = tx.Rollback()
		return false, false, err
	} else if found {
		_ = tx.Rollback()
		var cached struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return false, false, fmt.Errorf("decode cached body weight: %w", err)
		}
		return cached.OK, false, nil
	}

	existing, err := s.repo.GetWeightByDate(ctx, tx, dateISO, userID)
	if err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	if existing == nil {
		if _, err := s.repo.CreateWeight(ctx, tx, dateISO, bodyWeight, userID); err != nil {
			_ = tx.Rollback()
			return false, false, err
		}
		okResult = true
	} else {
		okResult, err = s.repo.UpdateWeight(ctx, tx, dateISO, bodyWeight, userID)
		if err != nil {
			_ = tx.Rollback()
			return false, false, err
		}
	}
	if !okResult {
		_ = tx.Rollback()
		return false, false, nil
	}

	payload, err := json.Marshal(struct {
		OK bool `json:"ok"`
	}{okResult})
	if err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(payload)); err != nil {
		_ = tx.Rollback()
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("commit body weight: %w", err)
	}

	s.InvalidateStats(userID)
	return true, true, nil
}

func (s *Service) InvalidateStats(userID int64) {
	s.statsCache.Delete(userID)
}

// defaultStatsWindowDays bounds the day-level detail returned by the public stats endpoint to a
// recent, cheap-to-transfer window — the full-history computation behind it (GetStats) is
// untouched and still cached/used as-is by GetDiaryFullUpdate, which needs targetKcal/weightAvg
// for arbitrary historical dates. Trimming happens only at the HTTP response boundary (see
// TrimStatsToRecentWindow), so this constant affects network payload size only, never correctness.
const defaultStatsWindowDays = 90

// TrimStatsToRecentWindow keeps only the last defaultStatsWindowDays of response.Days (today
// inclusive) — used by the HTTP handler for the common case where the client hasn't asked for
// full history (see GetStatsQueryParamFull). TopProducts/TotalEntries/Summary are already
// independently windowed/aggregated over the full history and are left untouched — Summary in
// particular exists so all-time records and milestones stay correct in the windowed response too.
func (s *Service) TrimStatsToRecentWindow(response StatsResponse) StatsResponse {
	today := s.clock.Now().UTC().Format("2006-01-02")
	windowStart := createUTCDate(today).AddDate(0, 0, -(defaultStatsWindowDays - 1)).Format("2006-01-02")

	trimmedDays := make(map[string]DayStats, defaultStatsWindowDays)
	for date, stat := range response.Days {
		if date >= windowStart {
			trimmedDays[date] = stat
		}
	}
	response.Days = trimmedDays
	return response
}

func (s *Service) GetStats(ctx context.Context, userID int64) (StatsResponse, error) {
	if cached, ok := s.statsCache.Get(userID); ok {
		return cached, nil
	}
	firstDate, err := s.repo.GetUserFirstDate(ctx, userID)
	if err != nil {
		return StatsResponse{}, err
	}
	if firstDate == "" {
		return StatsResponse{Days: map[string]DayStats{}, TopProductsByKcal: []ProductStat{}, TopProductsByWeight: []ProductStat{}}, nil
	}

	lastDate := s.clock.Now().UTC().Format("2006-01-02")
	allDates := getDatesList(firstDate, lastDate)
	weightRows, err := s.repo.GetWeightRange(ctx, userID, firstDate, lastDate)
	if err != nil {
		return StatsResponse{}, err
	}
	diaryRows, err := s.repo.GetStatsDiaryHistory(ctx, userID, firstDate, lastDate)
	if err != nil {
		return StatsResponse{}, err
	}
	resolver, catalogueNameByID, err := s.buildPersonalKcalResolver(ctx, userID)
	if err != nil {
		return StatsResponse{}, err
	}

	weights := prepareWeights(weightRows, allDates)
	weightsAvg := calculateCenteredAverage(weights, 10, true, 1)
	diaryEntries := prepareDiaryEntries(diaryRows, allDates)

	// Top-products window ends the day before lastDate (today) — today's diary entries
	// are still incomplete, same reasoning as excluding today from the frontend streak/ribbon.
	topProductsWindowStart := createUTCDate(lastDate).AddDate(0, 0, -topProductsWindowDays).Format("2006-01-02")

	stats := make(map[string]DayStats, len(allDates))
	productKcal := make(map[int64]float64)
	productWeight := make(map[int64]float64)
	var windowTotalKcal, windowTotalWeight float64
	for _, date := range allDates {
		yearMonth := date[:7]
		inTopProductsWindow := date >= topProductsWindowStart && date < lastDate
		var consumed float64
		for _, row := range diaryEntries[date] {
			kcal := resolver.AppliedKcal(row.FoodCatalogueID, yearMonth) * row.FoodWeight / 100
			consumed += kcal
			if inTopProductsWindow {
				productKcal[row.FoodCatalogueID] += kcal
				productWeight[row.FoodCatalogueID] += row.FoodWeight
				windowTotalKcal += kcal
				windowTotalWeight += row.FoodWeight
			}
		}
		target := roundFloat(resolver.AppliedNorm(yearMonth), 0)
		stats[date] = DayStats{Weight: weights[date], WeightAvg: weightsAvg[date], ConsumedKcal: consumed, TargetKcal: target, HasNoData: diaryEntries[date] == nil}
	}

	products := buildProductStats(productKcal, productWeight, catalogueNameByID)
	response := StatsResponse{
		Days:                         stats,
		Summary:                      buildStatsSummary(allDates, stats),
		TopProductsByKcal:            rankProducts(products, func(p ProductStat) float64 { return p.Kcal }),
		TopProductsByWeight:          rankProducts(products, func(p ProductStat) float64 { return p.Weight }),
		TopProductsWindowTotalKcal:   windowTotalKcal,
		TopProductsWindowTotalWeight: windowTotalWeight,
		TotalEntries:                 len(diaryRows),
	}
	s.statsCache.Set(userID, response)
	return response, nil
}

// buildStatsSummary scans the full-history stats map once (in chronological order, via allDates)
// to pick out all-time records. Weight is backfilled/interpolated for every day once the user has
// at least one weight entry (see prepareWeights), so "day.Weight > 0" reliably means "known or
// interpolated weight" rather than "an entry exists on this exact day" — consumedKcal has no such
// interpolation, so the kcal-day filter below does mean an exact match.
func buildStatsSummary(allDates []string, stats map[string]DayStats) StatsSummary {
	summary := StatsSummary{DaysInDiary: len(allDates)}
	if len(allDates) == 0 {
		return summary
	}

	var firstWeightedDate string
	for _, date := range allDates {
		day := stats[date]
		if day.Weight > 0 {
			if firstWeightedDate == "" {
				firstWeightedDate = date
			}
			if summary.MinWeight == nil || day.Weight < summary.MinWeight.Weight {
				summary.MinWeight = &WeightRecord{Weight: day.Weight, DateISO: date}
			}
			if summary.MaxWeight == nil || day.Weight > summary.MaxWeight.Weight {
				summary.MaxWeight = &WeightRecord{Weight: day.Weight, DateISO: date}
			}
		}
		if day.ConsumedKcal > 0 && day.TargetKcal > 0 {
			percent := roundFloat(day.ConsumedKcal/day.TargetKcal*100, 0)
			if summary.MostCaloricDay == nil || percent > summary.MostCaloricDay.Percent {
				summary.MostCaloricDay = &CaloricDayRecord{Percent: percent, DateISO: date}
			}
			if summary.LeastCaloricDay == nil || percent < summary.LeastCaloricDay.Percent {
				summary.LeastCaloricDay = &CaloricDayRecord{Percent: percent, DateISO: date}
			}
		}
	}

	todayISO := allDates[len(allDates)-1]
	today := stats[todayISO]
	if today.Weight <= 0 {
		return summary
	}

	if firstWeightedDate != "" {
		delta := roundFloat(today.Weight-stats[firstWeightedDate].Weight, 1)
		summary.WeightChangeSinceStartKg = &delta
	}

	yearAgoISO := createUTCDate(todayISO).AddDate(-1, 0, 0).Format("2006-01-02")
	if match, ok := stats[yearAgoISO]; ok && match.Weight > 0 {
		summary.YearAgo = &YearAgoRecord{
			DateISO:    yearAgoISO,
			WeightThen: match.Weight,
			WeightNow:  today.Weight,
			DeltaKg:    roundFloat(today.Weight-match.Weight, 1),
		}
	}

	return summary
}

func buildProductStats(productKcal map[int64]float64, productWeight map[int64]float64, catalogueNameByID map[int64]string) []ProductStat {
	catalogueIDs := make(map[int64]struct{}, len(productKcal))
	for catalogueID := range productKcal {
		catalogueIDs[catalogueID] = struct{}{}
	}
	for catalogueID := range productWeight {
		catalogueIDs[catalogueID] = struct{}{}
	}

	products := make([]ProductStat, 0, len(catalogueIDs))
	for catalogueID := range catalogueIDs {
		products = append(products, ProductStat{
			CatalogueID: catalogueID,
			Name:        catalogueNameByID[catalogueID],
			Kcal:        productKcal[catalogueID],
			Weight:      productWeight[catalogueID],
		})
	}
	return products
}

// rankProducts sorts a copy of products descending by keyFn (ties broken by CatalogueID ascending,
// for a stable order) and limits it to topProductsCount — used once per metric (kcal, weight) so
// each ranking gets its own top-N slice without the two orders disturbing each other.
func rankProducts(products []ProductStat, keyFn func(ProductStat) float64) []ProductStat {
	sorted := make([]ProductStat, len(products))
	copy(sorted, products)
	sort.Slice(sorted, func(i, j int) bool {
		ki, kj := keyFn(sorted[i]), keyFn(sorted[j])
		if ki != kj {
			return ki > kj
		}
		return sorted[i].CatalogueID < sorted[j].CatalogueID
	})
	if len(sorted) > topProductsCount {
		sorted = sorted[:topProductsCount]
	}
	return sorted
}

type nutrientTotals struct {
	Protein int64
	Fat     int64
	Carbs   int64
	Fiber   int64
}

type nutrientTargets struct {
	TargetProtein int64
	TargetFat     int64
	TargetCarbs   int64
	TargetFiber   int64
}

func parseHistory(history string) []HistoryEntry {
	var result []HistoryEntry
	if err := json.Unmarshal([]byte(history), &result); err != nil {
		return []HistoryEntry{}
	}
	if result == nil {
		return []HistoryEntry{}
	}
	return result
}

func toHistoryJSON(history []HistoryEntry) (string, error) {
	payload, err := json.Marshal(history)
	if err != nil {
		return "", fmt.Errorf("marshal history: %w", err)
	}
	return string(payload), nil
}

func getDateRange(dateISO string, offsetDays int) []string {
	center := createUTCDate(dateISO)
	result := make([]string, 0, offsetDays*2+1)
	for i := -offsetDays; i <= offsetDays; i++ {
		day := center.AddDate(0, 0, i)
		result = append(result, day.Format("2006-01-02"))
	}
	return result
}

func getStartAndEndDates(dateISO string, offsetDays int) (string, string) {
	date := createUTCDate(dateISO)
	start := date.AddDate(0, 0, -offsetDays)
	end := date.AddDate(0, 0, offsetDays+1)
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}

func buildTargetKcalsForRange(dates []string, stats map[string]DayStats) map[string]*int64 {
	result := make(map[string]*int64, len(dates))
	var lastKnown *int64
	for _, date := range dates {
		if stat, ok := stats[date]; ok {
			value := int64(stat.TargetKcal)
			lastKnown = &value
			result[date] = &value
			continue
		}
		result[date] = lastKnown
	}
	return result
}

func calculateTargetNutrientsForRange(dates []string, bodyWeights map[string]float64, stats map[string]DayStats, goal string, targetKcals map[string]*int64) map[string]nutrientTargets {
	result := make(map[string]nutrientTargets, len(dates))
	for _, date := range dates {
		weight := 75.0
		if stat, ok := stats[date]; ok && stat.WeightAvg != 0 {
			weight = stat.WeightAvg
		} else if value, ok := bodyWeights[date]; ok && value != 0 {
			weight = value
		}

		dayTargetKcals := int64(2000)
		if value := targetKcals[date]; value != nil {
			dayTargetKcals = *value
		}

		result[date] = calculateTargetNutrients(weight, goal, dayTargetKcals)
	}
	return result
}

func calculateConsumedNutrientsForRange(dates []string, diary map[string]DiaryDay, catalogue map[int64]CatalogueEntry) map[string]nutrientTotals {
	result := make(map[string]nutrientTotals, len(dates))
	for _, date := range dates {
		result[date] = calculateDailyNutrients(diary[date].Food, catalogue)
	}
	return result
}

func calculateConsumedKcalsForRange(dates []string, diary map[string]DiaryDay) map[string]int64 {
	result := make(map[string]int64, len(dates))
	for _, date := range dates {
		var total int64
		for _, entry := range diary[date].Food {
			total += entry.Kcals
		}
		result[date] = total
	}
	return result
}

func calculateTargetNutrients(weight float64, goal string, targetKcals int64) nutrientTargets {
	coefficient := 1.4
	switch goal {
	case "gain":
		coefficient = 2.0
	case "lose":
		coefficient = 1.8
	}

	targetProtein := weight * coefficient
	targetFat := (float64(targetKcals) * 0.25) / 9
	kcalsForCarbs := float64(targetKcals) - targetProtein*4 - targetFat*9
	targetCarbs := kcalsForCarbs / 4

	return nutrientTargets{
		TargetProtein: int64(math.Round(targetProtein)),
		TargetFat:     int64(math.Round(targetFat)),
		TargetCarbs:   int64(math.Round(targetCarbs)),
		TargetFiber:   30,
	}
}

func calculateDailyNutrients(entries map[int64]DiaryEntry, catalogue map[int64]CatalogueEntry) nutrientTotals {
	var protein float64
	var fat float64
	var carbs float64
	var fiber float64

	for _, entry := range entries {
		catalogueEntry, ok := catalogue[entry.FoodCatalogueID]
		if !ok {
			continue
		}
		portionMultiplier := float64(entry.FoodWeight) / 100
		protein += catalogueEntry.Protein * portionMultiplier
		fat += catalogueEntry.Fat * portionMultiplier
		carbs += catalogueEntry.Carbs * portionMultiplier
		fiber += catalogueEntry.Fiber * portionMultiplier
	}

	return nutrientTotals{Protein: int64(math.Round(protein)), Fat: int64(math.Round(fat)), Carbs: int64(math.Round(carbs)), Fiber: int64(math.Round(fiber))}
}

func createUTCDate(dateISO string) time.Time {
	parsed, _ := time.Parse("2006-01-02", dateISO)
	return parsed.UTC()
}

func getDatesList(firstDate string, lastDate string) []string {
	start := createUTCDate(firstDate)
	end := createUTCDate(lastDate)
	result := make([]string, 0)
	for current := start; !current.After(end); current = current.AddDate(0, 0, 1) {
		result = append(result, current.Format("2006-01-02"))
	}
	return result
}

func prepareWeights(rows []WeightRow, allDates []string) map[string]float64 {
	weights := make(map[string]float64, len(allDates))
	known := make(map[string]*float64, len(allDates))
	for _, date := range allDates {
		known[date] = nil
	}
	for _, row := range rows {
		value := row.Weight
		known[row.DateISO] = &value
	}

	points := make([]struct {
		Index int
		Value float64
	}, 0)
	for idx, date := range allDates {
		if known[date] != nil {
			points = append(points, struct {
				Index int
				Value float64
			}{Index: idx, Value: *known[date]})
		}
	}
	if len(points) == 0 {
		for _, date := range allDates {
			weights[date] = 0
		}
		return weights
	}

	for idx := 0; idx < points[0].Index; idx++ {
		weights[allDates[idx]] = points[0].Value
	}
	for pointIdx := 0; pointIdx < len(points)-1; pointIdx++ {
		left := points[pointIdx]
		right := points[pointIdx+1]
		weights[allDates[left.Index]] = left.Value
		gap := right.Index - left.Index
		for idx := left.Index + 1; idx < right.Index; idx++ {
			progress := float64(idx-left.Index) / float64(gap)
			weights[allDates[idx]] = left.Value + (right.Value-left.Value)*progress
		}
	}
	last := points[len(points)-1]
	weights[allDates[last.Index]] = last.Value
	for idx := last.Index + 1; idx < len(allDates); idx++ {
		weights[allDates[idx]] = last.Value
	}

	return weights
}

func prepareDiaryEntries(rows []StatsDiaryRow, allDates []string) map[string][]StatsDiaryRow {
	result := make(map[string][]StatsDiaryRow, len(allDates))
	for _, date := range allDates {
		result[date] = nil
	}
	for _, row := range rows {
		if result[row.DateISO] == nil {
			result[row.DateISO] = []StatsDiaryRow{}
		}
		result[row.DateISO] = append(result[row.DateISO], row)
	}
	return result
}

func calculateCenteredAverage(input map[string]float64, avgRange int, round bool, roundPlaces int) map[string]float64 {
	keys := sortedKeys(input)
	values := make([]float64, 0, len(keys))
	for _, key := range keys {
		values = append(values, input[key])
	}
	for idx := 1; idx < len(values); idx++ {
		if math.IsNaN(values[idx]) {
			values[idx] = values[idx-1]
		}
	}
	if len(values) > 0 && math.IsNaN(values[0]) {
		values[0] = 0
	}
	result := make(map[string]float64, len(keys))
	halfRange := avgRange / 2
	for idx := range values {
		start := max(0, idx-halfRange)
		end := min(len(values), idx+halfRange+1)
		avg := average(values[start:end])
		if round {
			avg = roundFloat(avg, roundPlaces)
		}
		result[keys[idx]] = avg
	}
	return result
}

func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableFloat64Value(value sql.NullFloat64) float64 {
	if !value.Valid {
		return 0
	}
	return value.Float64
}

func sortedKeys(input map[string]float64) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return sum(values) / float64(len(values))
}

func sum(values []float64) float64 {
	var result float64
	for _, value := range values {
		result += value
	}
	return result
}

func roundFloat(value float64, places int) float64 {
	multiplier := math.Pow(10, float64(places))
	return math.Round(value*multiplier) / multiplier
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
