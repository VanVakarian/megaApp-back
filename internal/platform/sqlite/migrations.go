package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Migration struct {
	Version string
	Name    string
	SQL     string
}

func ApplyMigrations(ctx context.Context, db *sql.DB, migrationsDir string) error {
	if err := ensureSchemaMigrationsTable(ctx, db); err != nil {
		return err
	}

	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		return err
	}

	appliedCount := 0
	skippedCount := 0
	var firstSkipped, lastSkipped string
	logSkipped := func() {
		if skippedCount > 0 {
			log.Printf("%d migrations already applied, skipped: %s .. %s", skippedCount, firstSkipped, lastSkipped)
			skippedCount = 0
		}
	}
	for _, migration := range migrations {
		applied, err := isApplied(ctx, db, migration.Version)
		if err != nil {
			return err
		}
		if applied {
			if skippedCount == 0 {
				firstSkipped = migration.Name
			}
			lastSkipped = migration.Name
			skippedCount++
			continue
		}
		logSkipped()
		if err := applyMigration(ctx, db, migration); err != nil {
			return err
		}
		log.Printf("migration %s applied", migration.Name)
		appliedCount++
	}
	logSkipped()

	log.Printf("migrations check complete: %d total, %d newly applied", len(migrations), appliedCount)
	return nil
}

func LoadMigrations(migrationsDir string) ([]Migration, error) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Migration{}, nil
		}
		return nil, fmt.Errorf("load migrations: %w", err)
	}

	files := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}

	sort.Strings(files)

	migrations := make([]Migration, 0, len(files))
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		version := strings.TrimSuffix(name, filepath.Ext(name))
		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			SQL:     string(content),
		})
	}

	return migrations, nil
}

func ensureSchemaMigrationsTable(ctx context.Context, db *sql.DB) error {
	query := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`

	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}

	return nil
}

func isApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		return false, fmt.Errorf("check migration %s: %w", version, err)
	}
	return count > 0, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", migration.Name, err)
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("exec migration %s: %w", migration.Name, err)
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, datetime('now'))`, migration.Version); err != nil {
		return fmt.Errorf("record migration %s: %w", migration.Name, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", migration.Name, err)
	}

	return nil
}
