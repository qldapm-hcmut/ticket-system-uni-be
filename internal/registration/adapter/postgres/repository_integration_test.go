//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/labstack/echo/v4"
	authhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/http"
	authpg "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/postgres"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/token"
	eventhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/http"
	reghttp "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/http"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	eventpg "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/postgres"
	eventapp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/qrtoken"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func TestRegistrationPersistence(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("ticket_registration_test_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema, err := os.ReadFile("../../../../db/migrations/0001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	service := a.New(New(pool))
	user := func() string {
		t.Helper()
		var id string
		err := pool.QueryRow(ctx, `INSERT INTO users(email,full_name,student_code) VALUES (gen_random_uuid()::text || '@hcmut.edu.vn','Test',substr(gen_random_uuid()::text,1,10)) RETURNING id::text`).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner := user()
	if _, err := pool.Exec(ctx, `UPDATE users SET student_code=NULL WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	var unit string
	if err := pool.QueryRow(ctx, `INSERT INTO units(code,name,type) VALUES ('TEST','Test','FACULTY') RETURNING id::text`).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	event := func(capacity, limit int) string {
		t.Helper()
		var id string
		err := pool.QueryRow(ctx, `INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,waitlist_limit,status) VALUES ($1,$2,'Test','Campus',now()+interval '3 days',now()+interval '4 days',now()-interval '1 day',now()+interval '2 days',$3,$4,'PUBLISHED') RETURNING id::text`, unit, owner, capacity, limit).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	counters := func(id string, registered, waiting int) {
		t.Helper()
		var r, w, actualR, actualW int
		if err := pool.QueryRow(ctx, `SELECT registered_count,waitlist_count,(SELECT count(*) FROM registrations WHERE event_id=events.id AND status='REGISTERED'),(SELECT count(*) FROM registrations WHERE event_id=events.id AND status='WAITLISTED') FROM events WHERE id=$1`, id).Scan(&r, &w, &actualR, &actualW); err != nil {
			t.Fatal(err)
		}
		if r != registered || w != waiting || r != actualR || w != actualW {
			t.Fatalf("counters=(%d,%d), rows=(%d,%d), want=(%d,%d)", r, w, actualR, actualW, registered, waiting)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES ($1,$2,'ORGANIZER')`, owner, unit); err != nil {
		t.Fatal(err)
	}
	es := eventapp.New(eventpg.New(pool), kernel.SystemClock{})
	patch := func(e string, capacity, version int32) (domain.Event, error) {
		return es.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, version, func(f domain.Fields) (domain.Fields, error) { f.Capacity = capacity; return f, nil })
	}
	seed := func(e string, n int) []a.Registration {
		t.Helper()
		out := make([]a.Registration, n)
		for i := range out {
			var err error
			out[i], err = service.Register(ctx, user(), e)
			if err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	checkQueue := func(e string) {
		t.Helper()
		var violations int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM registrations w JOIN registrations p ON p.event_id=w.event_id WHERE w.event_id=$1 AND w.status='WAITLISTED' AND p.status='REGISTERED' AND p.queue_seq>w.queue_seq`, e).Scan(&violations); err != nil {
			t.Fatal(err)
		}
		if violations != 0 {
			t.Fatalf("FIFO violations=%d", violations)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT user_id,type FROM notifications WHERE event_id=$1 GROUP BY user_id,type HAVING count(*)>1) duplicates`, e).Scan(&violations); err != nil {
			t.Fatal(err)
		}
		if violations != 0 {
			t.Fatalf("duplicate notifications=%d", violations)
		}
		var promoted, notified int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM registrations WHERE event_id=$1 AND promoted_at IS NOT NULL),(SELECT count(*) FROM notifications WHERE event_id=$1 AND type='PROMOTED')`, e).Scan(&promoted, &notified); err != nil {
			t.Fatal(err)
		}
		if promoted != notified {
			t.Fatalf("promotions=%d notifications=%d", promoted, notified)
		}
	}
	// A shared start barrier makes all operations contend rather than run serially.
	concurrent := func(tasks ...func() error) []error {
		start := make(chan struct{})
		results := make(chan error, len(tasks))
		var wg sync.WaitGroup
		for _, task := range tasks {
			wg.Add(1)
			go func(task func() error) { defer wg.Done(); <-start; results <- task() }(task)
		}
		close(start)
		wg.Wait()
		close(results)
		out := make([]error, 0, len(tasks))
		for err := range results {
			out = append(out, err)
		}
		return out
	}
	t.Run("C1 checkin code collisions", func(t *testing.T) {
		testCheckinCodeCollisions(t, pool, owner, user, event, counters, checkQueue)
	})
	t.Run("B4 owned registrations", func(t *testing.T) {
		testOwnedRegistrations(t, pool, user, event, seed, counters)
	})
	t.Run("C2 SQL cancellation and promotion retain pre-start decision", func(t *testing.T) {
		testCancellationDecisionBeforeStart(t, pool, owner, event, seed, counters, checkQueue)
	})
	t.Run("B3 cancellation on behalf", func(t *testing.T) {
		testCancellationOnBehalf(t, pool, unit, owner, event, seed, counters, concurrent, checkQueue)
	})
	t.Run("QR issuance ownership and checkin", func(t *testing.T) {
		codec, err := qrtoken.New(strings.Repeat("q", 32))
		if err != nil {
			t.Fatal(err)
		}
		store := New(pool)
		details := a.NewDetailService(store, codec)
		checkins := a.NewCheckinService(store, codec)
		e := event(1, 3)
		regs := seed(e, 2)
		detail, err := details.Detail(ctx, regs[0].StudentID, regs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		qr := *detail.QRToken
		waiting, err := details.Detail(ctx, regs[1].StudentID, regs[1].ID)
		if err != nil {
			t.Fatal(err)
		}
		if waiting.QRToken != nil {
			t.Fatal("waitlist received QR")
		}
		if _, err := details.Detail(ctx, regs[1].StudentID, regs[0].ID); !errors.Is(err, a.ErrNotFound) {
			t.Fatalf("ownership: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '30 minutes',end_at=now()+interval '2 hours',registration_closes_at=now()+interval '10 minutes' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		if _, err := checkins.CheckIn(ctx, owner, e, a.CheckinInput{Method: "QR", QRToken: qr + "a"}); !errors.Is(err, a.ErrInvalidQR) {
			t.Fatalf("tamper: %v", err)
		}
		waitingQR := codec.Sign(regs[1].ID, e)
		if _, err := checkins.CheckIn(ctx, owner, e, a.CheckinInput{Method: "QR", QRToken: waitingQR}); !errors.Is(err, a.ErrCheckinEligibility) {
			t.Fatalf("waitlist eligibility: %v", err)
		}
		result, err := checkins.CheckIn(ctx, owner, e, a.CheckinInput{Method: "QR", QRToken: qr})
		if err != nil {
			t.Fatal(err)
		}
		if result.Method != "QR" {
			t.Fatal("wrong method")
		}
		if err := store.UndoCheckIn(ctx, owner, regs[0].ID); err != nil {
			t.Fatal(err)
		}
		task := func() error {
			_, err := checkins.CheckIn(ctx, owner, e, a.CheckinInput{Method: "QR", QRToken: qr})
			return err
		}
		successes, duplicates := 0, 0
		for _, err := range concurrent(task, task) {
			var d *a.AlreadyCheckedIn
			if err == nil {
				successes++
			} else if errors.As(err, &d) {
				duplicates++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || duplicates != 1 {
			t.Fatalf("QR concurrency: %d %d", successes, duplicates)
		}
		var duplicate *a.AlreadyCheckedIn
		if _, err := checkins.CheckIn(ctx, owner, e, a.CheckinInput{Method: "QR", QRToken: qr}); !errors.As(err, &duplicate) {
			t.Fatalf("duplicate: %v", err)
		}
	})
	t.Run("checkin audit failures rollback", func(t *testing.T) {
		e := event(1, 2)
		reg := seed(e, 1)[0]
		store := New(pool)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '30 minutes',end_at=now()+interval '2 hours',registration_closes_at=now()+interval '10 minutes' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		install := func() {
			t.Helper()
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_checkin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_checkin_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_checkin_audit()`); err != nil {
				t.Fatal(err)
			}
		}
		remove := func() {
			t.Helper()
			if _, err := pool.Exec(ctx, `DROP TRIGGER IF EXISTS reject_checkin_audit ON activity_logs; DROP FUNCTION IF EXISTS reject_checkin_audit()`); err != nil {
				t.Fatal(err)
			}
		}
		defer remove()
		install()
		if _, err := store.CheckIn(ctx, owner, e, a.CheckinInput{Method: "MANUAL", RegistrationID: reg.ID, Note: "Card verified"}); err == nil {
			t.Fatal("expected manual audit failure")
		}
		var checked bool
		if err := pool.QueryRow(ctx, `SELECT checked_in_at IS NOT NULL FROM registrations WHERE id=$1`, reg.ID).Scan(&checked); err != nil {
			t.Fatal(err)
		}
		if checked {
			t.Fatal("manual checkin persisted")
		}
		remove()
		if _, err := store.CheckIn(ctx, owner, e, a.CheckinInput{Method: "CODE", Code: reg.CheckinCode}); err != nil {
			t.Fatal(err)
		}
		install()
		if err := store.UndoCheckIn(ctx, owner, reg.ID); err == nil {
			t.Fatal("expected undo audit failure")
		}
		if err := pool.QueryRow(ctx, `SELECT checked_in_at IS NOT NULL FROM registrations WHERE id=$1`, reg.ID).Scan(&checked); err != nil {
			t.Fatal(err)
		}
		if !checked {
			t.Fatal("undo persisted despite audit failure")
		}
	})
	t.Run("checkin service eligibility authorization and undo", func(t *testing.T) {
		e := event(1, 2)
		regs := seed(e, 2)
		store := New(pool)
		input := a.CheckinInput{Method: "MANUAL", RegistrationID: regs[0].ID, Note: "Verified student card"}
		if _, err := store.CheckIn(ctx, owner, e, input); !errors.Is(err, a.ErrCheckinWindow) {
			t.Fatalf("expected window error: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '30 minutes',end_at=now()+interval '2 hours',registration_closes_at=now()+interval '10 minutes' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CheckIn(ctx, user(), e, input); !errors.Is(err, a.ErrNotFound) {
			t.Fatalf("unauthorized: %v", err)
		}
		waiting := input
		waiting.RegistrationID = regs[1].ID
		if _, err := store.CheckIn(ctx, owner, e, waiting); !errors.Is(err, a.ErrCheckinEligibility) {
			t.Fatalf("waitlist: %v", err)
		}
		result, err := store.CheckIn(ctx, owner, e, input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Method != "MANUAL" || result.RegistrationID != regs[0].ID {
			t.Fatalf("result %+v", result)
		}
		var duplicate *a.AlreadyCheckedIn
		if _, err := store.CheckIn(ctx, owner, e, input); !errors.As(err, &duplicate) {
			t.Fatalf("duplicate: %v", err)
		}
		if err := store.UndoCheckIn(ctx, owner, regs[0].ID); err != nil {
			t.Fatal(err)
		}
		if err := store.UndoCheckIn(ctx, owner, regs[0].ID); !errors.Is(err, a.ErrNotFound) {
			t.Fatalf("second undo: %v", err)
		}
		var logs int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE event_id=$1 AND action IN ('MANUAL_CHECKIN','UNDO_CHECKIN')`, e).Scan(&logs); err != nil {
			t.Fatal(err)
		}
		if logs != 2 {
			t.Fatalf("audit count=%d", logs)
		}
		if _, err := store.CheckIn(ctx, owner, e, a.CheckinInput{Method: "CODE", Code: strings.ToLower(regs[0].CheckinCode)}); err != nil {
			t.Fatal(err)
		}
		if err := store.UndoCheckIn(ctx, owner, regs[0].ID); err != nil {
			t.Fatal(err)
		}
		task := func() error {
			_, err := store.CheckIn(ctx, owner, e, a.CheckinInput{Method: "CODE", Code: regs[0].CheckinCode})
			return err
		}
		success, duplicates := 0, 0
		for _, err := range concurrent(task, task) {
			var d *a.AlreadyCheckedIn
			if err == nil {
				success++
			} else if errors.As(err, &d) {
				duplicates++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || duplicates != 1 {
			t.Fatalf("success=%d duplicate=%d", success, duplicates)
		}
		counters(e, 1, 1)
	})
	t.Run("checkin SQL concurrent update and audited undo", func(t *testing.T) {
		e := event(1, 2)
		reg := seed(e, 1)[0]
		q := db.New(pool)
		eid, _ := uuid(e)
		rid, _ := uuid(reg.ID)
		staff, _ := uuid(owner)
		code := strings.ToLower(reg.CheckinCode[:4] + "-" + reg.CheckinCode[4:])
		found, err := q.GetRegistrationByCheckinCode(ctx, db.GetRegistrationByCheckinCodeParams{EventID: eid, Code: code})
		if err != nil || found.ID != rid {
			t.Fatalf("code lookup: %v", err)
		}
		task := func() error {
			_, err := q.CheckInRegistration(ctx, db.CheckInRegistrationParams{RegistrationID: rid, EventID: eid, StaffID: staff, Method: db.CheckinMethodCODE})
			return err
		}
		success, duplicate := 0, 0
		for _, err := range concurrent(task, task) {
			if err == nil {
				success++
			} else if errors.Is(err, pgx.ErrNoRows) {
				duplicate++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || duplicate != 1 {
			t.Fatalf("success=%d duplicate=%d", success, duplicate)
		}
		undone, err := q.UndoRegistrationCheckin(ctx, db.UndoRegistrationCheckinParams{RegistrationID: rid, ActorID: staff})
		if err != nil {
			t.Fatal(err)
		}
		if undone.CheckedInAt.Valid || undone.CheckedInBy.Valid || undone.CheckinMethod.Valid || undone.CheckinNote.Valid {
			t.Fatal("checkin fields not cleared")
		}
		if _, err := q.UndoRegistrationCheckin(ctx, db.UndoRegistrationCheckinParams{RegistrationID: rid, ActorID: staff}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("duplicate undo: %v", err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE event_id=$1 AND action='UNDO_CHECKIN' AND metadata->>'checkedInAt' IS NOT NULL`, e).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit count=%d", count)
		}
		counters(e, 1, 0)
	})
	t.Run("significant change failures roll back all writes", func(t *testing.T) {
		for _, target := range []string{"notifications", "activity_logs"} {
			t.Run(target, func(t *testing.T) {
				e := event(1, 5)
				regs := seed(e, 2)
				if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '12 hours',end_at=now()+interval '13 hours',registration_closes_at=now()+interval '1 hour' WHERE id=$1`, e); err != nil {
					t.Fatal(err)
				}
				var before string
				snapshot := func() string {
					t.Helper()
					var out string
					if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('event',to_jsonb(e),'registrations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM registrations r WHERE r.event_id=e.id),'notifications',(SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM notifications n WHERE n.event_id=e.id),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM activity_logs a WHERE a.event_id=e.id))::text FROM events e WHERE e.id=$1`, e).Scan(&out); err != nil {
						t.Fatal(err)
					}
					return out
				}
				before = snapshot()
				condition := ""
				if target == "notifications" {
					condition = "IF NEW.type <> 'EVENT_CHANGED' THEN RETURN NEW; END IF;"
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+condition+` RAISE EXCEPTION 'injected significant change failure'; END $$; CREATE TRIGGER reject_change BEFORE INSERT ON `+target+` FOR EACH ROW EXECUTE FUNCTION reject_change()`); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := pool.Exec(ctx, `DROP TRIGGER reject_change ON `+target+`; DROP FUNCTION reject_change()`); err != nil {
						t.Error(err)
					}
				}()
				_, err := es.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, 1, func(f domain.Fields) (domain.Fields, error) {
					f.Location = "Rollback campus"
					f.Capacity = 2
					return f, nil
				})
				if err == nil {
					t.Fatal("expected injected failure")
				}
				if after := snapshot(); after != before {
					t.Fatalf("partial changes persisted\nbefore=%s\nafter=%s", before, after)
				}
				counters(e, 1, 1)
				if _, err := service.Cancel(ctx, regs[0].StudentID, regs[0].ID, ""); !errors.Is(err, a.ErrCancelDeadline) {
					t.Fatalf("exception leaked: %v", err)
				}
			})
		}
	})
	t.Run("time boundaries with fixed application clock", func(t *testing.T) {
		for _, tc := range []struct {
			name           string
			until          time.Duration
			blocked, grant bool
		}{
			{"exact 60 minutes", time.Hour, true, false}, {"just before cutoff", time.Hour + time.Microsecond, false, true}, {"exact 24 hours", 24 * time.Hour, false, true}, {"before 24 hour window", 24*time.Hour + time.Microsecond, false, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := event(1, 5)
				regs := seed(e, 2)
				now := time.Now().UTC().Truncate(time.Microsecond)
				start := now.Add(tc.until)
				if _, err := pool.Exec(ctx, `UPDATE events SET start_at=$2,end_at=$2::timestamptz+interval '1 hour',registration_closes_at=$3 WHERE id=$1`, e, start, now.Add(30*time.Minute)); err != nil {
					t.Fatal(err)
				}
				fixedService := eventapp.New(eventpg.New(pool), fixedEventClock{now})
				result, err := fixedService.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, 1, func(f domain.Fields) (domain.Fields, error) { f.Location = "Boundary campus"; return f, nil })
				if tc.blocked {
					if !errors.Is(err, domain.ErrState) {
						t.Fatalf("expected cutoff: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.NotifiedCount != 2 {
					t.Fatalf("notified=%d", result.NotifiedCount)
				}
				for i, r := range regs {
					var exception *time.Time
					if err := pool.QueryRow(ctx, `SELECT cancellation_exception_until FROM registrations WHERE id=$1`, r.ID).Scan(&exception); err != nil {
						t.Fatal(err)
					}
					want := tc.grant && i == 0
					if (exception != nil) != want {
						t.Fatalf("student %d exception=%v want=%v", i, exception, want)
					}
				}
			})
		}
	})
	t.Run("repeated edits preserve only existing exceptions", func(t *testing.T) {
		e := event(3, 5)
		regs := seed(e, 1)
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=$2,end_at=$2::timestamptz+interval '1 hour',registration_closes_at=$3 WHERE id=$1`, e, now.Add(12*time.Hour), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		fixedService := eventapp.New(eventpg.New(pool), fixedEventClock{now})
		edit := func(version int32, hours int) error {
			_, err := fixedService.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, version, func(f domain.Fields) (domain.Fields, error) {
				f.StartAt = now.Add(time.Duration(hours) * time.Hour)
				f.EndAt = f.StartAt.Add(time.Hour)
				return f, nil
			})
			return err
		}
		if err := edit(1, 48); err != nil {
			t.Fatal(err)
		}
		late, err := service.Register(ctx, user(), e)
		if err != nil {
			t.Fatal(err)
		}
		if err := edit(2, 72); err != nil {
			t.Fatal(err)
		}
		var exception *time.Time
		if err := pool.QueryRow(ctx, `SELECT cancellation_exception_until FROM registrations WHERE id=$1`, regs[0].ID).Scan(&exception); err != nil {
			t.Fatal(err)
		}
		if exception == nil || !exception.Equal(now.Add(72*time.Hour)) {
			t.Fatalf("exception not moved: %v", exception)
		}
		if err := pool.QueryRow(ctx, `SELECT cancellation_exception_until FROM registrations WHERE id=$1`, late.ID).Scan(&exception); err != nil {
			t.Fatal(err)
		}
		if exception != nil {
			t.Fatalf("late registrant inherited exception: %v", exception)
		}
	})
	t.Run("real HTTP published changes and cancellation", func(t *testing.T) {
		tokens, err := token.New("integration-only-signing-key-at-least-32-bytes", kernel.SystemClock{})
		if err != nil {
			t.Fatal(err)
		}
		authHandler := authhttp.New(nil, tokens, authpg.NewProfileReader(pool))
		router := echo.New()
		eventhttp.New(es, eventpg.NewReader(pool, kernel.SystemClock{})).Register(router, authHandler.Authenticate)
		reghttp.New(service).Register(router, authHandler.Authenticate)
		codec, err := qrtoken.New(strings.Repeat("h", 32))
		if err != nil {
			t.Fatal(err)
		}
		registrationStore := New(pool)
		reghttp.NewDetail(a.NewDetailService(registrationStore, codec)).Register(router, authHandler.Authenticate)
		reghttp.NewCheckin(a.NewCheckinService(registrationStore, codec)).Register(router, authHandler.Authenticate)
		server := httptest.NewServer(router)
		defer server.Close()
		client := &http.Client{Timeout: 10 * time.Second}
		request := func(method, path, body, actor string, version int, want int) map[string]any {
			t.Helper()
			req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if actor != "" {
				access, err := tokens.AccessToken(actor, time.Now().Add(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+access)
			}
			if version > 0 {
				req.Header.Set("If-Match", fmt.Sprint(version))
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s %s -> %d %s", method, path, response.StatusCode, strings.TrimSpace(string(raw)))
			if response.StatusCode != want {
				t.Fatalf("want HTTP %d", want)
			}
			var out map[string]any
			if response.StatusCode == 204 {
				return out
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		e := event(1, 5)
		s1, s2 := user(), user()
		if _, err := pool.Exec(ctx, `UPDATE users SET student_code=CASE WHEN id=$1 THEN 'HTTP001' ELSE 'HTTP002' END WHERE id IN ($1,$2)`, s1, s2); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/events/" + e
		request("PATCH", path, `{"capacity":2}`, "", 1, 401)
		request("PATCH", path, `{"capacity":2}`, s1, 1, 403)
		first := request("POST", path+"/registrations", `{}`, s1, 0, 201)["data"].(map[string]any)
		second := request("POST", path+"/registrations", `{}`, s2, 0, 201)["data"].(map[string]any)
		registrationPath := "/api/v1/registrations/" + first["id"].(string)
		detail := request("GET", registrationPath, "", s1, 0, 200)["data"].(map[string]any)
		qr := detail["qrToken"].(string)
		request("GET", registrationPath, "", s2, 0, 404)
		waitingDetail := request("GET", "/api/v1/registrations/"+second["id"].(string), "", s2, 0, 200)["data"].(map[string]any)
		if waitingDetail["qrToken"] != nil || waitingDetail["waitlistPosition"] != float64(1) {
			t.Fatalf("waitlist detail %+v", waitingDetail)
		}
		if second["status"] != "WAITLISTED" {
			t.Fatal("expected waitlist")
		}
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '12 hours',end_at=now()+interval '13 hours',registration_closes_at=now()+interval '1 hour' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		request("PATCH", path, `{"location":"Updated campus"}`, owner, 0, 428)
		out := request("PATCH", path, `{"location":"Updated campus","capacity":2}`, owner, 1, 200)
		meta := out["meta"].(map[string]any)
		if meta["significantChange"] != true || meta["notifiedCount"] != float64(2) || meta["promotedCount"] != float64(1) {
			t.Fatalf("unexpected meta %+v", meta)
		}
		request("PATCH", path, `{"capacity":3}`, owner, 1, 409)
		deadline := request("POST", "/api/v1/registrations/"+second["id"].(string)+"/cancel", `{}`, s2, 0, 422)
		if deadline["error"].(map[string]any)["code"] != "CANCEL_DEADLINE_PASSED" {
			t.Fatal("wrong deadline code")
		}
		request("POST", "/api/v1/registrations/"+first["id"].(string)+"/cancel", `{}`, s1, 0, 200)
		counters(e, 1, 0)
		request("GET", path, "", owner, 0, 200)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '30 minutes',end_at=now()+interval '2 hours',registration_closes_at=now()+interval '10 minutes' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		admin := user()
		if _, err := pool.Exec(ctx, `UPDATE users SET is_admin=true WHERE id=$1`, admin); err != nil {
			t.Fatal(err)
		}
		outsider := user()
		var otherUnit string
		if err := pool.QueryRow(ctx, `INSERT INTO units(code,name,type) VALUES ('OTHER','Other','FACULTY') RETURNING id::text`).Scan(&otherUnit); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES ($1,$2,'ORGANIZER')`, outsider, otherUnit); err != nil {
			t.Fatal(err)
		}
		checkPath := path + "/check-ins"
		qrBody := func(value string) string {
			raw, _ := json.Marshal(map[string]string{"method": "QR", "qrToken": value})
			return string(raw)
		}
		request("POST", checkPath, qrBody(qr+"a"), owner, 0, 422)
		otherEvent := event(1, 2)
		request("POST", "/api/v1/events/"+otherEvent+"/check-ins", qrBody(qr), owner, 0, 422)
		// The original registration was cancelled earlier; a valid signature cannot restore eligibility.
		request("POST", checkPath, qrBody(qr), owner, 0, 422)
		promotedDetail := request("GET", "/api/v1/registrations/"+second["id"].(string), "", s2, 0, 200)["data"].(map[string]any)
		promotedQR := promotedDetail["qrToken"].(string)
		request("POST", checkPath, qrBody(promotedQR), owner, 0, 201)
		request("POST", checkPath, qrBody(promotedQR), owner, 0, 409)
		request("DELETE", "/api/v1/registrations/"+second["id"].(string)+"/check-in", "", owner, 0, 204)
		rid := second["id"].(string)
		body := `{"method":"MANUAL","registrationId":"` + rid + `","note":"Verified card"}`
		request("POST", checkPath, body, admin, 0, 403)
		request("POST", checkPath, body, outsider, 0, 404)
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`","note":"Verified card"}`, "", 0, 401)
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`","note":"Verified card"}`, s2, 0, 403)
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`"}`, owner, 0, 400)
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`","note":"Verified card"}`, owner, 0, 201)
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`","note":"Verified card"}`, owner, 0, 409)
		undoPath := "/api/v1/registrations/" + rid + "/check-in"
		request("DELETE", undoPath, "", owner, 0, 204)
		request("DELETE", undoPath, "", owner, 0, 404)
		if _, err := pool.Exec(ctx, `INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES ($1,$2,'ORGANIZER')`, s2, unit); err != nil {
			t.Fatal(err)
		}
		request("POST", checkPath, `{"method":"MANUAL","registrationId":"`+rid+`","note":"Self"}`, s2, 0, 403)
		request("DELETE", undoPath, "", s2, 0, 403)
	})
	t.Run("published significant changes and cancellation exceptions", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 2)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '12 hours',end_at=now()+interval '13 hours',registration_closes_at=now()+interval '1 hour' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		result, err := es.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, 1, func(f domain.Fields) (domain.Fields, error) { f.Location = "New campus"; f.Capacity = 2; return f, nil })
		if err != nil {
			t.Fatal(err)
		}
		if !result.SignificantChange || result.NotifiedCount != 2 || result.PromotedCount != 1 {
			t.Fatalf("unexpected result: %+v", result)
		}
		var exception *time.Time
		if err := pool.QueryRow(ctx, `SELECT cancellation_exception_until FROM registrations WHERE id=$1`, regs[0].ID).Scan(&exception); err != nil {
			t.Fatal(err)
		}
		if exception == nil {
			t.Fatal("existing registered user missing exception")
		}
		if err := pool.QueryRow(ctx, `SELECT cancellation_exception_until FROM registrations WHERE id=$1`, regs[1].ID).Scan(&exception); err != nil {
			t.Fatal(err)
		}
		if exception != nil {
			t.Fatal("promoted user received exception")
		}
		if _, err := service.Cancel(ctx, regs[1].StudentID, regs[1].ID, ""); !errors.Is(err, a.ErrCancelDeadline) {
			t.Fatalf("unexpected promoted cancellation: %v", err)
		}
		if _, err := service.Cancel(ctx, regs[0].StudentID, regs[0].ID, ""); err != nil {
			t.Fatal(err)
		}
		var logs int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE event_id=$1 AND action='UPDATE_EVENT' AND metadata->>'isSignificant'='true'`, e).Scan(&logs); err != nil {
			t.Fatal(err)
		}
		if logs != 1 {
			t.Fatalf("audit count %d", logs)
		}
	})
	t.Run("published significant change cutoff", func(t *testing.T) {
		e := event(1, 1)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()+interval '30 minutes',end_at=now()+interval '2 hours',registration_closes_at=now()+interval '10 minutes' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		_, err := es.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, 1, func(f domain.Fields) (domain.Fields, error) { f.Location = "Blocked"; return f, nil })
		if !errors.Is(err, domain.ErrState) {
			t.Fatalf("expected cutoff rejection: %v", err)
		}
	})
	t.Run("capacity exceeds queue and lower boundary", func(t *testing.T) {
		e := event(1, 5)
		seed(e, 3)
		result, err := patch(e, 10, 1)
		if err != nil {
			t.Fatal(err)
		}
		if result.PromotedCount != 2 {
			t.Fatalf("promoted=%d", result.PromotedCount)
		}
		counters(e, 3, 0)
		result, err = patch(e, 3, 2)
		if err != nil {
			t.Fatal(err)
		}
		if result.PromotedCount != 0 {
			t.Fatal("decrease promoted waitlist")
		}
		if _, err = patch(e, 2, 3); !errors.Is(err, domain.ErrCapacity) {
			t.Fatalf("expected capacity error: %v", err)
		}
		counters(e, 3, 0)
		checkQueue(e)
	})
	t.Run("started event cannot promote", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 2)
		if _, err := pool.Exec(ctx, `UPDATE events SET start_at=now()-interval '1 hour',end_at=now()+interval '1 hour',registration_opens_at=now()-interval '3 days',registration_closes_at=now()-interval '2 hours' WHERE id=$1`, e); err != nil {
			t.Fatal(err)
		}
		if _, err := patch(e, 2, 1); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("expected past-start validation: %v", err)
		}
		if _, err := service.Cancel(ctx, regs[0].StudentID, regs[0].ID, ""); !errors.Is(err, a.ErrCancelDeadline) {
			t.Fatalf("expected deadline: %v", err)
		}
		counters(e, 1, 1)
		checkQueue(e)
	})
	t.Run("concurrent patches same version", func(t *testing.T) {
		e := event(1, 10)
		seed(e, 8)
		results := concurrent(func() error { _, err := patch(e, 3, 1); return err }, func() error { _, err := patch(e, 3, 1); return err })
		success, conflict := 0, 0
		for _, err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, domain.ErrVersion) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("success=%d conflict=%d", success, conflict)
		}
		counters(e, 3, 5)
		checkQueue(e)
	})
	t.Run("concurrent cancellations", func(t *testing.T) {
		e := event(4, 20)
		regs := seed(e, 14)
		tasks := make([]func() error, 4)
		for i := range tasks {
			r := regs[i]
			tasks[i] = func() error { _, err := service.Cancel(ctx, r.StudentID, r.ID, ""); return err }
		}
		for _, err := range concurrent(tasks...) {
			if err != nil {
				t.Fatal(err)
			}
		}
		counters(e, 4, 6)
		checkQueue(e)
	})
	t.Run("capacity registration cancellation race", func(t *testing.T) {
		for iteration := 0; iteration < 5; iteration++ {
			e := event(2, 30)
			regs := seed(e, 12)
			newStudent := user()
			tasks := []func() error{
				func() error { _, err := patch(e, 5, 1); return err },
				func() error { _, err := service.Register(ctx, newStudent, e); return err },
				func() error { _, err := service.Cancel(ctx, regs[0].StudentID, regs[0].ID, ""); return err },
				func() error { _, err := service.Cancel(ctx, regs[1].StudentID, regs[1].ID, ""); return err },
			}
			for _, err := range concurrent(tasks...) {
				if err != nil {
					t.Fatal(err)
				}
			}
			counters(e, 5, 6)
			checkQueue(e)
		}
	})
	t.Run("PROMOTED notification failure rolls back cancellation", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 3)
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_promoted() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='PROMOTED' THEN RAISE EXCEPTION 'injected promotion failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_promoted BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION reject_promoted()`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_promoted ON notifications; DROP FUNCTION reject_promoted()`); err != nil {
				t.Error(err)
			}
		}()
		if _, err := service.Cancel(ctx, regs[0].StudentID, regs[0].ID, ""); err == nil {
			t.Fatal("expected promotion notification failure")
		}
		counters(e, 1, 2)
		checkQueue(e)
		var status string
		var cancelled, promoted *time.Time
		if err := pool.QueryRow(ctx, `SELECT status,cancelled_at,promoted_at FROM registrations WHERE id=$1`, regs[0].ID).Scan(&status, &cancelled, &promoted); err != nil {
			t.Fatal(err)
		}
		if status != "REGISTERED" || cancelled != nil || promoted != nil {
			t.Fatalf("cancellation persisted: %s %v %v", status, cancelled, promoted)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM registrations WHERE event_id=$1 AND promoted_at IS NOT NULL`, e).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("promotion timestamp persisted")
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1 AND type IN ('PROMOTED','REG_CANCELLED')`, e).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("transaction notifications persisted")
		}
	})
	t.Run("capacity promotion FIFO and notification rollback", func(t *testing.T) {
		e := event(1, 5)
		if _, err := pool.Exec(ctx, `INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES ($1,$2,'ORGANIZER') ON CONFLICT DO NOTHING`, owner, unit); err != nil {
			t.Fatal(err)
		}
		students := []string{user(), user(), user(), user()}
		for _, s := range students {
			if _, err := service.Register(ctx, s, e); err != nil {
				t.Fatal(err)
			}
		}
		es := eventapp.New(eventpg.New(pool), kernel.SystemClock{})
		patch := func(capacity int32, version int32) error {
			_, err := es.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, e, version, func(f domain.Fields) (domain.Fields, error) { f.Capacity = capacity; return f, nil })
			return err
		}
		if err := patch(3, 1); err != nil {
			t.Fatal(err)
		}
		counters(e, 3, 1)
		for i, s := range students {
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM registrations WHERE event_id=$1 AND student_id=$2`, e, s).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "REGISTERED"
			if i == 3 {
				want = "WAITLISTED"
			}
			if status != want {
				t.Fatalf("student %d: %s", i, status)
			}
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event_id=$1 AND type='PROMOTED'`, e).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("promoted notifications=%d", n)
		}
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected notification failure'; END $$; CREATE TRIGGER reject_notification BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION reject_notification()`); err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `DROP TRIGGER reject_notification ON notifications; DROP FUNCTION reject_notification()`)
		if err := patch(4, 2); err == nil {
			t.Fatal("expected notification failure")
		}
		counters(e, 3, 1)
		var capacity, version int
		if err := pool.QueryRow(ctx, `SELECT capacity,version FROM events WHERE id=$1`, e).Scan(&capacity, &version); err != nil {
			t.Fatal(err)
		}
		if capacity != 3 || version != 2 {
			t.Fatalf("update not rolled back: %d %d", capacity, version)
		}
		fresh := event(1, 1)
		if _, err := service.Register(ctx, user(), fresh); err == nil {
			t.Fatal("expected registration notification failure")
		}
		counters(fresh, 0, 0)
		var regID string
		if err := pool.QueryRow(ctx, `SELECT id::text FROM registrations WHERE event_id=$1 AND student_id=$2`, e, students[0]).Scan(&regID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Cancel(ctx, students[0], regID, ""); err == nil {
			t.Fatal("expected cancellation notification failure")
		}
		counters(e, 3, 1)
	})
	t.Run("FIFO promotion and cancellation", func(t *testing.T) {
		e := event(1, 3)
		students := []string{user(), user(), user()}
		regs := make([]a.Registration, 3)
		for i, s := range students {
			var err error
			regs[i], err = service.Register(ctx, s, e)
			if err != nil {
				t.Fatal(err)
			}
		}
		if regs[0].Status != "REGISTERED" || regs[1].Status != "WAITLISTED" || regs[2].Status != "WAITLISTED" || regs[1].QueueSeq >= regs[2].QueueSeq {
			t.Fatalf("unexpected queue: %+v", regs)
		}
		counters(e, 1, 2)
		for i := 0; i < 2; i++ {
			result, err := service.Cancel(ctx, students[i], regs[i].ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Registration.Status != "CANCELLED" || result.Promoted == nil || result.Promoted.ID != regs[i+1].ID || result.Promoted.Status != "REGISTERED" {
				t.Fatalf("unexpected promotion: %+v", result)
			}
			counters(e, 1, 1-i)
		}
	})
	t.Run("waitlist cancellation does not promote", func(t *testing.T) {
		e := event(1, 2)
		s1, s2 := user(), user()
		if _, err := service.Register(ctx, s1, e); err != nil {
			t.Fatal(err)
		}
		r, err := service.Register(ctx, s2, e)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Cancel(ctx, s2, r.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Promoted != nil {
			t.Fatal("waitlist cancellation promoted someone")
		}
		counters(e, 1, 0)
	})
	t.Run("concurrent registrations respect capacity and waitlist limit", func(t *testing.T) {
		e := event(3, 5)
		students := make([]string, 24)
		for i := range students {
			students[i] = user()
		}
		results := make(chan error, len(students))
		var wg sync.WaitGroup
		for _, s := range students {
			wg.Add(1)
			go func(s string) { defer wg.Done(); _, err := service.Register(ctx, s, e); results <- err }(s)
		}
		wg.Wait()
		close(results)
		success, full := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, a.ErrFull) {
				full++
			} else {
				t.Error(err)
			}
		}
		if success != 8 || full != 16 {
			t.Fatalf("success=%d full=%d", success, full)
		}
		counters(e, 3, 5)
	})
	t.Run("concurrent duplicate registration", func(t *testing.T) {
		e, s := event(2, 2), user()
		results := make(chan error, 12)
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := service.Register(ctx, s, e); results <- err }()
		}
		wg.Wait()
		close(results)
		success, duplicate := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, a.ErrAlreadyActive) {
				duplicate++
			} else {
				t.Error(err)
			}
		}
		if success != 1 || duplicate != 11 {
			t.Fatalf("success=%d duplicate=%d", success, duplicate)
		}
		counters(e, 1, 0)
	})
}

type fixedEventClock struct{ value time.Time }

func (c fixedEventClock) Now() time.Time { return c.value }
