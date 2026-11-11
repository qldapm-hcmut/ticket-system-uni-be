//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

// These wrappers inject interleavings at port/query boundaries, while retaining
// real PostgreSQL statements, isolation, unique constraints and runner cleanup.
type interleavedUOW struct {
	store  *Store
	before func(a.RegistrationRepository) a.RegistrationRepository
}

func (u interleavedUOW) Do(ctx context.Context, fn func(context.Context, a.RegistrationRepository) error) error {
	return u.store.Do(ctx, func(ctx context.Context, r a.RegistrationRepository) error { return fn(ctx, u.before(r)) })
}

type staleActive struct {
	a.RegistrationRepository
	afterPrecheck func()
}

func (s staleActive) Active(context.Context, string, string) (a.Registration, error) {
	s.afterPrecheck()
	return a.Registration{}, a.ErrNotFound
}

// decisionDB overrides database time only at the SQL boundary in tests. No
// application clock or test override is exposed by the production service.
type decisionDB struct {
	db.DBTX
	at            time.Time
	afterDecision func()
}

func (d decisionDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.Contains(sql, "clock_timestamp()") {
		row := d.DBTX.QueryRow(ctx, "SELECT $1::timestamptz", d.at)
		if d.afterDecision != nil {
			return afterScan{row, d.afterDecision}
		}
		return row
	}
	return d.DBTX.QueryRow(ctx, sql, args...)
}

func mineAt(store *Store, at time.Time) interleavedUOW {
	return interleavedUOW{store: store, before: func(r a.RegistrationRepository) a.RegistrationRepository {
		rr := r.(*repo)
		rr.q = db.New(decisionDB{DBTX: rr.tx, at: at})
		return rr
	}}
}

type afterCountDB struct {
	db.DBTX
	after func()
}

func (d afterCountDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := d.DBTX.QueryRow(ctx, sql, args...)
	if strings.Contains(sql, "-- name: CountMyRegistrations") {
		return afterScan{row, d.after}
	}
	return row
}

type afterScan struct {
	pgx.Row
	after func()
}

func (r afterScan) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if err == nil {
		r.after()
	}
	return err
}

func testMineInterleavings(t *testing.T, store *Store, pool *pgxpool.Pool, ctx context.Context, event func(int, int) string, seed func(string, int) []a.Registration, user func() string) {
	t.Run("unique constraint rollback then fresh ID lookup", func(t *testing.T) {
		e, owner := event(2, 3), user()
		var winner a.Registration
		calls := 0
		u := interleavedUOW{store: store, before: func(r a.RegistrationRepository) a.RegistrationRepository {
			calls++
			if calls == 1 {
				return staleActive{RegistrationRepository: r, afterPrecheck: func() {
					// Commit a concurrent winner after a stale precheck, before the loser INSERT.
					done := make(chan error, 1)
					go func() { var err error; winner, err = a.New(store).Register(ctx, owner, e); done <- err }()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}}
			}
			return r
		}}
		_, err := a.New(u).Register(ctx, owner, e)
		var duplicate *a.AlreadyRegistered
		if calls != 2 || !errors.As(err, &duplicate) || duplicate.RegistrationID != winner.ID {
			t.Fatalf("calls=%d duplicate=%+v err=%v", calls, duplicate, err)
		}
		// A unique violation must not leak a counter increment or notification.
		var count int
		err = store.Do(ctx, func(ctx context.Context, r a.RegistrationRepository) error {
			rr := r.(*repo)
			var queryErr error
			queryErr = rr.tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1`, e).Scan(&count)
			return queryErr
		})
		if err != nil || count != 1 {
			t.Fatalf("notifications=%d err=%v", count, err)
		}
	})
	t.Run("lost unique winner is a conflict without details", func(t *testing.T) {
		e, owner := event(2, 3), user()
		var winner a.Registration
		calls := 0
		u := interleavedUOW{store: store, before: func(r a.RegistrationRepository) a.RegistrationRepository {
			calls++
			if calls == 1 {
				return staleActive{RegistrationRepository: r, afterPrecheck: func() {
					var err error
					winner, err = a.New(store).Register(ctx, owner, e)
					if err != nil {
						t.Fatal(err)
					}
				}}
			}
			if _, err := a.New(store).Cancel(ctx, owner, winner.ID, ""); err != nil {
				t.Fatal(err)
			}
			return r
		}}
		_, err := a.New(u).Register(ctx, owner, e)
		var duplicate *a.AlreadyRegistered
		if calls != 2 || !errors.Is(err, a.ErrAlreadyActive) || errors.As(err, &duplicate) {
			t.Fatalf("calls=%d err=%v duplicate=%+v", calls, err, duplicate)
		}
		var registered, waiting, notifications int
		if err := pool.QueryRow(ctx, `SELECT registered_count,waitlist_count,(SELECT count(*) FROM notifications WHERE event_id=events.id) FROM events WHERE id=$1`, e).Scan(&registered, &waiting, &notifications); err != nil {
			t.Fatal(err)
		}
		if registered != 0 || waiting != 0 || notifications != 2 {
			t.Fatalf("registered=%d waiting=%d notifications=%d", registered, waiting, notifications)
		}
	})
	t.Run("DB decision is once inside snapshot before count and rows", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 2)
		at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=$2::timestamptz,end_at=$2::timestamptz+interval '1 hour',registration_opens_at=$2::timestamptz-interval '2 days',registration_closes_at=$2::timestamptz-interval '1 day' WHERE id=$1`, e, at); err != nil {
			t.Fatal(err)
		}
		decisions := 0
		u := interleavedUOW{store: store, before: func(r a.RegistrationRepository) a.RegistrationRepository {
			rr := r.(*repo)
			rr.q = db.New(decisionDB{DBTX: rr.tx, at: at, afterDecision: func() {
				decisions++
				var isolation, readOnly string
				if err := rr.tx.QueryRow(ctx, "SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')").Scan(&isolation, &readOnly); err != nil {
					t.Fatal(err)
				}
				if isolation != "repeatable read" || readOnly != "on" {
					t.Fatalf("isolation=%s readOnly=%s", isolation, readOnly)
				}
				// Once the clock statement starts the snapshot, an external
				// event edit must not alter status, filtering or event brief.
				if _, err := pool.Exec(ctx, `UPDATE events SET start_at=start_at+interval '1 day',end_at=end_at+interval '1 day' WHERE id=$1`, e); err != nil {
					t.Fatal(err)
				}
			}})
			return rr
		}}
		rows, n, err := a.New(u).Mine(ctx, regs[1].StudentID, a.MineFilter{Status: "EXPIRED", Upcoming: true, Page: 1, PageSize: 20})
		if err != nil || decisions != 1 || n != 1 || len(rows) != 1 || rows[0].Status != "EXPIRED" || rows[0].WaitlistPosition != nil || !rows[0].Event.StartAt.Equal(at) {
			t.Fatalf("decisions=%d rows=%+v n=%d err=%v", decisions, rows, n, err)
		}
	})
	t.Run("count and rows retain snapshot across committed cancellation", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 3)
		u := interleavedUOW{store: store, before: func(r a.RegistrationRepository) a.RegistrationRepository {
			rr := r.(*repo)
			rr.q = db.New(afterCountDB{DBTX: rr.tx, after: func() {
				if _, err := a.New(store).Cancel(ctx, regs[1].StudentID, regs[1].ID, ""); err != nil {
					t.Fatal(err)
				}
			}})
			return rr
		}}
		rows, n, err := a.New(u).Mine(ctx, regs[1].StudentID, a.MineFilter{Status: "WAITLISTED", Page: 1, PageSize: 20})
		if err != nil || n != 1 || len(rows) != 1 || rows[0].Status != "WAITLISTED" || rows[0].WaitlistPosition == nil || *rows[0].WaitlistPosition != 1 {
			t.Fatalf("rows=%+v n=%d err=%v", rows, n, err)
		}
		current, n, err := a.New(store).Mine(ctx, regs[1].StudentID, a.MineFilter{Status: "WAITLISTED", Page: 1, PageSize: 20})
		if err != nil || n != 0 || len(current) != 0 {
			t.Fatalf("current=%+v n=%d err=%v", current, n, err)
		}
	})
}
