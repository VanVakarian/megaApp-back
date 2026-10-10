package food

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SearchIndexRow struct {
	ID             int64
	Name           string
	Archived       bool
	NameVec        []byte
	DescriptionVec []byte
}

func (r *Repository) GetQueryEmbedding(ctx context.Context, query string) ([]byte, error) {
	row := r.db.QueryRowContext(ctx, `SELECT embedding FROM foodSearchQueryEmbeddings WHERE query = ?`, query)

	var embedding []byte
	if err := row.Scan(&embedding); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get query embedding: %w", err)
	}

	return embedding, nil
}

// RecordQueryEmbeddingHit bumps the cache hit counter for an already-cached query embedding.
// Best-effort and separate from GetQueryEmbedding on purpose: it's just popularity tracking, not
// consistency-critical, so it doesn't need to share a transaction with the read, and it goes
// through the write pool on its own instead of forcing every cache-hit read onto it too.
func (r *Repository) RecordQueryEmbeddingHit(ctx context.Context, query string) error {
	if _, err := r.write.ExecContext(ctx, `
		UPDATE foodSearchQueryEmbeddings
		SET hitCount = hitCount + 1, lastUsedAt = ?
		WHERE query = ?
	`, nowUnixMilli(), query); err != nil {
		return fmt.Errorf("update query embedding usage: %w", err)
	}
	return nil
}

func (r *Repository) SaveQueryEmbedding(ctx context.Context, query string, embedding []byte) error {
	_, err := r.write.ExecContext(ctx, `
		INSERT OR REPLACE INTO foodSearchQueryEmbeddings (query, embedding, hitCount, lastUsedAt, createdAt)
		VALUES (?, ?, 1, ?, ?)
	`, query, embedding, nowUnixMilli(), nowUnixMilli())
	if err != nil {
		return fmt.Errorf("save query embedding: %w", err)
	}
	return nil
}

func (r *Repository) GetSearchIndexRows(ctx context.Context) ([]SearchIndexRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, archived, nameVec, descriptionVec
		FROM foodCatalogue
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("get search index rows: %w", err)
	}
	defer rows.Close()

	result := make([]SearchIndexRow, 0)
	for rows.Next() {
		var row SearchIndexRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Archived, &row.NameVec, &row.DescriptionVec); err != nil {
			return nil, fmt.Errorf("scan search index row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search index rows: %w", err)
	}
	return result, nil
}

func nowUnixMilli() int64 {
	return time.Now().UnixMilli()
}
