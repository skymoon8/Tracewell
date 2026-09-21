// Package store persists decoded spans to a SQL database.
package store

import (
	"database/sql"
	"embed"
	"fmt"
)

//go:embed schema.sql
var schemaFS embed.FS

// Store provides persistence operations over a SQL database.
type Store struct {
	db       *sql.DB
	projects projectCache
}

// Open initializes a Store against an existing database handle,
// creating tables if they do not exist.
func Open(db *sql.DB) (*Store, error) {
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db, projects: projectCache{ids: map[string]int64{}}}, nil
}

// migrate applies the schema. Statements are idempotent (IF NOT
// EXISTS), so re-running on an existing database is safe.
func migrate(db *sql.DB) error {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	if _, err := db.Exec(string(b)); err != nil {
		return err
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for read-side queries (added in a
// later milestone).
func (s *Store) DB() *sql.DB { return s.db }
