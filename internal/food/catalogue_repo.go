package food

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type ProductInput struct {
	Name           string
	Kcals          int64
	Protein        float64
	Fat            float64
	Carbs          float64
	Fiber          float64
	Description    string
	Archived       *bool // nil on update leaves the flag untouched; nil on create means not archived
	NameVector     []byte
	DescriptionVec []byte
}

func (r *Repository) GetCatalogueEntryByName(ctx context.Context, name string) (*CatalogueRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, archived
		FROM foodCatalogue
		WHERE lower(name) = lower(?)
	`, strings.TrimSpace(name))

	var result CatalogueRow
	if err := row.Scan(&result.ID, &result.Name, &result.Kcals, &result.Protein, &result.Fat, &result.Carbs, &result.Fiber, &result.Description, &result.LegacyName, &result.Archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get catalogue entry by name: %w", err)
	}

	return &result, nil
}

func (r *Repository) CreateCatalogueEntry(ctx context.Context, tx *sql.Tx, input ProductInput) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO foodCatalogue (name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec, archived)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE(?, 0))
	`, input.Name, input.Kcals, input.Protein, input.Fat, input.Carbs, input.Fiber, input.Description, input.Name, input.NameVector, input.DescriptionVec, input.Archived)
	if err != nil {
		return 0, fmt.Errorf("create catalogue entry: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create catalogue entry last insert id: %w", err)
	}

	return id, nil
}

func (r *Repository) UpdateCatalogueEntry(ctx context.Context, tx *sql.Tx, catalogueID int64, input ProductInput) (bool, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE foodCatalogue
		SET name = ?, kcals = ?, protein = ?, fat = ?, carbs = ?, fiber = ?, description = ?, legacyName = ?, nameVec = ?, descriptionVec = ?, archived = COALESCE(?, archived)
		WHERE id = ?
	`, input.Name, input.Kcals, input.Protein, input.Fat, input.Carbs, input.Fiber, input.Description, input.Name, input.NameVector, input.DescriptionVec, input.Archived, catalogueID)
	if err != nil {
		return false, fmt.Errorf("update catalogue entry: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update catalogue entry rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}

// GetCatalogueArchived reads the product's archive flag inside tx; found is false when the product does not exist.
func (r *Repository) GetCatalogueArchived(ctx context.Context, tx *sql.Tx, catalogueID int64) (archived bool, found bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT archived FROM foodCatalogue WHERE id = ?`, catalogueID).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("get catalogue archived: %w", err)
	}
	return archived, true, nil
}

func (r *Repository) DeleteCatalogueEntry(ctx context.Context, tx *sql.Tx, catalogueID int64) (bool, error) {
	result, err := tx.ExecContext(ctx, `DELETE FROM foodCatalogue WHERE id = ?`, catalogueID)
	if err != nil {
		return false, fmt.Errorf("delete catalogue entry: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete catalogue entry rows affected: %w", err)
	}

	return rowsAffected > 0, nil
}
