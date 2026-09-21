package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/skymoon8/tracewell/internal/trace"
)

// Ingest pairs a span with its destination project.
type Ingest struct {
	Span    trace.Span
	Project string
}

// InsertSpans persists many spans under one transaction. Each span is
// wrapped in a SAVEPOINT so a failure rolls back only that span's
// partial writes and the rest of the batch proceeds; the returned
// errors slice is indexed to the input, with nil entries for spans
// that persisted successfully. A transaction-level failure aborts
// everything and is returned as the error.
func (s *Store) InsertSpans(ctx context.Context, items []Ingest) []error {
	errs := make([]error, len(items))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		for i := range errs {
			errs[i] = fmt.Errorf("begin transaction: %w", err)
		}
		return errs
	}
	defer tx.Rollback() // no-op after Commit

	for i, item := range items {
		if err := s.insertSpanSavepoint(ctx, tx, item.Span, item.Project); err != nil {
			errs[i] = err
		}
	}

	if err := tx.Commit(); err != nil {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = fmt.Errorf("commit: %w", err)
			}
		}
	}
	return errs
}

// insertSpanSavepoint wraps one span insert in a SAVEPOINT: on error
// the span's partial writes roll back, leaving the transaction usable
// for the remaining spans.
func (s *Store) insertSpanSavepoint(ctx context.Context, tx *sql.Tx, span trace.Span, projectName string) error {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT insert_span`); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	if err := s.insertSpanTx(ctx, tx, span, projectName); err != nil {
		// Roll back this span's partial writes but keep the outer
		// transaction alive for the rest of the batch.
		_, _ = tx.ExecContext(ctx, `ROLLBACK TO insert_span`)
		_, _ = tx.ExecContext(ctx, `RELEASE insert_span`)
		return err
	}
	if _, err := tx.ExecContext(ctx, `RELEASE insert_span`); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}
