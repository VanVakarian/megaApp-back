package food

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
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

type scoredCatalogueEntry struct {
	entry CatalogueEntry
	score int
}

func (s *Service) SearchCatalogue(ctx context.Context, query string) ([]CatalogueEntry, error) {
	ids, err := s.searchCatalogueIDs(ctx, query, false)
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

// SearchCatalogueRealtime serves the WS search. The archive mode is applied only for an admin;
// for anyone else the request silently falls back to the normal mode. The returned flag is the
// mode actually applied, so the client never mistakes one mode's results for the other's.
func (s *Service) SearchCatalogueRealtime(ctx context.Context, userID int64, query string, archived bool) ([]int64, bool, error) {
	if archived {
		isAdmin, err := s.isAdmin(ctx, userID)
		if err != nil {
			return nil, false, err
		}
		archived = isAdmin
	}
	ids, err := s.searchCatalogueIDs(ctx, query, archived)
	if err != nil {
		return nil, false, err
	}
	return ids, archived, nil
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
		s.searchCache.Clear()
		s.catalogueCache.Invalidate()
		applied = true
	}

	entry, err = s.GetCatalogueEntry(ctx, resultID)
	if err != nil {
		return nil, false, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load saved product", err)
	}
	if applied && entry != nil && catalogueID == nil && s.imageGenerationRequest != nil {
		s.imageGenerationRequest.RequestProductImageGeneration(entry.ID, entry.Name, entry.Description)
	}
	return entry, applied, nil
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
		s.searchCache.Clear()
		s.catalogueCache.Invalidate()
	}
	return deleted, true, nil
}

// searchCatalogueIDs ranks the products of one mode: normal (archived == false) skips archived
// products, archive mode returns only them. The results cache serves the normal mode only —
// archive searches are rare, so they neither read nor write it.
func (s *Service) searchCatalogueIDs(ctx context.Context, query string, archived bool) ([]int64, error) {
	normalizedQuery := normalizeSearchText(query)
	if normalizedQuery == "" {
		return []int64{}, nil
	}
	if !archived {
		if cached, ok := s.searchCache.Get(normalizedQuery); ok {
			return cached, nil
		}
	}

	catalogue, err := s.GetCatalogue(ctx)
	if err != nil {
		return nil, err
	}

	semanticScores, err := s.semanticScoresByID(ctx, normalizedQuery)
	if err != nil {
		return nil, err
	}

	normalizedTokens := splitSearchTokens(normalizedQuery)
	transliteratedQuery := transliterateEnToRu(normalizedQuery)
	transliteratedTokens := splitSearchTokens(transliteratedQuery)
	scored := make([]scoredCatalogueEntry, 0, len(catalogue))
	for _, entry := range catalogue {
		if entry.Archived != archived {
			continue
		}
		score := scoreCatalogueEntry(entry, normalizedQuery, normalizedTokens, transliteratedQuery, transliteratedTokens)
		score += semanticScores[entry.ID]
		if score == 0 {
			continue
		}
		scored = append(scored, scoredCatalogueEntry{entry: entry, score: score})
	}

	sort.Slice(scored, func(i int, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].entry.Name < scored[j].entry.Name
		}
		return scored[i].score > scored[j].score
	})

	limit := 30
	if len(scored) > limit {
		scored = scored[:limit]
	}
	ids := make([]int64, 0, len(scored))
	for _, item := range scored {
		ids = append(ids, item.entry.ID)
	}

	if !archived {
		s.searchCache.Set(normalizedQuery, ids)
	}
	return ids, nil
}

// semanticScoreWeight converts a semantic (embedding) similarity into the same points
// scale scoreField uses, so it can be added directly to the text/transliteration score
// instead of gating it — a close semantic match (similarity near 1) is worth roughly
// as much as a prefix match, a distant one contributes close to nothing.
const semanticScoreWeight = 500

// semanticScoresByID returns a similarity score per catalogue ID for the query's embedding.
// A nil map (no error) means no embedding is available for this query — callers should
// treat every entry as scoring 0 from this signal and rely on text/transliteration alone.
func (s *Service) semanticScoresByID(ctx context.Context, query string) (map[int64]int, error) {
	embeddingBlob, err := s.repo.GetQueryEmbedding(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(embeddingBlob) == 0 {
		if s.embeddingGenerator == nil {
			return nil, nil
		}
		embedding, err := s.embeddingGenerator.GenerateEmbedding(ctx, query)
		if err != nil {
			return nil, err
		}
		embeddingBlob = encodeFloat32Blob(embedding)
		if err := s.repo.SaveQueryEmbedding(ctx, query, embeddingBlob); err != nil {
			return nil, err
		}
	} else {
		// Best-effort popularity counter — losing an increment on failure isn't worth failing
		// the search itself over.
		_ = s.repo.RecordQueryEmbeddingHit(ctx, query)
	}
	queryVector := decodeFloat32Blob(embeddingBlob)
	if len(queryVector) == 0 {
		return nil, nil
	}

	rows, err := s.repo.GetSearchVectors(ctx)
	if err != nil {
		return nil, err
	}

	scores := make(map[int64]int, len(rows))
	for _, row := range rows {
		nameVector := decodeFloat32Blob(row.NameVector)
		descriptionVector := decodeFloat32Blob(row.DescriptionVec)
		distance, ok := combinedDistance(queryVector, nameVector, descriptionVector)
		if !ok {
			continue
		}
		if similarity := 1 - distance; similarity > 0 {
			scores[row.ID] = int(similarity * semanticScoreWeight)
		}
	}
	return scores, nil
}

func scoreCatalogueEntry(entry CatalogueEntry, query string, tokens []string, transliteratedQuery string, transliteratedTokens []string) int {
	name := normalizeSearchText(entry.Name)
	legacy := normalizeSearchText(nullableStringOrEmpty(entry.LegacyName))
	description := normalizeSearchText(entry.Description)

	score := 0
	score += scoreField(name, query, tokens, 1000, 500, 80, 140)
	score += scoreField(legacy, query, transliteratedTokens, 850, 420, 70, 120)
	score += scoreField(description, query, tokens, 120, 80, 20, 40)
	if transliteratedQuery != query {
		score += scoreField(name, transliteratedQuery, transliteratedTokens, 700, 300, 60, 100)
		score += scoreField(legacy, query, tokens, 700, 300, 60, 100)
	}
	return score
}

func scoreField(field string, query string, tokens []string, exact int, prefix int, tokenScore int, allTokens int) int {
	if field == "" || query == "" {
		return 0
	}
	if field == query {
		return exact
	}
	score := 0
	if strings.HasPrefix(field, query) {
		score += prefix
	}
	matched := 0
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(field, token) {
			score += tokenScore
			matched++
		}
	}
	if matched > 0 && matched == len(tokens) {
		score += allTokens
	}
	return score
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

func splitSearchTokens(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Fields(value)
}

func nullableStringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var enToRuTransliterationRules = map[rune]rune{
	'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н', 'u': 'г', 'i': 'ш', 'o': 'щ', 'p': 'з', '[': 'х', ']': 'ъ',
	'a': 'ф', 's': 'ы', 'd': 'в', 'f': 'а', 'g': 'п', 'h': 'р', 'j': 'о', 'k': 'л', 'l': 'д', ';': 'ж', '\'': 'э',
	'z': 'я', 'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и', 'n': 'т', 'm': 'ь', ',': 'б', '.': 'ю',
}

func transliterateEnToRu(text string) string {
	runes := []rune(strings.ToLower(text))
	for idx, r := range runes {
		if mapped, ok := enToRuTransliterationRules[r]; ok {
			runes[idx] = mapped
		}
	}
	return string(runes)
}

func decodeFloat32Blob(blob []byte) []float64 {
	if len(blob) == 0 || len(blob)%4 != 0 {
		return nil
	}
	result := make([]float64, 0, len(blob)/4)
	for idx := 0; idx < len(blob); idx += 4 {
		bits := binary.LittleEndian.Uint32(blob[idx : idx+4])
		result = append(result, float64(math.Float32frombits(bits)))
	}
	return result
}

func combinedDistance(query []float64, nameVector []float64, descriptionVector []float64) (float64, bool) {
	hasName := len(nameVector) == len(query) && len(nameVector) > 0
	hasDescription := len(descriptionVector) == len(query) && len(descriptionVector) > 0
	if !hasName && !hasDescription {
		return 0, false
	}
	if hasName && hasDescription {
		return cosineDistance(query, nameVector)*0.5 + cosineDistance(query, descriptionVector)*0.5, true
	}
	if hasName {
		return cosineDistance(query, nameVector), true
	}
	return cosineDistance(query, descriptionVector), true
}

func cosineDistance(a []float64, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 1
	}
	dot := 0.0
	normA := 0.0
	normB := 0.0
	for idx := range a {
		dot += a[idx] * b[idx]
		normA += a[idx] * a[idx]
		normB += b[idx] * b[idx]
	}
	normProduct := math.Sqrt(normA) * math.Sqrt(normB)
	if normProduct == 0 {
		return 1
	}
	return 1 - dot/normProduct
}
