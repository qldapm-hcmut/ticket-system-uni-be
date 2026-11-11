//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

func testOwnedRegistrations(t *testing.T, pool *pgxpool.Pool, user func() string, event func(int, int) string, seed func(string, int) []a.Registration, counters func(string, int, int)) {
	ctx := context.Background()
	testMineInterleavings(t, New(pool), pool, ctx, event, seed, user)
	at := time.Now().UTC().Truncate(time.Microsecond)
	svc := a.New(mineAt(New(pool), at))
	read := func(owner, status string, upcoming bool, page, size int32) ([]a.MyRegistration, int64) {
		t.Helper()
		rows, n, err := svc.Mine(ctx, owner, a.MineFilter{Status: status, Upcoming: upcoming, Page: page, PageSize: size})
		if err != nil {
			t.Fatal(err)
		}
		return rows, n
	}
	t.Run("global rank and pagination with other owners and gaps", func(t *testing.T) {
		e := event(1, 10)
		regs := seed(e, 4)
		if regs[1].WaitlistPosition == nil || *regs[1].WaitlistPosition != 1 || regs[3].WaitlistPosition == nil || *regs[3].WaitlistPosition != 3 {
			t.Fatalf("R1 positions: %+v", regs)
		}
		if _, err := a.New(New(pool)).Cancel(ctx, regs[1].StudentID, regs[1].ID, ""); err != nil {
			t.Fatal(err)
		}
		history, total := read(regs[1].StudentID, "CANCELLED", false, 1, 20)
		if total != 1 || len(history) != 1 || history[0].CancelledAt == nil || history[0].CancelledAt.IsZero() || history[0].CancelledBy == nil || *history[0].CancelledBy != "STUDENT" || history[0].WaitlistPosition != nil {
			t.Fatalf("cancelled history=%+v total=%d", history, total)
		}
		rows, n := read(regs[3].StudentID, "WAITLISTED", false, 1, 1)
		if n != 1 || len(rows) != 1 || rows[0].WaitlistPosition == nil || *rows[0].WaitlistPosition != 2 {
			t.Fatalf("rows=%+v count=%d", rows, n)
		}
		if rows[0].Event.ID != e || rows[0].Event.Title != "Test" || rows[0].Event.Location != "Campus" || rows[0].Event.EndAt.IsZero() || rows[0].Event.Status != "PUBLISHED" {
			t.Fatalf("event=%+v", rows[0].Event)
		}
		rows, n = read(regs[3].StudentID, "WAITLISTED", false, 2, 1)
		if n != 1 || len(rows) != 0 || rows == nil {
			t.Fatalf("empty page rows=%+v count=%d", rows, n)
		}
		rows, n = read(user(), "", false, 1, 20)
		if n != 0 || len(rows) != 0 || rows == nil {
			t.Fatalf("empty owner rows=%v count=%d", rows, n)
		}
		rows, n = read(regs[0].StudentID, "", false, 1, 20)
		if n != 1 || len(rows) != 1 || rows[0].WaitlistPosition != nil {
			t.Fatalf("nonwaiting rank=%+v", rows)
		}
	})
	t.Run("upcoming end boundary and effective expiry start boundary", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 2)
		setTimes := func(start, end time.Time) {
			t.Helper()
			if _, err := pool.Exec(ctx, `UPDATE events SET start_at=$2::timestamptz,end_at=$3::timestamptz,registration_opens_at=$2::timestamptz-interval '2 days',registration_closes_at=$2::timestamptz-interval '1 day' WHERE id=$1`, e, start, end); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			name       string
			start, end time.Time
			status     string
			upcoming   int64
			rank       bool
		}{
			{"before start", at.Add(time.Microsecond), at.Add(time.Hour), "WAITLISTED", 1, true},
			{"at start", at, at.Add(time.Hour), "EXPIRED", 1, false},
			{"ongoing", at.Add(-time.Hour), at.Add(time.Microsecond), "EXPIRED", 1, false},
			{"at end", at.Add(-time.Hour), at, "EXPIRED", 0, false},
			{"after end", at.Add(-time.Hour), at.Add(-time.Microsecond), "EXPIRED", 0, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				setTimes(tc.start, tc.end)
				rows, n := read(regs[1].StudentID, tc.status, false, 1, 20)
				if n != 1 || len(rows) != 1 || rows[0].Status != tc.status || (rows[0].WaitlistPosition != nil) != tc.rank {
					t.Fatalf("rows=%+v count=%d", rows, n)
				}
				_, n = read(regs[1].StudentID, "", true, 1, 20)
				if n != tc.upcoming {
					t.Fatalf("upcoming=%d want=%d", n, tc.upcoming)
				}
				other := "WAITLISTED"
				if tc.status == other {
					other = "EXPIRED"
				}
				_, n = read(regs[1].StudentID, other, false, 1, 20)
				if n != 0 {
					t.Fatalf("opposite status count=%d", n)
				}
			})
		}
	})
	t.Run("pages order by created time then ID and retain distinct global ranks", func(t *testing.T) {
		owner := user()
		regs := make([]a.Registration, 3)
		for i := range regs {
			e := event(1, 10)
			seed(e, i+2)
			var err error
			regs[i], err = a.New(New(pool)).Register(ctx, owner, e)
			if err != nil {
				t.Fatal(err)
			}
		}
		// Deliberately reverse insertion order for the two timestamps.
		for i, reg := range regs {
			created := at.Add(time.Hour)
			if i == 2 {
				created = at
			}
			if _, err := pool.Exec(ctx, `UPDATE registrations SET created_at=$2 WHERE id=$1`, reg.ID, created); err != nil {
				t.Fatal(err)
			}
		}
		order := []int{0, 1, 2}
		if regs[0].ID < regs[1].ID {
			order[0], order[1] = 1, 0
		}
		for page, i := range order {
			rows, n := read(owner, "WAITLISTED", false, int32(page+1), 1)
			wantRank := int64(i + 2)
			if n != 3 || len(rows) != 1 || rows[0].ID != regs[i].ID || rows[0].Event.ID != regs[i].EventID || rows[0].WaitlistPosition == nil || *rows[0].WaitlistPosition != wantRank {
				t.Fatalf("page=%d want ID=%s rank=%d rows=%+v n=%d", page+1, regs[i].ID, wantRank, rows, n)
			}
		}
	})
	t.Run("R4 effective expiry and rank agree with R3 without widening ownership", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 4)
		if _, err := a.New(New(pool)).Cancel(ctx, regs[1].StudentID, regs[1].ID, ""); err != nil {
			t.Fatal(err)
		}
		store := New(pool)
		for _, tc := range []struct {
			name, offset, status string
			rank                 bool
		}{
			{"before start", "1 hour", "WAITLISTED", true},
			{"at or after start", "0 seconds", "EXPIRED", false},
			{"after start", "-1 hour", "EXPIRED", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := pool.Exec(ctx, `UPDATE events SET start_at=clock_timestamp()+$2::interval,end_at=clock_timestamp()+interval '2 hours',registration_opens_at=clock_timestamp()-interval '2 days',registration_closes_at=clock_timestamp()-interval '1 day' WHERE id=$1`, e, tc.offset); err != nil {
					t.Fatal(err)
				}
				v, err := store.OwnedRegistration(ctx, regs[3].StudentID, regs[3].ID)
				if err != nil || v.Status != tc.status || (v.WaitlistPosition != nil) != tc.rank || tc.rank && *v.WaitlistPosition != 2 {
					t.Fatalf("detail=%+v err=%v", v, err)
				}
				rows, n, err := a.New(store).Mine(ctx, regs[3].StudentID, a.MineFilter{Page: 1, PageSize: 20})
				if err != nil || n != 1 || len(rows) != 1 || rows[0].Status != v.Status || (rows[0].WaitlistPosition != nil) != tc.rank {
					t.Fatalf("mine=%+v n=%d err=%v", rows, n, err)
				}
				if _, err := store.OwnedRegistration(ctx, regs[0].StudentID, regs[3].ID); !errors.Is(err, a.ErrNotFound) {
					t.Fatalf("ownership: %v", err)
				}
			})
		}
	})
	t.Run("all persisted history statuses count and filter", func(t *testing.T) {
		for _, status := range []string{"REGISTERED", "WAITLISTED", "CANCELLED", "CANCELLED_BY_EVENT", "EXPIRED"} {
			t.Run(status, func(t *testing.T) {
				e := event(1, 5)
				reg := seed(e, 2)[1]
				// Fixtures exercise historical states without adding production transitions.
				if _, err := pool.Exec(ctx, `UPDATE registrations SET status=$2::registration_status,cancelled_at=CASE WHEN $2::registration_status IN ('CANCELLED','CANCELLED_BY_EVENT') THEN now() ELSE NULL END WHERE id=$1`, reg.ID, status); err != nil {
					t.Fatal(err)
				}
				rows, n := read(reg.StudentID, status, false, 1, 20)
				if n != 1 || len(rows) != 1 || rows[0].Status != status || (rows[0].WaitlistPosition != nil) != (status == "WAITLISTED") {
					t.Fatalf("rows=%+v n=%d", rows, n)
				}
			})
		}
	})
	t.Run("event lock serializes duplicate precheck and includes winner ID", func(t *testing.T) {
		e, owner := event(1, 3), user()
		svc := a.New(New(pool))
		start := make(chan struct{})
		var wg sync.WaitGroup
		type result struct {
			r   a.Registration
			err error
		}
		results := make(chan result, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; r, err := svc.Register(ctx, owner, e); results <- result{r, err} }()
		}
		close(start)
		wg.Wait()
		close(results)
		winner, duplicate := "", ""
		for x := range results {
			if x.err == nil {
				winner = x.r.ID
			} else {
				var d *a.AlreadyRegistered
				if !errors.As(x.err, &d) {
					t.Fatal(x.err)
				}
				duplicate = d.RegistrationID
			}
		}
		if winner == "" || duplicate != winner {
			t.Fatalf("winner=%s duplicate=%s", winner, duplicate)
		}
		counters(e, 1, 0)
	})
}
