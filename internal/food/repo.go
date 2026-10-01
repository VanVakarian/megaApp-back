package food

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"megaapp-back/internal/platform/sqlite"
)

type DiaryRow struct {
	ID              int64
	DateISO         string
	FoodCatalogueID int64
	FoodWeight      int64
	History         string
	Version         int64
}

type WeightRow struct {
	DateISO string
	Weight  float64
}

type CatalogueRow struct {
	ID          int64
	Name        string
	LegacyName  sql.NullString
	Kcals       int64
	Protein     sql.NullFloat64
	Fat         sql.NullFloat64
	Carbs       sql.NullFloat64
	Fiber       sql.NullFloat64
	Description sql.NullString
	Archived    bool
}

type StatsDiaryRow struct {
	DateISO         string
	FoodWeight      float64
	FoodCatalogueID int64
}

type Repository struct {
	db    *sql.DB
	write sqlite.WriteDB
}

func NewRepository(read *sql.DB, write sqlite.WriteDB) *Repository {
	return &Repository{db: read, write: write}
}

func (r *Repository) GetDiaryRange(ctx context.Context, userID int64, startDate string, endDate string) ([]DiaryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, dateISO, foodCatalogueId, foodWeight, history, ver
		FROM foodDiary
		WHERE usersId = ? AND dateISO BETWEEN ? AND ?
		ORDER BY dateISO ASC
	`, userID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get diary range: %w", err)
	}
	defer rows.Close()

	return scanDiaryRows(rows)
}

func (r *Repository) GetAllDiaryEntries(ctx context.Context, userID int64) ([]DiaryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, dateISO, foodCatalogueId, foodWeight, history, ver
		FROM foodDiary
		WHERE usersId = ?
		ORDER BY dateISO ASC, id ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("get all diary entries: %w", err)
	}
	defer rows.Close()

	return scanDiaryRows(rows)
}

func (r *Repository) GetWeightRange(ctx context.Context, userID int64, startDate string, endDate string) ([]WeightRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT dateISO, weight
		FROM foodBodyWeight
		WHERE usersId = ? AND dateISO BETWEEN ? AND ?
		ORDER BY dateISO ASC
	`, userID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get weight range: %w", err)
	}
	defer rows.Close()

	return scanWeightRows(rows)
}

func (r *Repository) GetAllWeightEntries(ctx context.Context, userID int64) ([]WeightRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT dateISO, weight
		FROM foodBodyWeight
		WHERE usersId = ?
		ORDER BY dateISO ASC, id ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("get all weight entries: %w", err)
	}
	defer rows.Close()

	return scanWeightRows(rows)
}

func (r *Repository) GetCatalogue(ctx context.Context) ([]CatalogueRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, archived
		FROM foodCatalogue
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("get catalogue: %w", err)
	}
	defer rows.Close()

	var result []CatalogueRow
	for rows.Next() {
		var row CatalogueRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kcals, &row.Protein, &row.Fat, &row.Carbs, &row.Fiber, &row.Description, &row.LegacyName, &row.Archived); err != nil {
			return nil, fmt.Errorf("scan catalogue row: %w", err)
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalogue rows: %w", err)
	}

	return result, nil
}

func (r *Repository) GetCatalogueEntry(ctx context.Context, catalogueID int64) (*CatalogueRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, archived
		FROM foodCatalogue
		WHERE id = ?
	`, catalogueID)

	var result CatalogueRow
	if err := row.Scan(&result.ID, &result.Name, &result.Kcals, &result.Protein, &result.Fat, &result.Carbs, &result.Fiber, &result.Description, &result.LegacyName, &result.Archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get catalogue entry: %w", err)
	}

	return &result, nil
}

func (r *Repository) CountDiaryEntriesByCatalogueID(ctx context.Context, catalogueID int64) (int64, error) {
	row := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM foodDiary WHERE foodCatalogueId = ?`, catalogueID)

	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count diary entries by catalogue id: %w", err)
	}

	return count, nil
}

func (r *Repository) GetUserFirstDate(ctx context.Context, userID int64) (string, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT MIN(date) as firstDate
		FROM (
			SELECT dateISO as date FROM foodDiary WHERE usersId = ?
			UNION
			SELECT dateISO as date FROM foodBodyWeight WHERE usersId = ?
		)
	`, userID, userID)

	var firstDate sql.NullString
	if err := row.Scan(&firstDate); err != nil {
		return "", fmt.Errorf("get user first date: %w", err)
	}

	return firstDate.String, nil
}

func (r *Repository) GetAllUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("get all user ids: %w", err)
	}
	defer rows.Close()

	result := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan user id: %w", err)
		}
		result = append(result, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user ids: %w", err)
	}
	return result, nil
}

func scanDiaryRows(rows *sql.Rows) ([]DiaryRow, error) {
	var result []DiaryRow
	for rows.Next() {
		var row DiaryRow
		if err := rows.Scan(&row.ID, &row.DateISO, &row.FoodCatalogueID, &row.FoodWeight, &row.History, &row.Version); err != nil {
			return nil, fmt.Errorf("scan diary row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate diary rows: %w", err)
	}
	return result, nil
}

func scanWeightRows(rows *sql.Rows) ([]WeightRow, error) {
	var result []WeightRow
	for rows.Next() {
		var row WeightRow
		if err := rows.Scan(&row.DateISO, &row.Weight); err != nil {
			return nil, fmt.Errorf("scan weight row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate weight rows: %w", err)
	}
	return result, nil
}

// ProductHistoryRow is one foodDiary event (one consumption instance) for the product-history
// list — every INSERT is its own row (no per-day merging, see write_repo.go), so this is already
// event-level, not day-aggregated.
type ProductHistoryRow struct {
	ID              int64
	DateISO         string
	FoodCatalogueID int64
	FoodWeight      int64
}

// GetProductHistory returns up to `limit` foodDiary rows for the given catalogue ids, newest
// first (dateISO DESC, id DESC), continuing after `cursor` when given.
func (r *Repository) GetProductHistory(ctx context.Context, userID int64, catalogueIDs []int64, cursor *ProductHistoryCursor, limit int) ([]ProductHistoryRow, error) {
	if len(catalogueIDs) == 0 {
		return []ProductHistoryRow{}, nil
	}

	placeholders := make([]string, 0, len(catalogueIDs))
	args := make([]any, 0, len(catalogueIDs)+4)
	args = append(args, userID)
	for _, id := range catalogueIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	query := `
		SELECT id, dateISO, foodCatalogueId, foodWeight
		FROM foodDiary
		WHERE usersId = ? AND foodCatalogueId IN (` + strings.Join(placeholders, ",") + `)
	`
	if cursor != nil {
		query += " AND (dateISO < ? OR (dateISO = ? AND id < ?))"
		args = append(args, cursor.DateISO, cursor.DateISO, cursor.ID)
	}
	query += " ORDER BY dateISO DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get product history: %w", err)
	}
	defer rows.Close()

	result := make([]ProductHistoryRow, 0)
	for rows.Next() {
		var row ProductHistoryRow
		if err := rows.Scan(&row.ID, &row.DateISO, &row.FoodCatalogueID, &row.FoodWeight); err != nil {
			return nil, fmt.Errorf("scan product history row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate product history rows: %w", err)
	}
	return result, nil
}

func (r *Repository) GetStatsDiaryHistory(ctx context.Context, userID int64, startDate string, endDate string) ([]StatsDiaryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT dateISO, foodWeight, foodCatalogueId
		FROM foodDiary
		WHERE usersId = ? AND dateISO BETWEEN ? AND ?
		ORDER BY dateISO ASC
	`, userID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("get stats diary history: %w", err)
	}
	defer rows.Close()

	var result []StatsDiaryRow
	for rows.Next() {
		var row StatsDiaryRow
		if err := rows.Scan(&row.DateISO, &row.FoodWeight, &row.FoodCatalogueID); err != nil {
			return nil, fmt.Errorf("scan stats diary row: %w", err)
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stats diary rows: %w", err)
	}

	return result, nil
}
