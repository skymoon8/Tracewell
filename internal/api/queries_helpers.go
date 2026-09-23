package api

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Sentinel errors mapped to HTTP status codes by the handlers.
var (
	errNotFound  = errors.New("not found")
	errBadCursor = errors.New("invalid cursor")
)

// rowScanner abstracts *sql.Rows and *sql.Row for shared scan helpers.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPage reads rows into T via scan, enforcing the limit+1
// pagination contract: the caller passes limit+1 rows' worth of
// results; if more rows exist the last one is dropped and its id
// becomes the next cursor. For traces the caller post-encodes its own
// cursor shape, so scanPage just reports the dropped row's presence
// through the returned next id (0 when exhausted).
func scanPage[T any](rows *sql.Rows, limit int, scan func(rowScanner) (T, error)) ([]T, int64, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) <= limit {
		return out, 0, nil
	}
	// The extra row only signals "there is more"; drop it.
	out = out[:limit]
	return out, 1, nil
}

// makePlaceholders renders "?,?,?" with n marks.
func makePlaceholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// parseTime reads the RFC3339 timestamps the store writes.
func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
