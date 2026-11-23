package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// InSavepoint runs fn inside a savepoint of tx so a failure in fn undoes only
// fn's work and leaves the outer transaction usable. fn's own error is returned
// as fnErr for the caller to tolerate. fatal reports a savepoint that could not
// be begun, released or rolled back; the outer transaction must then fail.
func InSavepoint(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) (fnErr, fatal error) {
	// pgx nested transactions are savepoints, not independent transactions.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin savepoint: %w", err)
	}
	if fnErr = fn(sp); fnErr == nil {
		if err := sp.Commit(ctx); err != nil {
			return nil, fmt.Errorf("release savepoint: %w", err)
		}
		return nil, nil
	}

	// Cancellation must not prevent cleanup of the aborted savepoint.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := sp.Rollback(cleanupCtx); err != nil {
		return fnErr, errors.Join(fnErr, fmt.Errorf("rollback savepoint: %w", err))
	}
	return fnErr, nil
}
