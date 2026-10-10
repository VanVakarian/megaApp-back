package food

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"megaapp-back/internal/httpx/legacy"
)

type LabService struct {
	repo     *Repository
	food     *Service
	pipeline *ImagePipeline
}

type DebugService struct {
	repo             *Repository
	backupsDir       string
	rateLimitChecker ImageRateLimitChecker
	food             *Service
	realtime         RealtimePublisher
}

type CatalogueImportEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CatalogueExportPayload struct {
	Timestamp  string                 `json:"timestamp"`
	Total      int                    `json:"totalEntries"`
	ExportedBy string                 `json:"exportedBy"`
	Metadata   map[string]any         `json:"metadata"`
	Entries    []CatalogueImportEntry `json:"entries"`
}

type LabGeneratedProduct struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Kcals       int64   `json:"kcals"`
	Protein     float64 `json:"protein"`
	Fat         float64 `json:"fat"`
	Carbs       float64 `json:"carbs"`
	Fiber       float64 `json:"fiber"`
}

type LabGenerateProductResult struct {
	Data         any            `json:"data"`
	BatchInfo    map[string]int `json:"batchInfo,omitempty"`
	Saved        bool           `json:"saved,omitempty"`
	PreviousData map[string]any `json:"previousData,omitempty"`
}

type LabGenerateEmbeddingsResult struct {
	Data      []map[string]any `json:"data"`
	BatchInfo map[string]int   `json:"batchInfo"`
}

type LabGenerateImageResult struct {
	Data      any            `json:"data"`
	BatchInfo map[string]int `json:"batchInfo,omitempty"`
}

func NewLabService(repo *Repository, food *Service, pipeline *ImagePipeline) *LabService {
	return &LabService{repo: repo, food: food, pipeline: pipeline}
}

func NewDebugService(repo *Repository, backupsDir string, rateLimitChecker ImageRateLimitChecker, food *Service, realtime RealtimePublisher) *DebugService {
	return &DebugService{repo: repo, backupsDir: backupsDir, rateLimitChecker: rateLimitChecker, food: food, realtime: realtime}
}

func (s *DebugService) RunPersonalKcalJob(ctx context.Context) (*PersonalKcalJobResult, error) {
	if s.food == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Personal kcal job is not configured")
	}
	result, err := s.food.RunPersonalKcalJob(ctx)
	if err != nil {
		return nil, err
	}
	if s.realtime != nil {
		for _, user := range result.Users {
			if user.Success && user.MonthsComputed > 0 {
				s.realtime.MarkUserUpdated(user.UserID)
			}
		}
	}
	return result, nil
}

func (s *DebugService) ResetPersonalKcal(ctx context.Context, userID int64) error {
	if err := s.repo.DeletePersonalHistoryForUser(ctx, userID); err != nil {
		return err
	}
	s.food.InvalidateStats(userID)
	if s.realtime != nil {
		s.realtime.MarkUserUpdated(userID)
	}
	return nil
}

func (s *DebugService) ResetPersonalKcalAll(ctx context.Context) error {
	return s.repo.DeletePersonalHistoryForAllUsers(ctx)
}

func (s *LabService) GenerateProduct(ctx context.Context, description string, catalogueID int64, nextN int, useKcals bool, saveToDB bool) (*LabGenerateProductResult, error) {
	if strings.TrimSpace(description) != "" {
		generated, err := s.generateProductFromDescription(ctx, description)
		if err != nil {
			return nil, err
		}
		return &LabGenerateProductResult{Data: generated}, nil
	}
	if catalogueID > 0 {
		result, err := s.generateProductFromCatalogueID(ctx, catalogueID, useKcals, saveToDB)
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	if nextN > 0 {
		return s.generateProductBatch(ctx, nextN, useKcals, saveToDB)
	}
	return nil, legacy.NewError(legacy.ErrorKindValidation, "Either description, id, or nextN query parameter must be provided")
}

func (s *LabService) generateProductFromDescription(ctx context.Context, description string) (LabGeneratedProduct, error) {
	preview, err := s.food.GenerateProductPreview(ctx, description)
	if err != nil {
		return LabGeneratedProduct{}, err
	}
	return previewToLabProduct(preview), nil
}

func (s *LabService) generateProductFromCatalogueID(ctx context.Context, catalogueID int64, useKcals bool, saveToDB bool) (*LabGenerateProductResult, error) {
	entry, err := s.repo.GetCatalogueEntry(ctx, catalogueID)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load product", err)
	}
	if entry == nil {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, fmt.Sprintf("Catalogue entry with ID %d not found", catalogueID))
	}
	input := strings.TrimSpace(entry.Name)
	if useKcals {
		input = strings.TrimSpace(fmt.Sprintf("%s %d ккал", input, entry.Kcals))
	}
	if description := strings.TrimSpace(nullableStringValue(entry.Description)); description != "" {
		input = strings.TrimSpace(input + " " + description)
	}
	preview, err := s.food.GenerateProductPreview(ctx, input)
	if err != nil {
		return nil, err
	}
	generated := previewToLabProduct(preview)
	result := &LabGenerateProductResult{Data: generated}
	if !saveToDB {
		return result, nil
	}
	nameVector, descriptionVector, err := s.food.generateProductEmbeddings(ctx, ProductInput{Name: generated.Name, Description: generated.Description})
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindExternal, "Failed to generate product embeddings", err)
	}
	updated, err := s.repo.UpdateGeneratedCatalogueEntry(ctx, catalogueID, ProductInput{
		Name:           generated.Name,
		Kcals:          generated.Kcals,
		Protein:        generated.Protein,
		Fat:            generated.Fat,
		Carbs:          generated.Carbs,
		Fiber:          generated.Fiber,
		Description:    generated.Description,
		NameVector:     nameVector,
		DescriptionVec: descriptionVector,
	})
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to save generated product", err)
	}
	if !updated {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, "Product not found")
	}
	s.food.catalogueChanged()
	result.Saved = true
	result.PreviousData = map[string]any{"id": entry.ID, "name": entry.Name, "legacyName": nullableStringPtr(entry.LegacyName), "kcals": entry.Kcals}
	return result, nil
}

func (s *LabService) generateProductBatch(ctx context.Context, nextN int, useKcals bool, saveToDB bool) (*LabGenerateProductResult, error) {
	entries, err := s.repo.GetCatalogueEntriesWithoutDescription(ctx, nextN)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load products for generation", err)
	}
	if len(entries) == 0 {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, "No catalogue entries without description found")
	}
	results := make([]map[string]any, 0, len(entries))
	successCount := 0
	failedCount := 0
	for _, entry := range entries {
		item := map[string]any{"id": entry.ID, "saved": false}
		input := strings.TrimSpace(entry.Name)
		if useKcals {
			input = strings.TrimSpace(fmt.Sprintf("%s %d ккал", input, entry.Kcals))
		}
		preview, err := s.food.GenerateProductPreview(ctx, input)
		if err != nil {
			item["error"] = legacy.ErrorMessageOf(err, "Failed to generate product")
			results = append(results, item)
			failedCount++
			continue
		}
		generated := previewToLabProduct(preview)
		item["generated"] = generated
		if saveToDB {
			nameVector, descriptionVector, err := s.food.generateProductEmbeddings(ctx, ProductInput{Name: generated.Name, Description: generated.Description})
			if err != nil {
				item["error"] = legacy.ErrorMessageOf(err, "Failed to generate product embeddings")
				results = append(results, item)
				failedCount++
				continue
			}
			updated, err := s.repo.UpdateGeneratedCatalogueEntry(ctx, entry.ID, ProductInput{
				Name:           generated.Name,
				Kcals:          generated.Kcals,
				Protein:        generated.Protein,
				Fat:            generated.Fat,
				Carbs:          generated.Carbs,
				Fiber:          generated.Fiber,
				Description:    generated.Description,
				NameVector:     nameVector,
				DescriptionVec: descriptionVector,
			})
			if err != nil || !updated {
				item["error"] = "Failed to save to database"
				results = append(results, item)
				failedCount++
				continue
			}
			s.food.catalogueChanged()
			item["saved"] = true
		}
		results = append(results, item)
		successCount++
	}
	return &LabGenerateProductResult{
		Data: results,
		BatchInfo: map[string]int{
			"totalRequested": nextN,
			"totalProcessed": len(entries),
			"successCount":   successCount,
			"failedCount":    failedCount,
		},
	}, nil
}

func (s *LabService) GenerateEmbeddings(ctx context.Context, count int) (*LabGenerateEmbeddingsResult, error) {
	if s.food.embeddingGenerator == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Embedding generator is not configured")
	}
	entries, err := s.repo.GetCatalogueEntriesWithoutEmbeddings(ctx, count)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load products for embeddings", err)
	}
	if len(entries) == 0 {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, "No catalogue entries without embeddings found")
	}
	results := make([]map[string]any, 0, len(entries))
	successCount := 0
	failedCount := 0
	for _, entry := range entries {
		item := map[string]any{"id": entry.ID, "name": entry.Name, "saved": false}
		nameVector := entry.NameVec
		descriptionVector := entry.DescriptionVec
		if len(nameVector) == 0 {
			embedding, err := s.food.embeddingGenerator.GenerateEmbedding(ctx, entry.Name)
			if err != nil {
				item["error"] = legacy.ErrorMessageOf(err, "Failed to generate name embedding")
				results = append(results, item)
				failedCount++
				continue
			}
			nameVector = encodeFloat32Blob(embedding)
			item["nameEmbedding"] = map[string]any{"dimensions": len(embedding)}
		}
		descriptionText := strings.TrimSpace(entry.Description.String)
		if len(descriptionVector) == 0 && descriptionText != "" {
			embedding, err := s.food.embeddingGenerator.GenerateEmbedding(ctx, descriptionText)
			if err != nil {
				item["error"] = legacy.ErrorMessageOf(err, "Failed to generate description embedding")
				results = append(results, item)
				failedCount++
				continue
			}
			descriptionVector = encodeFloat32Blob(embedding)
			item["descriptionEmbedding"] = map[string]any{"dimensions": len(embedding)}
		}
		updated, err := s.repo.UpdateCatalogueEntryEmbeddings(ctx, entry.ID, nameVector, descriptionVector)
		if err != nil || !updated {
			item["error"] = "Failed to save embeddings to database"
			results = append(results, item)
			failedCount++
			continue
		}
		s.food.catalogueChanged()
		item["saved"] = true
		results = append(results, item)
		successCount++
	}
	return &LabGenerateEmbeddingsResult{Data: results, BatchInfo: map[string]int{"totalRequested": count, "totalProcessed": len(entries), "successCount": successCount, "failedCount": failedCount}}, nil
}

func (s *LabService) RebuildImageVariants(ctx context.Context, catalogueID int64, nextN int) (*LabGenerateImageResult, error) {
	if s.pipeline == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Image pipeline is not configured")
	}
	if catalogueID > 0 {
		entry, err := s.repo.GetCatalogueEntry(ctx, catalogueID)
		if err != nil {
			return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load product", err)
		}
		if entry == nil {
			return nil, legacy.NewError(legacy.ErrorKindNotFound, fmt.Sprintf("Catalogue entry with ID %d not found", catalogueID))
		}
		variants, err := s.pipeline.RebuildImageVariants(ctx, catalogueID)
		if err != nil {
			return nil, err
		}
		return &LabGenerateImageResult{Data: map[string]any{"catalogueId": entry.ID, "name": entry.Name, "variants": variants}}, nil
	}
	ids, err := s.pipeline.store.CatalogueIDsWithOriginals()
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to inspect original images", err)
	}
	if len(ids) == 0 {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, "Original images directory not found")
	}
	allEntries, err := s.repo.GetAllCatalogueDebugEntries(ctx)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load catalogue entries", err)
	}
	nameByID := map[int64]string{}
	for _, entry := range allEntries {
		nameByID[entry.ID] = entry.Name
	}
	processable := make([]int64, 0)
	for _, id := range ids {
		hasAll, err := s.pipeline.store.HasAllVariants(id)
		if err != nil {
			continue
		}
		if !hasAll {
			processable = append(processable, id)
		}
	}
	if len(processable) == 0 {
		return &LabGenerateImageResult{Data: []map[string]any{}, BatchInfo: map[string]int{"totalRequested": nextN, "totalProcessed": 0, "successCount": 0, "failedCount": 0, "skippedCount": len(allEntries)}}, nil
	}
	if nextN > 0 && len(processable) > nextN {
		processable = processable[:nextN]
	}
	results := make([]map[string]any, 0, len(processable))
	successCount := 0
	failedCount := 0
	for _, id := range processable {
		variants, err := s.pipeline.RebuildImageVariants(ctx, id)
		if err != nil {
			results = append(results, map[string]any{"id": id, "name": nameByID[id], "regenerated": false, "error": legacy.ErrorMessageOf(err, "Failed to rebuild image variants")})
			failedCount++
			continue
		}
		results = append(results, map[string]any{"id": id, "name": nameByID[id], "regenerated": true, "variants": variants})
		successCount++
	}
	return &LabGenerateImageResult{Data: results, BatchInfo: map[string]int{"totalRequested": nextN, "totalProcessed": len(processable), "successCount": successCount, "failedCount": failedCount, "skippedCount": len(allEntries) - len(processable)}}, nil
}

func (s *DebugService) Ping(ctx context.Context) (string, error) {
	return "pong", nil
}

func (s *DebugService) ListCatalogueEntries(ctx context.Context) (map[string]any, error) {
	entries, err := s.repo.GetAllCatalogueDebugEntries(ctx)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load catalogue entries", err)
	}
	payloadEntries := make([]map[string]any, 0, len(entries))
	entriesWithNutrition := 0
	entriesWithDescription := 0
	entriesWithEmbedding := 0
	for _, entry := range entries {
		hasNutrition := nullableFloat64Value(entry.Protein) > 0 || nullableFloat64Value(entry.Fat) > 0 || nullableFloat64Value(entry.Carbs) > 0
		hasDescription := strings.TrimSpace(entry.Description.String) != ""
		hasEmbedding := len(entry.NameVec) > 0 || len(entry.DescriptionVec) > 0
		if hasNutrition {
			entriesWithNutrition++
		}
		if hasDescription {
			entriesWithDescription++
		}
		if hasEmbedding {
			entriesWithEmbedding++
		}
		payloadEntries = append(payloadEntries, map[string]any{
			"id":             entry.ID,
			"name":           entry.Name,
			"kcals":          entry.Kcals,
			"hasNutrition":   hasNutrition,
			"hasDescription": hasDescription,
			"hasEmbedding":   hasEmbedding,
			"protein":        nullableFloat64Value(entry.Protein),
			"fat":            nullableFloat64Value(entry.Fat),
			"carbs":          nullableFloat64Value(entry.Carbs),
			"fiber":          nullableFloat64Value(entry.Fiber),
			"description":    entry.Description.String,
		})
	}
	return map[string]any{
		"result": true,
		"stats": map[string]any{
			"totalEntries":           len(entries),
			"entriesWithNutrition":   entriesWithNutrition,
			"entriesWithDescription": entriesWithDescription,
			"entriesWithEmbedding":   entriesWithEmbedding,
		},
		"entries": payloadEntries,
	}, nil
}

func (s *DebugService) CheckRateLimits(ctx context.Context) (map[string]any, error) {
	if s.rateLimitChecker == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "OpenRouter rate-limit checker is not configured")
	}
	data, err := s.rateLimitChecker.CheckRateLimits(ctx)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindExternal, "Failed to check rate limits", err)
	}
	return map[string]any{"result": true, "data": data}, nil
}

func (s *DebugService) ExportCatalogue(ctx context.Context) (map[string]any, error) {
	entries, err := s.repo.GetAllCatalogueDebugEntries(ctx)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to load catalogue entries", err)
	}
	if len(entries) == 0 {
		return nil, legacy.NewError(legacy.ErrorKindNotFound, "No catalogue entries found")
	}
	if err := os.MkdirAll(s.backupsDir, 0o755); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to create backups directory", err)
	}
	cleaned := make([]CatalogueImportEntry, 0, len(entries))
	for _, entry := range entries {
		cleaned = append(cleaned, CatalogueImportEntry{Name: entry.Name, Description: strings.TrimSpace(entry.Description.String)})
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	filename := fmt.Sprintf("catalogue-backup-%s.json", time.Now().UTC().Format("2006-01-02"))
	filePath := filepath.Join(s.backupsDir, filename)
	payload := CatalogueExportPayload{Timestamp: timestamp, Total: len(cleaned), ExportedBy: "debug-api", Metadata: map[string]any{"version": "1.0", "description": "Food catalogue backup export", "source": "foodCatalogue table"}, Entries: cleaned}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to encode export payload", err)
	}
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write export file", err)
	}
	return map[string]any{"result": true, "filename": filename, "filePath": filepath.ToSlash(filePath), "totalEntries": len(cleaned), "timestamp": timestamp, "message": fmt.Sprintf("Successfully exported %d catalogue entries", len(cleaned))}, nil
}

func (s *DebugService) ImportCatalogue(ctx context.Context, filename string) (map[string]any, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Filename parameter is required")
	}
	if filepath.Base(filename) != filename || !strings.HasSuffix(strings.ToLower(filename), ".json") {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Invalid filename")
	}
	filePath := filepath.Join(s.backupsDir, filename)
	body, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, legacy.NewError(legacy.ErrorKindNotFound, fmt.Sprintf("Backup file not found: %s", filename))
		}
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to read backup file", err)
	}
	var payload CatalogueExportPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Invalid JSON format in backup file")
	}
	if len(payload.Entries) == 0 {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Invalid backup file format - missing entries array")
	}
	defer s.food.catalogueChanged()
	deletedCount, err := s.repo.ClearAllCatalogueEntries(ctx)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to clear existing catalogue entries", err)
	}
	importedCount, err := s.repo.ImportCatalogueEntries(ctx, payload.Entries)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to import catalogue entries", err)
	}
	return map[string]any{"result": true, "filename": filename, "totalEntriesInBackup": len(payload.Entries), "deletedCount": deletedCount, "importedCount": importedCount, "skippedCount": len(payload.Entries) - int(importedCount), "timestamp": time.Now().UTC().Format(time.RFC3339), "message": fmt.Sprintf("Successfully imported %d catalogue entries from backup", importedCount)}, nil
}

func previewToLabProduct(preview ProductPreviewData) LabGeneratedProduct {
	return LabGeneratedProduct{Name: preview.GeneralizedName, Description: preview.Description, Kcals: preview.Kcals, Protein: preview.Protein, Fat: preview.Fat, Carbs: preview.Carbs, Fiber: preview.Fiber}
}

func sortableKeys(values map[int64]string) []int64 {
	ids := make([]int64, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i int, j int) bool { return ids[i] < ids[j] })
	return ids
}
