package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type approvalQueries struct {
	db.DBTX
	sql  string
	args []any
	err  error
}

func (q *approvalQueries) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.sql, q.args = sql, args
	return approvalRow{q.err}
}

type approvalRow struct{ err error }

func (r approvalRow) Scan(dest ...any) error {
	if r.err == nil {
		*dest[0].(*bool) = true
	}
	return r.err
}

func TestApprovalLookupSemantics(t *testing.T) {
	ctx := context.Background()
	const user = "00000000-0000-0000-0000-000000000001"
	const unit = "00000000-0000-0000-0000-000000000002"
	failure := &pgconn.PgError{Code: "08006"}
	for _, lock := range []bool{false, true} {
		for _, queryErr := range []error{pgx.ErrNoRows, failure} {
			q := &approvalQueries{err: queryErr}
			r := &repository{q: db.New(q)}
			_, err := r.Get(ctx, user, lock)
			want := queryErr
			if queryErr == pgx.ErrNoRows {
				want = domain.ErrNotFound
			}
			if !errors.Is(err, want) || strings.Contains(q.sql, "FOR UPDATE") != lock || len(q.args) != 1 || q.args[0].(pgtype.UUID).String() != user {
				t.Fatalf("Get lock=%v: err=%v sql=%s args=%v", lock, err, q.sql, q.args)
			}
		}
	}
	q := &approvalQueries{}
	r := &repository{q: db.New(q)}
	allowed, err := r.IsOrganizer(ctx, user, unit)
	if err != nil || !allowed || len(q.args) != 2 || q.args[0].(pgtype.UUID).String() != user || q.args[1].(pgtype.UUID).String() != unit {
		t.Fatalf("organizer result=%v err=%v args=%v", allowed, err, q.args)
	}
	q.err = failure
	if _, err := r.IsOrganizer(ctx, user, unit); !errors.Is(err, failure) {
		t.Fatalf("organizer swallowed query error: %v", err)
	}
	for _, ids := range [][2]string{{"bad", unit}, {user, "bad"}} {
		q.sql = ""
		if _, err := r.IsOrganizer(ctx, ids[0], ids[1]); !errors.Is(err, domain.ErrValidation) || q.sql != "" {
			t.Fatalf("invalid organizer IDs queried DB: %v %s", err, q.sql)
		}
	}
	q.sql = ""
	if _, err := r.Get(ctx, "bad", true); !errors.Is(err, domain.ErrValidation) || q.sql != "" {
		t.Fatalf("invalid event ID queried DB: %v %s", err, q.sql)
	}
}
