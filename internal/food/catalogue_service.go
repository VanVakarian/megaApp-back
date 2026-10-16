package food

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"megaapp-back/internal/httpx/legacy"
)

type ProductPreviewData struct {
	GeneralizedName string  `json:"generalizedName"`
	Kcals           int64   `json:"kcals"`
	Protein         float64 `json:"protein"`
	Fat             float64 `json:"fat"`
	Carbs           float64 `json:"carbs"`
	Fiber           float64 `json:"fiber"`
	Description     string  `json:"description"`
	Confidence      float64 `json:"confidence"`
}

func (s *Service) SearchCatalogue(ctx context.Context, query string) ([]CatalogueEntry, error) {
	ids, err := s.searchCatalogueIDs(ctx, query, SearchScopeActive)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []CatalogueEntry{}, nil
	}

	catalogue, err := s.GetCatalogue(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]CatalogueEntry, 0, len(ids))
	for _, id := range ids {
		entry, ok := catalogue[id]
		if !ok {
			continue
		}
		result = append(result, entry)
	}

	return result, nil
}

// SearchCatalogueRealtime serves the WS search. The archived scope is applied only for an admin, and
// an unknown scope is the active one: anything else silently falls back to the active scope. The scope
// "all" is open to everybody, since the whole catalogue, archived products included, is already on every
// client. The returned scope is the one actually applied, so the client never mistakes the results of
// one scope for another's.
func (s *Service) SearchCatalogueRealtime(ctx context.Context, userID int64, query string, scope SearchScope) ([]int64, SearchScope, error) {
	switch scope {
	case SearchScopeAll:
	case SearchScopeArchived:
		isAdmin, err := s.isAdmin(ctx, userID)
		if err != nil {
			return nil, "", err
		}
		if !isAdmin {
			scope = SearchScopeActive
		}
	default:
		scope = SearchScopeActive
	}
	ids, err := s.searchCatalogueIDs(ctx, query, scope)
	if err != nil {
		return nil, "", err
	}
	return ids, scope, nil
}

func (s *Service) isAdmin(ctx context.Context, userID int64) (bool, error) {
	if s.adminChecker == nil {
		return false, nil
	}
	return s.adminChecker.IsAdmin(ctx, userID)
}

func (s *Service) GenerateProductPreview(ctx context.Context, query string) (ProductPreviewData, error) {
	normalizedQuery := normalizeSearchText(query)
	if normalizedQuery == "" {
		return ProductPreviewData{}, legacy.NewError(legacy.ErrorKindValidation, "Description is required")
	}
	if s.productGenerator == nil {
		return ProductPreviewData{}, legacy.NewError(legacy.ErrorKindInternal, "Product generator is not configured")
	}
	preview, err := s.productGenerator.GenerateProduct(ctx, normalizedQuery)
	if err != nil {
		return ProductPreviewData{}, legacy.WrapError(legacy.ErrorKindExternal, "Failed to generate product preview", err)
	}
	return preview, nil
}

// peekIdempotency reports whether operationID has already been applied, without holding a
// transaction open across it. Checked first and unconditionally in SaveProduct/DeleteProduct —
// on a replay, nothing else (name lookup, embeddings generation, usage check) should run at all.
// Mirrors internal/money/service.go's helper of the same name and reasoning.
func (s *Service) peekIdempotency(ctx context.Context, userID int64, operationID string) (string, bool, error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return "", false, err
	}
	cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID)
	_ = tx.Rollback()
	return cachedJSON, found, err
}

func (s *Service) SaveProduct(ctx context.Context, userID int64, operationID string, catalogueID *int64, input ProductInput) (entry *CatalogueEntry, applied bool, err error) {
	// Only an admin may touch the archive flag. Checked before the idempotency peek so that a
	// replayed operation cannot bypass it, and for any value of the flag (even an unchanged one).
	if input.Archived != nil {
		isAdmin, err := s.isAdmin(ctx, userID)
		if err != nil {
			return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to check permissions", err)
		}
		if !isAdmin {
			return nil, false, legacy.NewError(legacy.ErrorKindForbidden, "Forbidden")
		}
	}

	cachedJSON, found, err := s.peekIdempotency(ctx, userID, operationID)
	if err != nil {
		return nil, false, err
	}
	var resultID int64
	if found {
		var cached struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return nil, false, fmt.Errorf("decode cached catalogue save: %w", err)
		}
		resultID = cached.ID
	} else {
		input = normalizeProductInput(input)
		if err := validateProductInput(input); err != nil {
			return nil, false, legacy.WrapError(legacy.ErrorKindValidation, err.Error(), err)
		}

		existingByName, err := s.repo.GetCatalogueEntryByName(ctx, input.Name)
		if err != nil {
			return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to look up product by name", err)
		}

		// All reads happen before the write transaction opens, so it only holds the writes.
		if catalogueID == nil {
			if existingByName != nil {
				return nil, false, legacy.NewError(legacy.ErrorKindValidation, "product with this name already exists")
			}
		} else {
			existingByID, err := s.repo.GetCatalogueEntry(ctx, *catalogueID)
			if err != nil {
				return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load product", err)
			}
			if existingByID == nil {
				return nil, false, legacy.NewError(legacy.ErrorKindNotFound, "product not found")
			}
			if existingByName != nil && existingByName.ID != *catalogueID {
				return nil, false, legacy.NewError(legacy.ErrorKindValidation, "product with this name already exists")
			}
		}

		nameVector, descriptionVector, err := s.generateProductEmbeddings(ctx, input)
		if err != nil {
			return nil, false, legacy.WrapError(legacy.ErrorKindExternal, "Failed to generate product embeddings", err)
		}
		input.NameVector = nameVector
		input.DescriptionVec = descriptionVector

		tx, err := s.idempotency.BeginTx(ctx)
		if err != nil {
			return nil, false, err
		}

		if catalogueID == nil {
			resultID, err = s.repo.CreateCatalogueEntry(ctx, tx, input)
			if err != nil {
				_ = tx.Rollback()
				return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to create product", err)
			}
		} else {
			updated, err := s.repo.UpdateCatalogueEntry(ctx, tx, *catalogueID, input)
			if err != nil {
				_ = tx.Rollback()
				return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to update product", err)
			}
			if !updated {
				_ = tx.Rollback()
				return nil, false, legacy.NewError(legacy.ErrorKindNotFound, "product not found")
			}
			resultID = *catalogueID
		}

		payload, err := json.Marshal(struct {
			ID int64 `json:"id"`
		}{resultID})
		if err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := s.idempotency.Record(ctx, tx, userID, operationID, string(payload)); err != nil {
			_ = tx.Rollback()
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit catalogue save: %w", err)
		}
		s.catalogueChanged()
		applied = true
	}

	entry, err = s.GetCatalogueEntry(ctx, resultID)
	if err != nil {
		return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load saved product", err)
	}
	if applied && entry != nil && catalogueID == nil && s.imageGenerationRequest != nil {
		s.imageGenerationRequest.RequestProductImageGeneration(entry.ID, entry.Name, entry.Description, false)
	}
	return entry, applied, nil
}

// RegenerateProductImage queues a new image for the product, replacing the current one once the
// new one is ready. Admin only: generation costs money. The permission check goes first, ahead of
// any lookup, as for the archive flag. Nothing is refused silently: every outcome that does not
// queue the work is an error.
func (s *Service) RegenerateProductImage(ctx context.Context, userID int64, catalogueID int64) (ImageRequestOutcome, error) {
	isAdmin, err := s.isAdmin(ctx, userID)
	if err != nil {
		return "", legacy.WrapError(legacy.ErrorKindInternal, "Failed to check permissions", err)
	}
	if !isAdmin {
		return "", legacy.NewError(legacy.ErrorKindForbidden, "Forbidden")
	}

	entry, err := s.repo.GetCatalogueEntry(ctx, catalogueID)
	if err != nil {
		return "", legacy.WrapError(legacy.ErrorKindInternal, "Failed to load product", err)
	}
	if entry == nil {
		return "", legacy.NewError(legacy.ErrorKindNotFound, "product not found")
	}

	if s.imageGenerationRequest == nil {
		return "", legacy.NewError(legacy.ErrorKindConflict, "Image generation is disabled")
	}
	outcome := s.imageGenerationRequest.RequestProductImageGeneration(entry.ID, entry.Name, nullableStringValue(entry.Description), true)
	switch outcome {
	case ImageRequestQueued, ImageRequestAlreadyQueued:
		return outcome, nil
	case ImageRequestQueueFull:
		return "", legacy.NewError(legacy.ErrorKindTooManyRequests, "Image generation queue is full")
	default:
		return "", legacy.NewError(legacy.ErrorKindConflict, "Image generation is disabled")
	}
}

// BumpCatalogueVersion tells reconnecting clients the catalogue changed without dropping the
// cached entries or the search index: used when an image becomes ready, which neither holds.
func (s *Service) BumpCatalogueVersion() {
	s.catalogueCache.BumpVersion()
}

// LogProductsWithoutImage writes one warning listing the non-archived products that have no
// image, so a silently failed generation of the past does not stay unnoticed. It never generates.
func (s *Service) LogProductsWithoutImage(ctx context.Context, logger *slog.Logger) {
	catalogue, err := s.GetCatalogue(ctx)
	if err != nil {
		logger.Warn("products without image check failed", "error", err)
		return
	}
	ids := make([]int64, 0)
	for id, entry := range catalogue {
		if entry.ImageVersion == nil && !entry.Archived {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	logger.Warn("products without image", "count", len(ids), "catalogueIds", ids)
}

func (s *Service) DeleteProduct(ctx context.Context, userID int64, operationID string, catalogueID int64) (deleted bool, applied bool, err error) {
	cachedJSON, found, err := s.peekIdempotency(ctx, userID, operationID)
	if err != nil {
		return false, false, err
	}
	if found {
		var cached struct {
			Deleted bool `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return false, false, fmt.Errorf("decode cached catalogue delete: %w", err)
		}
		return cached.Deleted, false, nil
	}

	entry, err := s.repo.GetCatalogueEntry(ctx, catalogueID)
	if err != nil {
		return false, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load product", err)
	}
	if entry == nil {
		return false, false, legacy.NewError(legacy.ErrorKindNotFound, "product not found")
	}

	count, err := s.repo.CountDiaryEntriesByCatalogueID(ctx, catalogueID)
	if err != nil {
		return false, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to check product usage", err)
	}
	if count > 0 {
		return false, false, legacy.NewError(legacy.ErrorKindConflict, "product is used in diary entries")
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return false, false, err
	}

	deleted, err = s.repo.DeleteCatalogueEntry(ctx, tx, catalogueID)
	if err != nil {
		_ = tx.Rollback()
		return false, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to delete product", err)
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
		return false, false, fmt.Errorf("commit catalogue delete: %w", err)
	}

	if deleted {
		s.catalogueChanged()
	}
	return deleted, true, nil
}

// catalogueChanged is the one place every catalogue write reports to: it drops the cached catalogue
// and the search index, so the next read of either starts from the database.
func (s *Service) catalogueChanged() {
	s.catalogueCache.Invalidate()
	s.searchIndex.Invalidate()
}

// searchCatalogueIDs ranks the products of one scope. The results cache serves the active scope
// only — searches of the other scopes are rare, so they neither read nor write it.
func (s *Service) searchCatalogueIDs(ctx context.Context, query string, scope SearchScope) ([]int64, error) {
	normalizedQuery := normalizeSearchText(query)
	if normalizedQuery == "" {
		return []int64{}, nil
	}

	index, err := s.searchIndex.Get(ctx, s.buildSearchIndex)
	if err != nil {
		return nil, err
	}
	cacheable := scope == SearchScopeActive
	if cacheable {
		if cached, ok := index.results.Get(normalizedQuery); ok {
			return cached, nil
		}
	}

	// A vector that cannot be obtained (OpenAI down) is not an error for the user: the products whose
	// name starts with a query word are still found. A result built without the vector is not cached,
	// so the next search of the query tries the vector again. A database failure is an error.
	queryVector, err := s.queryVector(ctx, normalizedQuery)
	switch {
	case errors.Is(err, errQueryVectorUnavailable):
		slog.WarnContext(ctx, "food search: query vector unavailable, ranking by name only", "query", normalizedQuery, "error", err)
	case err != nil:
		return nil, err
	}

	ids := index.search(searchWords(normalizedQuery), queryVector, scope, searchWordLift)
	if cacheable && queryVector != nil {
		index.results.Set(normalizedQuery, ids)
	}
	return ids, nil
}

// errQueryVectorUnavailable marks a query vector that could not be produced by the embedding generator.
var errQueryVectorUnavailable = errors.New("query vector unavailable")

func (s *Service) buildSearchIndex(ctx context.Context) (*searchIndex, error) {
	rows, err := s.repo.GetSearchIndexRows(ctx)
	if err != nil {
		return nil, err
	}
	return newSearchIndex(rows), nil
}

// queryVector returns the unit embedding of the query: from the cache table, or from the embedding
// generator when the query is new or its cached vector is unusable (malformed or all zeros — the
// fresh vector then replaces the bad row). It is nil, without an error, only when no generator is
// configured; a generator failure or an unusable generated vector is errQueryVectorUnavailable; a
// failed database read is returned as it is.
func (s *Service) queryVector(ctx context.Context, query string) ([]float32, error) {
	embeddingBlob, err := s.repo.GetQueryEmbedding(ctx, query)
	if err != nil {
		return nil, err
	}
	if vector := decodeUnitVector(embeddingBlob); vector != nil {
		// Best-effort popularity counter — losing an increment on failure isn't worth failing
		// the search itself over.
		_ = s.repo.RecordQueryEmbeddingHit(ctx, query)
		return vector, nil
	}

	if s.embeddingGenerator == nil {
		return nil, nil
	}
	embedding, err := s.embeddingGenerator.GenerateEmbedding(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errQueryVectorUnavailable, err)
	}
	embeddingBlob = encodeFloat32Blob(embedding)
	vector := decodeUnitVector(embeddingBlob)
	if vector == nil {
		return nil, fmt.Errorf("%w: the embedding generator returned an empty or zero vector", errQueryVectorUnavailable)
	}
	// The vector is already in hand, so a failed cache write only costs a repeated embedding later.
	if err := s.repo.SaveQueryEmbedding(ctx, query, embeddingBlob); err != nil {
		slog.WarnContext(ctx, "food search: query vector was not cached", "query", query, "error", err)
	}
	return vector, nil
}

func (s *Service) generateProductEmbeddings(ctx context.Context, input ProductInput) ([]byte, []byte, error) {
	if s.embeddingGenerator == nil {
		return nil, nil, nil
	}
	nameEmbedding, err := s.embeddingGenerator.GenerateEmbedding(ctx, input.Name)
	if err != nil {
		return nil, nil, err
	}
	descriptionEmbedding, err := s.embeddingGenerator.GenerateEmbedding(ctx, input.Description)
	if err != nil {
		return nil, nil, err
	}
	return encodeFloat32Blob(nameEmbedding), encodeFloat32Blob(descriptionEmbedding), nil
}

func normalizeProductInput(input ProductInput) ProductInput {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	return input
}

func validateProductInput(input ProductInput) error {
	if input.Name == "" || input.Description == "" {
		return fmt.Errorf("all fields are required")
	}
	if input.Kcals < 0 || input.Kcals > 1000 {
		return fmt.Errorf("calories must be between 0 and 1000")
	}
	if input.Protein < 0 || input.Protein > 100 || input.Fat < 0 || input.Fat > 100 || input.Carbs < 0 || input.Carbs > 100 {
		return fmt.Errorf("protein, fat, and carbs must be between 0 and 100")
	}
	if input.Fiber < 0 || input.Fiber > 50 {
		return fmt.Errorf("fiber must be between 0 and 50")
	}
	if utfLen(input.Name) > 100 {
		return fmt.Errorf("name must be between 1 and 100 characters")
	}
	if utfLen(input.Description) > 2000 {
		return fmt.Errorf("description must be between 1 and 2000 characters")
	}
	return nil
}

func utfLen(value string) int {
	return len([]rune(value))
}

func normalizeSearchText(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	return strings.Join(fields, " ")
}
