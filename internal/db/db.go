// Package db opens the service's SQLite database, applies schema migrations,
// and holds the queries the rest of the program uses.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps *sql.DB so queries can be methods on it.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at filePath and brings
// its schema up to date.
//
// Every connection gets the same settings:
//   - foreign_keys: files.api_key_id must point at a real api_keys row.
//   - journal_mode WAL: readers don't block the writer and vice versa.
//   - busy_timeout: wait up to 5 s for a lock instead of failing at once.
func Open(ctx context.Context, filePath string) (*DB, error) {
	dsn := "file:" + filePath +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open database %s: %w", filePath, err)
	}

	d := &DB{sqlDB}
	if err := d.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded NNNN_name.sql files in version order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		numPart, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: name must look like 0001_description.sql", name)
		}
		v, err := strconv.Atoi(numPart)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: bad version number", name)
		}
		body, err := fs.ReadFile(migrationFS, path.Join("migrations", name))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations must be numbered 1, 2, 3, ... without gaps; found %s", m.name)
		}
	}
	return ms, nil
}

// migrate applies every migration newer than the database's user_version.
// SQLite stores user_version in the file header; each migration and its
// version bump run in one transaction, so a failure leaves the schema as it was.
func (d *DB) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}

	current, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > len(ms) {
		return fmt.Errorf("database schema version %d is newer than this program knows (%d)", current, len(ms))
	}

	for _, m := range ms[current:] {
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		// PRAGMA can't take a bound parameter; version is an int we parsed.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}
	return nil
}

// SchemaVersion reports the number of the last migration applied.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}
