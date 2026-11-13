//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

type cancellationStartDB struct {
	db.DBTX
	after func(time.Time)
}

func (d cancellationStartDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := d.DBTX.QueryRow(ctx, sql, args...)
	if strings.Contains(sql, "-- name: GetCancellationDecisionTime :one") {
		return cancellationDecisionRow{Row: row, after: d.after}
	}
	return row
}

type cancellationDecisionRow struct {
	pgx.Row
	after func(time.Time)
}

func (r cancellationDecisionRow) Scan(dest ...any) error {
	if err := r.Row.Scan(dest...); err != nil {
		return err
	}
	r.after(dest[0].(*pgtype.Timestamptz).Time)
	return nil
}

func testCancellationDecisionBeforeStart(t *testing.T, pool *pgxpool.Pool, organizer string, event func(int, int) string, seed func(string, int) []a.Registration, counters func(string, int, int), checkQueue func(string)) {
	// Regression target: re-reading wall time in Cancel/Promote (or using
	// transaction-start now()) instead of reusing the post-lock decision.
	// The test-only query wrapper keeps the real DB clock and all real SQL;
	// after its read it moves start between decision and the remaining writes.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e := event(1, 3)
	regs := seed(e, 3)
	var decision, start time.Time
	u := interleavedUOW{store: New(pool), before: func(r a.RegistrationRepository) a.RegistrationRepository {
		rr := r.(*repo)
		rr.q = db.New(cancellationStartDB{DBTX: rr.tx, after: func(at time.Time) {
			decision = at
			// The event lock is already ours. Move the fixture's start after
			// the decision but before cancellation/promotion and commit. This
			// substitutes for waiting for a scheduled start, without a sleep.
			if err := rr.tx.QueryRow(ctx, `UPDATE events
				SET start_at = clock_timestamp(),
					end_at = clock_timestamp() + interval '1 hour',
					registration_closes_at = clock_timestamp() - interval '1 second'
				WHERE id = $1
				RETURNING start_at`, e).Scan(&start); err != nil {
				t.Fatal(err)
			}
		}})
		return rr
	}}
	out, err := a.New(u).Cancel(ctx, organizer, regs[0].ID, "pre-start decision")
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Before(start) || out.Registration.CancelledAt == nil || !out.Registration.CancelledAt.Equal(decision) || out.Promoted == nil || out.Promoted.ID != regs[1].ID {
		t.Fatalf("decision=%s start=%s result=%+v", decision, start, out)
	}
	var committedAfterStart, sameDecision bool
	var cancelled, promoted, audit int
	if err := pool.QueryRow(ctx, `SELECT
		clock_timestamp() >= e.start_at,
		(SELECT promoted_at = $2::timestamptz FROM registrations WHERE id = $3),
		(SELECT count(*) FROM notifications WHERE event_id = e.id AND type = 'REG_CANCELLED'),
		(SELECT count(*) FROM notifications WHERE event_id = e.id AND type = 'PROMOTED'),
		(SELECT count(*) FROM activity_logs WHERE event_id = e.id AND action = 'CANCEL_ON_BEHALF')
		FROM events e WHERE e.id = $1`, e, decision, regs[1].ID).Scan(&committedAfterStart, &sameDecision, &cancelled, &promoted, &audit); err != nil {
		t.Fatal(err)
	}
	if !committedAfterStart || !sameDecision || cancelled != 1 || promoted != 1 || audit != 1 {
		t.Fatalf("afterStart=%v sameDecision=%v notifications=%d/%d audit=%d", committedAfterStart, sameDecision, cancelled, promoted, audit)
	}
	counters(e, 1, 1)
	checkQueue(e)
}
