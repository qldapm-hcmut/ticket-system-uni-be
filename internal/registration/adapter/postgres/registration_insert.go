package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
)

const checkinCodeAttempts = 5

func (r *repo) insertRegistration(ctx context.Context, params db.RegisterStudentParams) (db.RegisterStudentRow, error) {
	for attempt := 0; attempt < checkinCodeAttempts; attempt++ {
		code, err := generateCheckinCode(r.random)
		if err != nil {
			return db.RegisterStudentRow{}, err
		}
		params.CheckinCode = code

		// pgx nested transactions are savepoints, not independent transactions.
		// A unique violation aborts the subtransaction: roll back its INSERT and
		// counter CTE before retrying, retaining the outer event lock from Active.
		sp, err := r.tx.Begin(ctx)
		if err != nil {
			return db.RegisterStudentRow{}, fmt.Errorf("begin registration savepoint: %w", err)
		}
		row, err := r.q.WithTx(sp).RegisterStudent(ctx, params)
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			if releaseErr := sp.Commit(ctx); releaseErr != nil {
				return db.RegisterStudentRow{}, fmt.Errorf("release registration savepoint: %w", releaseErr)
			}
			return row, err
		}

		// Cancellation must not prevent cleanup; never retry if rollback fails.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		rollbackErr := sp.Rollback(cleanupCtx)
		cancel()
		if rollbackErr != nil {
			return db.RegisterStudentRow{}, errors.Join(err, fmt.Errorf("rollback registration savepoint: %w", rollbackErr))
		}
		var p *pgconn.PgError
		if !errors.As(err, &p) || p.Code != "23505" || p.ConstraintName != "uq_checkin_code" {
			return db.RegisterStudentRow{}, err
		}
		if attempt == checkinCodeAttempts-1 {
			return db.RegisterStudentRow{}, fmt.Errorf("check-in code collision attempts exhausted: %w", err)
		}
	}
	panic("unreachable registration attempt")
}
