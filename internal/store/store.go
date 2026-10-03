// Package store wraps the SQLite database: connection setup, migrations,
// and all queries used by the web server.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO needed
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a single-row lookup finds nothing.
var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids
	// SQLITE_BUSY churn and is plenty for this workload.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", e.Name())
		}
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent snapshot of the live database to dest using
// VACUUM INTO. Safe to run while the app is serving requests.
func (s *Store) Backup(ctx context.Context, dest string) error {
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Settings ---------------------------------------------------------------

func (s *Store) Setting(key, fallback string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// helpers ----------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
