package food

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type CatalogueDebugRow struct {
	ID             int64
	Name           string
	Kcals          int64
	Protein        sql.NullFloat64
	Fat            sql.NullFloat64
	Carbs          sql.NullFloat64
	Fiber          sql.NullFloat64
	Description    sql.NullString
	LegacyName     sql.NullString
	NameVec        []byte
	DescriptionVec []byte
}

func (r *Repository) GetCatalogueEntriesWithoutDescription(ctx context.Context, limit int) ([]CatalogueRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName
		FROM foodCatalogue
		WHERE description IS NULL OR trim(description) = ''
		ORDER BY id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("get catalogue entries without description: %w", err)
	}
	defer rows.Close()

	result := make([]CatalogueRow, 0)
	for rows.Next() {
		var row CatalogueRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kcals, &row.Protein, &row.Fat, &row.Carbs, &row.Fiber, &row.Description, &row.LegacyName); err != nil {
			return nil, fmt.Errorf("scan catalogue entries without description: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalogue entries without description: %w", err)
	}
	return result, nil
}

func (r *Repository) GetCatalogueEntriesWithoutEmbeddings(ctx context.Context, limit int) ([]CatalogueDebugRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec
		FROM foodCatalogue
		WHERE nameVec IS NULL OR (description IS NOT NULL AND trim(description) != '' AND descriptionVec IS NULL)
		ORDER BY id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("get catalogue entries without embeddings: %w", err)
	}
	defer rows.Close()

	result := make([]CatalogueDebugRow, 0)
	for rows.Next() {
		var row CatalogueDebugRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kcals, &row.Protein, &row.Fat, &row.Carbs, &row.Fiber, &row.Description, &row.LegacyName, &row.NameVec, &row.DescriptionVec); err != nil {
			return nil, fmt.Errorf("scan catalogue entries without embeddings: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalogue entries without embeddings: %w", err)
	}
	return result, nil
}

func (r *Repository) UpdateCatalogueEntryEmbeddings(ctx context.Context, catalogueID int64, nameVector []byte, descriptionVector []byte) (bool, error) {
	result, err := r.write.ExecContext(ctx, `
		UPDATE foodCatalogue
		SET nameVec = ?, descriptionVec = ?
		WHERE id = ?
	`, nameVector, descriptionVector, catalogueID)
	if err != nil {
		return false, fmt.Errorf("update catalogue entry embeddings: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("catalogue entry embeddings rows affected: %w", err)
	}
	return rowsAffected > 0, nil
}

func (r *Repository) UpdateGeneratedCatalogueEntry(ctx context.Context, catalogueID int64, input ProductInput) (bool, error) {
	result, err := r.write.ExecContext(ctx, `
		UPDATE foodCatalogue
		SET name = ?, kcals = ?, protein = ?, fat = ?, carbs = ?, fiber = ?, description = ?,
			legacyName = COALESCE(legacyName, (SELECT name FROM foodCatalogue WHERE id = ?)),
			nameVec = ?, descriptionVec = ?
		WHERE id = ?
	`, input.Name, input.Kcals, input.Protein, input.Fat, input.Carbs, input.Fiber, input.Description, catalogueID, input.NameVector, input.DescriptionVec, catalogueID)
	if err != nil {
		return false, fmt.Errorf("update generated catalogue entry: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("generated catalogue entry rows affected: %w", err)
	}
	return rowsAffected > 0, nil
}

func (r *Repository) GetAllCatalogueDebugEntries(ctx context.Context) ([]CatalogueDebugRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec
		FROM foodCatalogue
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("get all catalogue debug entries: %w", err)
	}
	defer rows.Close()

	result := make([]CatalogueDebugRow, 0)
	for rows.Next() {
		var row CatalogueDebugRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kcals, &row.Protein, &row.Fat, &row.Carbs, &row.Fiber, &row.Description, &row.LegacyName, &row.NameVec, &row.DescriptionVec); err != nil {
			return nil, fmt.Errorf("scan all catalogue debug entries: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all catalogue debug entries: %w", err)
	}
	return result, nil
}

func (r *Repository) ClearAllCatalogueEntries(ctx context.Context) (int64, error) {
	result, err := r.write.ExecContext(ctx, `DELETE FROM foodCatalogue`)
	if err != nil {
		return 0, fmt.Errorf("clear catalogue entries: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("clear catalogue rows affected: %w", err)
	}
	return rowsAffected, nil
}

func (r *Repository) ImportCatalogueEntries(ctx context.Context, entries []CatalogueImportEntry) (int64, error) {
	statement, err := r.write.PrepareContext(ctx, `
		INSERT INTO foodCatalogue (name, description, legacyName)
		VALUES (?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("prepare catalogue import: %w", err)
	}
	defer statement.Close()

	var inserted int64
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if _, err := statement.ExecContext(ctx, name, strings.TrimSpace(entry.Description), name); err == nil {
			inserted++
		}
	}
	return inserted, nil
}
