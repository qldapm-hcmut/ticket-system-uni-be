//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	eventhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

// Hold a completed mutation before commit, while its event lock is still held.
// The competing use case must wait and then re-read committed scope/state.
type deletionGate struct {
	*Store
	entered chan struct{}
	release chan struct{}
}

func (g *deletionGate) wait(ctx context.Context, err error) error {
	if err != nil {
		return err
	}
	close(g.entered)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *deletionGate) Do(ctx context.Context, fn func(context.Context, a.EventRepository) error) error {
	return g.Store.Do(ctx, func(ctx context.Context, r a.EventRepository) error {
		return g.wait(ctx, fn(ctx, r))
	})
}
func (g *deletionGate) DoApproval(ctx context.Context, fn func(a.ApprovalRepository) error) error {
	return g.Store.DoApproval(ctx, func(r a.ApprovalRepository) error {
		return g.wait(ctx, fn(r))
	})
}

func TestDraftDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("ticket_delete_test_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var result string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	schema, err := os.ReadFile("../../../../db/migrations/0001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(schema))
	user := func() string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn','Delete test') RETURNING id::text`)
	}
	creator, colleague, outsider, adminOnly := user(), user(), user(), user()
	unit := scalar(`INSERT INTO units(code,name,type) VALUES('DELETE','Delete','FACULTY') RETURNING id::text`)
	otherUnit := scalar(`INSERT INTO units(code,name,type) VALUES('OTHER','Other','CLUB') RETURNING id::text`)
	exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$4,'ORGANIZER'),($2,$4,'ORGANIZER'),($3,$5,'ORGANIZER')`, creator, colleague, outsider, unit, otherUnit)
	exec(`UPDATE users SET is_admin=true WHERE id=$1`, adminOnly)
	store := New(pool)
	svc := a.New(store, nil)
	actor := a.Actor{ID: colleague, IsOrganizer: true}
	event := func(status string) string {
		return scalar(`INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,status) VALUES($1,$2,'Delete test','Campus',now()+interval '3 days',now()+interval '4 days',now(),now()+interval '2 days',10,$3) RETURNING id::text`, unit, creator, status)
	}
	snapshot := func(id string) string {
		return scalar(`SELECT jsonb_build_object('event',(SELECT to_jsonb(e) FROM events e WHERE id=$1),'registrations',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM registrations r WHERE event_id=$1),'notifications',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM notifications n WHERE event_id=$1),'audit',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM activity_logs l WHERE event_id=$1 OR entity_id=$1))::text`, id)
	}
	request := func(t *testing.T, profile auth.Profile, id string, wantStatus int, wantCode string) {
		t.Helper()
		e := echo.New()
		eventhttp.New(svc, nil).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Set("auth.profile", profile)
				return next(c)
			}
		})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/events/"+id, nil))
		if rec.Code != wantStatus {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if wantStatus == 204 {
			if rec.Body.Len() != 0 {
				t.Fatal("204 returned a body")
			}
			return
		}
		var body struct{ Error struct{ Code string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != wantCode {
			t.Fatal(rec.Body.String(), err)
		}
	}
	profile := func(id string, organizer, admin bool) auth.Profile {
		p := auth.Profile{ID: id, IsAdmin: admin}
		if organizer {
			p.Memberships = []auth.Membership{{}}
		}
		return p
	}
	t.Run("HTTP assigned non creator deletes only draft", func(t *testing.T) {
		id, untouched := event("DRAFT"), event("DRAFT")
		before := snapshot(untouched)
		request(t, profile(colleague, true, false), id, 204, "")
		if scalar(`SELECT count(*)::text FROM events WHERE id=$1`, id) != "0" || snapshot(untouched) != before {
			t.Fatal("wrong deletion scope")
		}
		request(t, profile(colleague, true, false), id, 404, "NOT_FOUND")
		if scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1 OR entity_id=$1`, id) != "0" {
			t.Fatal("unexpected new audit policy")
		}
	})
	for _, tc := range []struct {
		name              string
		p                 auth.Profile
		inactive, revoked bool
		status            int
		code              string
	}{
		{"foreign organizer", profile(outsider, true, false), false, false, 404, "NOT_FOUND"},
		{"admin alone", profile(adminOnly, false, true), false, false, 403, "FORBIDDEN"},
		{"no role", profile(user(), false, false), false, false, 403, "FORBIDDEN"},
		{"inactive assigned unit", profile(colleague, true, false), true, false, 404, "NOT_FOUND"},
		{"creator membership revoked", profile(creator, true, false), false, true, 404, "NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := event("DRAFT")
			if tc.inactive {
				exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
				defer exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
			}
			if tc.revoked {
				exec(`DELETE FROM unit_memberships WHERE user_id=$1`, creator)
				defer exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, creator, unit)
			}
			before := snapshot(id)
			request(t, tc.p, id, tc.status, tc.code)
			if snapshot(id) != before {
				t.Fatal("denial changed event/history")
			}
		})
	}
	t.Run("admin assigned organizer succeeds", func(t *testing.T) {
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, adminOnly, unit)
		defer exec(`DELETE FROM unit_memberships WHERE user_id=$1`, adminOnly)
		request(t, profile(adminOnly, true, true), event("DRAFT"), 204, "")
	})
	t.Run("missing and malformed IDs", func(t *testing.T) {
		request(t, profile(colleague, true, false), "00000000-0000-0000-0000-000000000000", 404, "NOT_FOUND")
		request(t, profile(colleague, true, false), "not-a-uuid", 400, "VALIDATION_ERROR")
	})
	for _, status := range []string{"PENDING_APPROVAL", "REJECTED", "PUBLISHED", "CANCELLED", "COMPLETED"} {
		t.Run(status+" retains history and hides foreign scope", func(t *testing.T) {
			id := event(status)
			exec(`INSERT INTO activity_logs(actor_id,event_id,entity_type,entity_id,action) VALUES($1,$2,'EVENT',$2,'SUBMIT')`, creator, id)
			before := snapshot(id)
			request(t, profile(colleague, true, false), id, 409, "INVALID_STATE_TRANSITION")
			request(t, profile(outsider, true, false), id, 404, "NOT_FOUND")
			if snapshot(id) != before {
				t.Fatal("non draft event/history changed")
			}
		})
	}
	// These rows cannot arise for a fresh draft through the current workflow.
	// Existing FK restrictions must still protect them; do not invent cleanup.
	for _, table := range []string{"registrations", "notifications", "activity_logs"} {
		t.Run("NO ACTION preserves "+table, func(t *testing.T) {
			id := event("DRAFT")
			switch table {
			case "registrations":
				exec(`INSERT INTO registrations(event_id,student_id,status,checkin_code) VALUES($1,$2,'REGISTERED','DELETE01')`, id, creator)
			case "notifications":
				exec(`INSERT INTO notifications(event_id,user_id,type,title,body) VALUES($1,$2,'EVENT_CHANGED','History','History')`, id, creator)
			case "activity_logs":
				exec(`INSERT INTO activity_logs(event_id,actor_id,entity_type,entity_id,action) VALUES($1,$2,'EVENT',$1,'UPDATE_EVENT')`, id, creator)
			}
			before := snapshot(id)
			err := svc.DeleteDraft(ctx, actor, id)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
				t.Fatalf("expected unchanged FK restriction, got %v", err)
			}
			if snapshot(id) != before {
				t.Fatal("failed deletion lost history")
			}
		})
	}
	t.Run("failed delete rolls back", func(t *testing.T) {
		id := event("DRAFT")
		before := snapshot(id)
		exec(`CREATE FUNCTION fail_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER fail_delete AFTER DELETE ON events FOR EACH ROW EXECUTE FUNCTION fail_delete()`)
		defer exec(`DROP TRIGGER fail_delete ON events; DROP FUNCTION fail_delete()`)
		if err := svc.DeleteDraft(ctx, actor, id); err == nil {
			t.Fatal("unexpected success")
		}
		if snapshot(id) != before {
			t.Fatal("failed deletion persisted")
		}
	})
	for _, first := range []string{"delete", "submit", "duplicate delete"} {
		t.Run("concurrent "+first+" first", func(t *testing.T) {
			id := event("DRAFT")
			gate := &deletionGate{Store: store, entered: make(chan struct{}), release: make(chan struct{})}
			defer close(gate.release)
			firstResult, secondResult := make(chan error, 1), make(chan error, 1)
			go func() {
				if first == "submit" {
					_, err := a.NewApprovalService(gate).Submit(ctx, actor, id, 0)
					firstResult <- err
				} else {
					firstResult <- a.New(gate, nil).DeleteDraft(ctx, actor, id)
				}
			}()
			select {
			case <-gate.entered:
			case err := <-firstResult:
				t.Fatalf("first operation failed before gate: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() {
				if first == "delete" {
					_, err := a.NewApprovalService(store).Submit(ctx, actor, id, 0)
					secondResult <- err
				} else {
					secondResult <- svc.DeleteDraft(ctx, actor, id)
				}
			}()
			// Prove overlap at PostgreSQL's event lock, not just goroutine timing.
			deadline := time.Now().Add(5 * time.Second)
			for scalar(`SELECT count(*)::text FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`) == "0" {
				select {
				case err := <-secondResult:
					t.Fatalf("competitor did not wait for event lock: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("competitor never reached event lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			gate.release <- struct{}{}
			if err := <-firstResult; err != nil {
				t.Fatal(err)
			}
			want := domain.ErrNotFound
			if first == "submit" {
				want = domain.ErrState
			}
			if err := <-secondResult; !errors.Is(err, want) {
				t.Fatalf("loser=%v want=%v", err, want)
			}
			if first == "submit" {
				if scalar(`SELECT status::text||':'||version::text FROM events WHERE id=$1`, id) != "PENDING_APPROVAL:2" || scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1 AND action='SUBMIT'`, id) != "1" {
					t.Fatal("submitted event or audit lost")
				}
			} else if scalar(`SELECT count(*)::text FROM events WHERE id=$1`, id) != "0" || scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1`, id) != "0" {
				t.Fatal("losing mutation persisted")
			}
		})
	}
}
