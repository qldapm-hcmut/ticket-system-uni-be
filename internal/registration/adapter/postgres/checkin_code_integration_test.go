//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/qrtoken"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

// These tests catch retries in an aborted transaction, overly broad unique-error
// handling, extra counter/notification writes, and replacing legacy code validation.
func testCheckinCodeCollisions(t *testing.T, pool *pgxpool.Pool, owner string, user func() string, event func(int, int) string, counters func(string, int, int), checkQueue func(string)) {
	ctx := context.Background()
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	assertState := func(t *testing.T, e string, registered, waiting, notifications int) {
		t.Helper()
		counters(e, registered, waiting)
		var rows, notices int
		if err := pool.QueryRow(ctx, `SELECT
    (SELECT count(*) FROM registrations WHERE event_id = $1),
    (SELECT count(*) FROM notifications WHERE event_id = $1)`, e).Scan(&rows, &notices); err != nil {
			t.Fatal(err)
		}
		if rows != registered+waiting || notices != notifications {
			t.Fatalf("rows=%d notices=%d, want %d/%d", rows, notices, registered+waiting, notifications)
		}
	}
	storeWith := func(random io.Reader) *Store {
		s := New(pool)
		s.random = random
		return s
	}
	seedCode := func(t *testing.T, e string) a.Registration {
		t.Helper()
		r, err := a.New(storeWith(bytes.NewReader(bytes.Repeat([]byte{10}, 8)))).Register(ctx, user(), e)
		if err != nil || r.CheckinCode != "AAAAAAAA" {
			t.Fatalf("seed: %+v %v", r, err)
		}
		return r
	}
	pgError := func(t *testing.T, err error, constraint string) {
		t.Helper()
		var p *pgconn.PgError
		if !errors.As(err, &p) || p.Code != "23505" || p.ConstraintName != constraint {
			t.Fatalf("want 23505/%s, got %v", constraint, err)
		}
	}
	for _, capacity := range []int{1, 3} {
		t.Run(fmt.Sprintf("collision then success capacity %d", capacity), func(t *testing.T) {
			e := event(capacity, 10)
			seedCode(t, e)
			random := bytes.NewReader(append(bytes.Repeat([]byte{10}, 8), bytes.Repeat([]byte{31}, 8)...))
			service := a.New(storeWith(random))
			reg, err := service.Register(ctx, user(), e)
			if err != nil || reg.CheckinCode != "ZZZZZZZZ" || random.Len() != 0 {
				t.Fatalf("retry: %+v %v unread=%d", reg, err, random.Len())
			}
			if capacity == 1 {
				if reg.Status != "WAITLISTED" || reg.WaitlistPosition == nil || *reg.WaitlistPosition != 1 {
					t.Fatalf("waitlist result: %+v", reg)
				}
				assertState(t, e, 1, 1, 2)
				next, err := a.New(storeWith(bytes.NewReader(bytes.Repeat([]byte{11}, 8)))).Register(ctx, user(), e)
				if err != nil || next.QueueSeq <= reg.QueueSeq {
					t.Fatalf("FIFO ordering: %+v %v", next, err)
				}
				var first string
				if err := pool.QueryRow(ctx, `SELECT id::text FROM registrations WHERE event_id = $1 AND status = 'REGISTERED'`, e).Scan(&first); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Cancel(ctx, owner, first, "test promotion"); err != nil {
					t.Fatal(err)
				}
				var promoted string
				if err := pool.QueryRow(ctx, `SELECT id::text FROM registrations WHERE event_id = $1 AND status = 'REGISTERED'`, e).Scan(&promoted); err != nil || promoted != reg.ID {
					t.Fatalf("promoted=%s want=%s err=%v", promoted, reg.ID, err)
				}
				counters(e, 1, 1)
				checkQueue(e)
			} else {
				assertState(t, e, 2, 0, 2)
			}
		})
	}
	t.Run("five collisions exhaust without sixth attempt", func(t *testing.T) {
		e := event(3, 10)
		seedCode(t, e)
		random := bytes.NewReader(append(bytes.Repeat([]byte{10}, 40), bytes.Repeat([]byte{31}, 8)...))
		_, err := a.New(storeWith(random)).Register(ctx, user(), e)
		pgError(t, err, "uq_checkin_code")
		if random.Len() != 8 {
			t.Fatalf("unread=%d, want sixth candidate untouched", random.Len())
		}
		assertState(t, e, 1, 0, 1)
	})
	for _, afterCollision := range []bool{false, true} {
		t.Run(fmt.Sprintf("RNG error after collision %t", afterCollision), func(t *testing.T) {
			e := event(3, 10)
			seedCode(t, e)
			failure := errors.New("entropy unavailable")
			var random io.Reader = failedRandomReader{failure}
			if afterCollision {
				random = io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{10}, 8)), random)
			}
			_, err := a.New(storeWith(random)).Register(ctx, user(), e)
			if !errors.Is(err, failure) {
				t.Fatalf("RNG error: %v", err)
			}
			assertState(t, e, 1, 0, 1)
		})
	}
	t.Run("unrelated unique error never retries", func(t *testing.T) {
		e := event(3, 10)
		seedCode(t, e)
		exec(t, fmt.Sprintf(`CREATE UNIQUE INDEX uq_test_registration_event ON registrations (event_id) WHERE event_id = '%s'`, e))
		defer exec(t, `DROP INDEX uq_test_registration_event`)
		random := bytes.NewReader(bytes.Repeat([]byte{31}, 16))
		_, err := a.New(storeWith(random)).Register(ctx, user(), e)
		pgError(t, err, "uq_test_registration_event")
		if random.Len() != 8 {
			t.Fatalf("unexpected retry: unread=%d", random.Len())
		}
		assertState(t, e, 1, 0, 1)
	})
	t.Run("active unique error and service duplicate preserved", func(t *testing.T) {
		e := event(3, 10)
		seed := seedCode(t, e)
		random := bytes.NewReader(bytes.Repeat([]byte{31}, 16))
		store := storeWith(random)
		err := store.Do(ctx, func(ctx context.Context, r a.RegistrationRepository) error {
			_, err := r.Register(ctx, e, seed.StudentID)
			return err
		})
		if !errors.Is(err, a.ErrAlreadyActive) || random.Len() != 8 {
			t.Fatalf("active unique: %v unread=%d", err, random.Len())
		}
		_, err = a.New(store).Register(ctx, seed.StudentID, e)
		var duplicate *a.AlreadyRegistered
		if !errors.As(err, &duplicate) || duplicate.RegistrationID != seed.ID || random.Len() != 8 {
			t.Fatalf("service duplicate: %v unread=%d", err, random.Len())
		}
		assertState(t, e, 1, 0, 1)
	})
	t.Run("notification failure after retry rolls back registration and counters", func(t *testing.T) {
		e := event(3, 10)
		seedCode(t, e)
		exec(t, `CREATE FUNCTION fail_c1_notification() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'forced notification failure';
END;
$$`)
		exec(t, `CREATE TRIGGER fail_c1_notification BEFORE INSERT ON notifications
FOR EACH ROW EXECUTE FUNCTION fail_c1_notification()`)
		defer exec(t, `DROP FUNCTION fail_c1_notification() CASCADE`)
		random := bytes.NewReader(append(bytes.Repeat([]byte{10}, 8), bytes.Repeat([]byte{31}, 8)...))
		_, err := a.New(storeWith(random)).Register(ctx, user(), e)
		if err == nil || !strings.Contains(err.Error(), "forced notification failure") || random.Len() != 0 {
			t.Fatalf("notification failure: %v unread=%d", err, random.Len())
		}
		assertState(t, e, 1, 0, 1)
	})
	t.Run("cancelled code remains reserved but codes are event scoped", func(t *testing.T) {
		e := event(3, 10)
		seed := seedCode(t, e)
		if _, err := a.New(New(pool)).Cancel(ctx, owner, seed.ID, "test"); err != nil {
			t.Fatal(err)
		}
		random := bytes.NewReader(append(bytes.Repeat([]byte{10}, 8), bytes.Repeat([]byte{31}, 8)...))
		reg, err := a.New(storeWith(random)).Register(ctx, seed.StudentID, e)
		if err != nil || reg.CheckinCode != "ZZZZZZZZ" {
			t.Fatalf("cancelled collision: %+v %v", reg, err)
		}
		counters(e, 1, 0)
		other := event(3, 10)
		seedCode(t, other)
		assertState(t, other, 1, 0, 1)
	})
	for _, legacy := range []struct{ stored, formatted, input string }{
		{"ABCDEF01", "ABCD-EF01", "abcd-ef01"}, // Previous hex generator.
		{"I1LOU0AB", "I1LO-U0AB", "i1lo-u0ab"}, // Valid old data outside the new alphabet.
	} {
		t.Run("legacy code "+legacy.stored, func(t *testing.T) {
			e := event(1, 10)
			reg := seedCode(t, e)
			exec(t, `UPDATE registrations SET checkin_code = $1 WHERE id = $2`, legacy.stored, reg.ID)
			exec(t, `UPDATE events SET start_at = now() + interval '30 minutes',
    end_at = now() + interval '2 hours',
    registration_closes_at = now() + interval '10 minutes'
WHERE id = $1`, e)
			codec, err := qrtoken.New(strings.Repeat("q", 32))
			if err != nil {
				t.Fatal(err)
			}
			detail, err := a.NewDetailService(New(pool), codec).Detail(ctx, reg.StudentID, reg.ID)
			if err != nil || detail.CheckinCode == nil || *detail.CheckinCode != legacy.formatted {
				t.Fatalf("legacy format: %+v %v", detail, err)
			}
			if _, err := a.NewCheckinService(New(pool), codec).CheckIn(ctx, owner, e, a.CheckinInput{Method: "CODE", Code: legacy.input}); err != nil {
				t.Fatalf("legacy check-in: %v", err)
			}
			var stored string
			if err := pool.QueryRow(ctx, `SELECT checkin_code FROM registrations WHERE id = $1`, reg.ID).Scan(&stored); err != nil || stored != legacy.stored {
				t.Fatalf("legacy code changed: %q %v", stored, err)
			}
		})
	}
}
