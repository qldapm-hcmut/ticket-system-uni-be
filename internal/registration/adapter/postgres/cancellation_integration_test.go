//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	authhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/http"
	authpg "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/postgres"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/token"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	reghttp "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func testCancellationOnBehalf(t *testing.T, pool *pgxpool.Pool, unit, organizer string, event func(int, int) string, seed func(string, int) []a.Registration, counters func(string, int, int), concurrent func(...func() error) []error, checkQueue func(string)) {
	ctx := context.Background()
	svc := a.New(New(pool))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	user := func() string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn','B3') RETURNING id::text`)
	}
	near := func(e string) {
		exec(`UPDATE events SET start_at=now()+interval '12 hours',end_at=now()+interval '13 hours',registration_closes_at=now()+interval '1 hour' WHERE id=$1`, e)
	}
	tokens, err := token.New(strings.Repeat("b", 32), kernel.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	router := echo.New()
	authHandler := authhttp.New(nil, tokens, authpg.NewProfileReader(pool))
	reghttp.New(svc).Register(router, authHandler.Authenticate)
	request := func(actor, id, body string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/registrations/"+id+"/cancel", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if actor != "" {
			access, err := tokens.AccessToken(actor, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+access)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("want %d got %d %s", status, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Run("no MSSV reason and audit promote", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 3)
		near(e)
		request("", regs[0].ID, `{}`, 401)
		for _, body := range []string{"", `{}`, `{"reason":""}`, `{"reason":"  \n "}`} {
			request(organizer, regs[0].ID, body, 400)
		}
		for _, body := range []string{`null`, `[]`, `{"reason":null}`, `{"reason":1}`, `{} {}`, `{"reason":"a\u0000b"}`, `{"reason":" ` + strings.Repeat("á", 255) + `"}`, `{"reason":"` + strings.Repeat("á", 256) + `"}`} {
			request(organizer, regs[0].ID, body, 400)
		}
		counters(e, 1, 2)
		out := request(organizer, regs[0].ID, `{"reason":"  timetable conflict  "}`, 200)
		data := out["data"].(map[string]any)
		if data["cancelledBy"] != "ORGANIZER" || data["cancelledAt"] == nil || out["meta"].(map[string]any)["promotedRegistrationId"] != regs[1].ID {
			t.Fatalf("%+v", out)
		}
		if got := scalar(`SELECT cancelled_by_user::text||':'||cancel_reason FROM registrations WHERE id=$1`, regs[0].ID); got != organizer+":timetable conflict" {
			t.Fatal(got)
		}
		var metadata map[string]string
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT metadata FROM activity_logs WHERE event_id=$1 AND entity_id=$2 AND entity_type='REGISTRATION' AND action='CANCEL_ON_BEHALF' AND actor_id=$3`, e, regs[0].ID, organizer).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatal(err)
		}
		if len(metadata) != 2 || metadata["reason"] != "timetable conflict" || metadata["studentId"] != regs[0].StudentID {
			t.Fatalf("%s", raw)
		}
		counters(e, 1, 1)
		checkQueue(e)
		request(organizer, regs[0].ID, `{"reason":"again"}`, 409)
		request(organizer, regs[2].ID, `{"reason":"leave waitlist"}`, 200)
		counters(e, 1, 0)
	})
	t.Run("raw padded boundary trims stored reason and audit", func(t *testing.T) {
		e := event(1, 1)
		reg := seed(e, 1)[0]
		request(organizer, reg.ID, `{"reason":" `+strings.Repeat("á", 253)+` "}`, 200)
		if got := scalar(`SELECT cancel_reason FROM registrations WHERE id=$1`, reg.ID); got != strings.Repeat("á", 253) {
			t.Fatal(got)
		}
		if got := scalar(`SELECT metadata->>'reason' FROM activity_logs WHERE entity_id=$1 AND action='CANCEL_ON_BEHALF'`, reg.ID); got != strings.Repeat("á", 253) {
			t.Fatal(got)
		}
	})
	t.Run("database roles active unit and scope", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 1)
		outsider := user()
		admin := user()
		plain := user()
		otherUnit := scalar(`INSERT INTO units(code,name,type) VALUES('B3OTHER','Other','FACULTY') RETURNING id::text`)
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, outsider, otherUnit)
		exec(`UPDATE users SET is_admin=true WHERE id=$1`, admin)
		request(outsider, regs[0].ID, `{"reason":"scope"}`, 404)
		request(plain, regs[0].ID, `{"reason":"role"}`, 403)
		request(plain, "00000000-0000-0000-0000-000000000000", `{"reason":"role"}`, 403)
		request(admin, regs[0].ID, `{"reason":"admin only"}`, 403)
		student := seed(event(1, 1), 1)[0].StudentID
		request(student, regs[0].ID, `{"reason":"not mine"}`, 404)
		request(regs[0].StudentID, regs[0].ID, "", 200) // body-less self remains supported
		e = event(1, 3)
		regs = seed(e, 1)
		exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
		request(organizer, regs[0].ID, `{"reason":"inactive unit"}`, 404)
		exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
		exec(`UPDATE users SET is_active=false WHERE id=$1`, organizer)
		if _, err := svc.Cancel(ctx, organizer, regs[0].ID, "inactive actor"); !errors.Is(err, a.ErrForbidden) {
			t.Fatalf("%v", err)
		}
		request(organizer, regs[0].ID, `{"reason":"inactive actor"}`, 401)
		exec(`UPDATE users SET is_active=true WHERE id=$1`, organizer)
		exec(`DELETE FROM unit_memberships WHERE user_id=$1 AND unit_id=$2`, organizer, unit)
		request(organizer, regs[0].ID, `{"reason":"revoked"}`, 403)
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, organizer, unit)
		request(organizer, "00000000-0000-0000-0000-000000000000", `{"reason":"missing"}`, 404)
		counters(e, 1, 0)
	})
	t.Run("unauthorized preflight never waits for event lock", func(t *testing.T) {
		e := event(1, 3)
		reg := seed(e, 1)[0]
		plain, admin, outsider := user(), user(), user()
		exec(`UPDATE users SET is_admin=true WHERE id=$1`, admin)
		otherUnit := scalar(`INSERT INTO units(code,name,type) VALUES(substr(gen_random_uuid()::text,1,30),'Preflight','FACULTY') RETURNING id::text`)
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, outsider, otherUnit)
		student := seed(event(1, 1), 1)[0].StudentID
		// An owner retaining Organizer but losing Student must also fail before lock.
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, reg.StudentID, unit)
		exec(`UPDATE users SET student_code=NULL WHERE id=$1`, reg.StudentID)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `SELECT id FROM events WHERE id=$1 FOR UPDATE`, e); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, actor, id string
			want            error
		}{
			{"roleless existing", plain, reg.ID, a.ErrForbidden},
			{"roleless missing", plain, "00000000-0000-0000-0000-000000000000", a.ErrForbidden},
			{"admin existing", admin, reg.ID, a.ErrForbidden},
			{"student not owner", student, reg.ID, a.ErrNotFound},
			{"organizer wrong scope", outsider, reg.ID, a.ErrNotFound},
			{"owner without Student", reg.StudentID, reg.ID, a.ErrForbidden},
			{"inactive unit", organizer, reg.ID, a.ErrNotFound},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.name == "inactive unit" {
					exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
					defer exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
				}
				// The blocker stays held until every request has completed. A path
				// that attempts the event lock times out instead of returning want.
				requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				_, err := svc.Cancel(requestCtx, tc.actor, tc.id, "preflight")
				if !errors.Is(err, tc.want) {
					t.Fatalf("while event locked: got %v want %v", err, tc.want)
				}
			})
		}
		counters(e, 1, 0)
	})
	t.Run("owner cannot bypass deadline with organizer admin", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 1)
		self := regs[0].StudentID
		exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, self, unit)
		exec(`UPDATE users SET is_admin=true WHERE id=$1`, self)
		// Normal self deadline still works, including for Organizer + Admin.
		far := event(1, 3)
		ownFar, err := svc.Register(ctx, self, far)
		if err != nil {
			t.Fatal(err)
		}
		farOut := request(self, ownFar.ID, "", 200)
		if farOut["data"].(map[string]any)["cancelledBy"] != "STUDENT" {
			t.Fatal(farOut)
		}
		near(e)
		request(self, regs[0].ID, `{"reason":"staff bypass"}`, 422)
		exec(`UPDATE registrations SET cancellation_exception_until=now()+interval '1 hour' WHERE id=$1`, regs[0].ID)
		out := request(self, regs[0].ID, `{"reason":"optional self reason"}`, 200)
		if out["data"].(map[string]any)["cancelledBy"] != "STUDENT" {
			t.Fatal(out)
		}
		if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1 AND action='CANCEL_ON_BEHALF'`, e); got != "0" {
			t.Fatal(got)
		}
		if got := scalar(`SELECT (cancellation_exception_until IS NULL)::text||':'||cancel_reason FROM registrations WHERE id=$1`, regs[0].ID); got != "true:optional self reason" {
			t.Fatal(got)
		}
		e = event(1, 3)
		regs = seed(e, 1)
		exec(`UPDATE registrations SET cancellation_exception_until=now()-interval '1 second' WHERE id=$1`, regs[0].ID)
		near(e)
		request(regs[0].StudentID, regs[0].ID, "", 422)
		// A self-owned registration does not grant student privileges after MSSV removal.
		exec(`UPDATE users SET student_code=NULL WHERE id=$1`, self)
		e = event(1, 3)
		own, err := svc.Register(ctx, self, e)
		if err != nil {
			t.Fatal(err)
		}
		request(self, own.ID, `{"reason":"not a student"}`, 403)
	})
	t.Run("before start only for staff and waiting self", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 3)
		near(e)
		request(regs[1].StudentID, regs[1].ID, "", 200)
		exec(`UPDATE events SET start_at=now(),end_at=now()+interval '1 hour',registration_closes_at=now()-interval '1 second' WHERE id=$1`, e)
		exec(`UPDATE registrations SET cancellation_exception_until=now()+interval '1 day' WHERE id=$1`, regs[0].ID)
		request(organizer, regs[0].ID, `{"reason":"started"}`, 422)
		request(organizer, regs[2].ID, `{"reason":"started waiting"}`, 409)
		request(regs[0].StudentID, regs[0].ID, "", 422)
		request(regs[2].StudentID, regs[2].ID, "", 409)
		counters(e, 1, 1)
	})
	t.Run("exact SQL deadline boundaries", func(t *testing.T) {
		for _, tc := range []struct {
			name, start, exception string
			staff, waiting         bool
			want                   error
		}{
			{"self at 24h", "24 hours", "0 seconds", false, false, a.ErrCancelDeadline},
			{"self before 24h", "24 hours 1 second", "0 seconds", false, false, nil},
			{"self exception at now", "12 hours", "0 seconds", false, false, a.ErrCancelDeadline},
			{"self active exception", "12 hours", "1 second", false, false, nil},
			{"self at start with exception", "0 seconds", "1 hour", false, false, a.ErrCancelDeadline},
			{"staff at start", "0 seconds", "1 hour", true, false, a.ErrCancelDeadline},
			{"staff just before start", "1 second", "0 seconds", true, false, nil},
			{"waiting at start", "0 seconds", "1 hour", false, true, a.ErrState},
			{"waiting just before start", "1 second", "0 seconds", false, true, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := event(1, 3)
				regs := seed(e, 2)
				reg := regs[0]
				if tc.waiting {
					reg = regs[1]
				}
				store := New(pool)
				// Explicit decision_at makes the SQL comparisons exact, independent of
				// transaction-start now() and elapsed test execution time.
				rollback := errors.New("rollback boundary fixture")
				err := store.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
					q := store.q.WithTx(tx)
					decisionAt, err := q.GetCancellationDecisionTime(ctx)
					if err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE events SET start_at=$3::timestamptz+$2::interval,end_at=$3::timestamptz+interval '4 days',registration_closes_at=$3::timestamptz-interval '1 second' WHERE id=$1`, e, tc.start, decisionAt); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE registrations SET cancellation_exception_until=$3::timestamptz+$2::interval WHERE id=$1 AND status='REGISTERED'`, reg.ID, tc.exception, decisionAt); err != nil {
						return err
					}
					actor, by := reg.StudentID, db.CancelActorSTUDENT
					if tc.staff {
						actor, by = organizer, db.CancelActorORGANIZER
					}
					id, _ := uuid(reg.ID)
					actorID, _ := uuid(actor)
					out, err := q.CancelRegistration(ctx, db.CancelRegistrationParams{
						ID: id, ActorID: actorID, CancelledBy: db.NullCancelActor{CancelActor: by, Valid: true}, DecisionAt: decisionAt,
					})
					if tc.want != nil {
						if !errors.Is(err, pgx.ErrNoRows) {
							t.Fatalf("got %v want no cancellation", err)
						}
					} else if err != nil || !out.CancelledAt.Time.Equal(decisionAt.Time) {
						t.Fatalf("cancellation=%+v err=%v", out, err)
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatal(err)
				}
			})
		}
	})
	t.Run("blocked event lock rechecks authorization and deadlines", func(t *testing.T) {
		for _, tc := range []struct {
			name                  string
			self, waiting, revoke bool
			scopeRevoke           string
			want                  error
		}{
			{"deactivated organizer", false, false, true, "", a.ErrForbidden},
			{"deactivated owner", true, false, true, "", a.ErrForbidden},
			{"owner Student revoked", true, false, true, "student", a.ErrForbidden},
			{"organizer role revoked", false, false, true, "roles", a.ErrForbidden},
			{"scope membership revoked", false, false, true, "membership", a.ErrNotFound},
			{"scope unit deactivated", false, false, true, "unit", a.ErrNotFound},
			{"organizer across start", false, false, false, "", a.ErrCancelDeadline},
			{"owner exception across start", true, false, false, "", a.ErrCancelDeadline},
			{"waiting owner across start", true, true, false, "", a.ErrState},
			{"waiting organizer across start", false, true, false, "", a.ErrState},
			{"owner across 24h cutoff", true, false, false, "cutoff", a.ErrCancelDeadline},
			{"owner across exception expiry", true, false, false, "exception", a.ErrCancelDeadline},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				e := event(1, 3)
				regs := seed(e, 2)
				reg := regs[0]
				if tc.waiting {
					reg = regs[1]
				}
				actor := organizer
				if tc.self {
					actor = reg.StudentID
				}
				if !tc.revoke && tc.scopeRevoke != "cutoff" {
					exec(`UPDATE events
						SET start_at = clock_timestamp() + interval '12 hours',
							end_at = clock_timestamp() + interval '13 hours',
							registration_closes_at = clock_timestamp() - interval '1 second'
						WHERE id = $1`, e)
					// Even an exception beyond start must not permit cancellation.
					exec(`UPDATE registrations
						SET cancellation_exception_until = clock_timestamp() + interval '1 day'
						WHERE id = $1`, regs[0].ID)
				}
				parent, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer parent.Rollback(context.Background())
				var blocker int32
				if err := parent.QueryRow(ctx, `SELECT pg_backend_pid() FROM events WHERE id=$1 FOR UPDATE`, e).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				errs := make(chan error, 1)
				go func() {
					_, err := svc.Cancel(ctx, actor, reg.ID, "blocked cancellation")
					errs <- err
				}()
				// Observe a real ungranted lock held up by this exact transaction.
				// Polling is bounded; no sleep is used to guess whether Cancel ran.
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					var blocked, beganBeforeStart bool
					err := pool.QueryRow(ctx, `SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid
						WHERE a.datname=current_database() AND NOT l.granted
						AND $1=ANY(pg_blocking_pids(a.pid))
						AND strpos(a.query,'-- name: GetEventForRegistration :one')>0), EXISTS (
						SELECT 1 FROM pg_stat_activity a, events e
						WHERE e.id=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.xact_start<e.start_at)`, blocker, e).Scan(&blocked, &beganBeforeStart)
					if err != nil {
						t.Fatalf("observe cancellation event-lock wait: %v", err)
					}
					if blocked {
						if !beganBeforeStart {
							t.Fatal("cancellation must begin before start")
						}
						break
					}
					select {
					case err := <-errs:
						t.Fatalf("cancellation finished before lock release: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-ticker.C:
					}
				}
				if tc.scopeRevoke == "student" {
					code := scalar(`SELECT student_code FROM users WHERE id=$1`, actor)
					exec(`UPDATE users SET student_code=NULL WHERE id=$1`, actor)
					defer exec(`UPDATE users SET student_code=$2 WHERE id=$1`, actor, code)
				} else if tc.scopeRevoke == "roles" {
					exec(`DELETE FROM unit_memberships WHERE user_id=$1`, actor)
					defer exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, actor, unit)
				} else if tc.scopeRevoke == "membership" {
					// Keep the Organizer role in another unit: only event scope is revoked.
					other := scalar(`INSERT INTO units(code,name,type) VALUES(substr(gen_random_uuid()::text,1,30),'Revoke scope','FACULTY') RETURNING id::text`)
					exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, actor, other)
					exec(`DELETE FROM unit_memberships WHERE user_id=$1 AND unit_id=$2`, actor, unit)
					defer exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, actor, unit)
					defer exec(`DELETE FROM unit_memberships WHERE user_id=$1 AND unit_id=$2`, actor, other)
				} else if tc.scopeRevoke == "unit" {
					exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
					defer exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
				} else if tc.revoke {
					exec(`UPDATE users SET is_active=false WHERE id=$1`, actor)
					defer exec(`UPDATE users SET is_active=true WHERE id=$1`, actor)
				} else {
					// Advance the fixture boundary only after the exact waiter is
					// observed. The waiter's transaction began before this instant;
					// its fresh post-lock decision will be after it. No scheduling
					// window or wall-clock sleep is needed to cross the deadline.
					var boundary time.Time
					if err := parent.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&boundary); err != nil {
						t.Fatal(err)
					}
					switch tc.scopeRevoke {
					case "cutoff":
						_, err = parent.Exec(ctx, `UPDATE events
							SET start_at = $2::timestamptz + interval '24 hours',
								end_at = $2::timestamptz + interval '25 hours',
								registration_closes_at = $2::timestamptz - interval '1 second'
							WHERE id = $1`, e, boundary)
					case "exception":
						_, err = parent.Exec(ctx, `UPDATE registrations
							SET cancellation_exception_until = $2
							WHERE id = $1`, regs[0].ID, boundary)
					default:
						_, err = parent.Exec(ctx, `UPDATE events
							SET start_at = $2::timestamptz,
								end_at = $2::timestamptz + interval '1 hour',
								registration_closes_at = $2::timestamptz - interval '1 second'
							WHERE id = $1`, e, boundary)
					}
					if err != nil {
						t.Fatal(err)
					}
					var crossed bool
					if err := parent.QueryRow(ctx, `SELECT EXISTS (
						SELECT 1 FROM pg_stat_activity a
						WHERE $1 = ANY(pg_blocking_pids(a.pid))
							AND a.xact_start < $2::timestamptz
							AND clock_timestamp() >= $2::timestamptz
					)`, blocker, boundary).Scan(&crossed); err != nil || !crossed {
						t.Fatalf("deadline must cross while cancellation waits: crossed=%v err=%v", crossed, err)
					}
				}
				if err := parent.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-errs:
					if !errors.Is(err, tc.want) {
						t.Fatalf("got %v want %v", err, tc.want)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				counters(e, 1, 1)
				if got := scalar(`SELECT count(*)::text FROM registrations WHERE event_id=$1 AND (status NOT IN ('REGISTERED','WAITLISTED') OR cancelled_at IS NOT NULL OR cancelled_by IS NOT NULL OR cancel_reason IS NOT NULL OR promoted_at IS NOT NULL)`, e); got != "0" {
					t.Fatalf("mutated registrations: %s", got)
				}
				if got := scalar(`SELECT count(*)::text FROM notifications WHERE event_id=$1 AND type IN ('REG_CANCELLED','PROMOTED')`, e); got != "0" {
					t.Fatalf("cancellation/promotion notifications: %s", got)
				}
				if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1`, e); got != "0" {
					t.Fatalf("cancellation audit: %s", got)
				}
			})
		}
	})
	t.Run("concurrent duplicate cancellation exactly once", func(t *testing.T) {
		e := event(1, 5)
		regs := seed(e, 3)
		near(e)
		task := func() error { _, err := svc.Cancel(ctx, organizer, regs[0].ID, "duplicate"); return err }
		successes, conflicts := 0, 0
		for _, err := range concurrent(task, task) {
			if err == nil {
				successes++
			} else if errors.Is(err, a.ErrState) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("%d %d", successes, conflicts)
		}
		counters(e, 1, 1)
		checkQueue(e)
		if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1 AND action='CANCEL_ON_BEHALF'`, e); got != "1" {
			t.Fatal(got)
		}
	})
	t.Run("concurrent different cancellations FIFO", func(t *testing.T) {
		e := event(3, 6)
		regs := seed(e, 6)
		near(e)
		tasks := make([]func() error, 3)
		for i := range tasks {
			r := regs[i]
			tasks[i] = func() error { _, err := svc.Cancel(ctx, organizer, r.ID, "concurrent"); return err }
		}
		for _, err := range concurrent(tasks...) {
			if err != nil {
				t.Fatal(err)
			}
		}
		counters(e, 3, 0)
		checkQueue(e)
		if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1 AND action='CANCEL_ON_BEHALF'`, e); got != "3" {
			t.Fatal(got)
		}
	})
	t.Run("zero row invariant failures rollback everything", func(t *testing.T) {
		for _, tc := range []struct {
			name, table, timing, operation, body, message string
		}{
			{"audit", "activity_logs", "BEFORE", "INSERT", `IF NEW.action='CANCEL_ON_BEHALF' THEN RETURN NULL; END IF;`, "audit cancellation"},
			{"counter", "events", "BEFORE", "UPDATE", `IF NEW.registered_count>OLD.registered_count THEN RETURN NULL; END IF;`, "increment promoted counters"},
			{"promotion", "registrations", "BEFORE", "UPDATE", `IF OLD.status='WAITLISTED' AND NEW.status='REGISTERED' THEN RETURN NULL; END IF;`, "promote selected waitlist head"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := event(1, 3)
				regs := seed(e, 2)
				near(e)
				exec(`UPDATE registrations SET cancellation_exception_until=now()+interval '1 hour' WHERE id=$1`, regs[0].ID)
				// Deterministically suppress exactly the targeted write, without
				// raising SQL errors: execrows/no-row checks must catch this.
				exec(`CREATE FUNCTION suppress_b3_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ` + tc.body + ` RETURN NEW; END $$; CREATE TRIGGER suppress_b3_write ` + tc.timing + ` ` + tc.operation + ` ON ` + tc.table + ` FOR EACH ROW EXECUTE FUNCTION suppress_b3_write()`)
				defer exec(`DROP TRIGGER suppress_b3_write ON ` + tc.table + `; DROP FUNCTION suppress_b3_write()`)
				_, err := svc.Cancel(ctx, organizer, regs[0].ID, "must rollback")
				if err == nil || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("expected contextual internal error %q, got %v", tc.message, err)
				}
				if tc.name != "promotion" && !strings.Contains(err.Error(), "expected 1 row, got 0") {
					t.Fatal(err)
				}
				if tc.name == "promotion" && !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("missing wrapped promotion error: %v", err)
				}
				request(organizer, regs[0].ID, `{"reason":"must rollback"}`, 500)
				counters(e, 1, 1)
				if got := scalar(`SELECT status::text||':'||(cancelled_at IS NULL)::text||':'||(cancelled_by IS NULL)::text||':'||(cancelled_by_user IS NULL)::text||':'||(cancel_reason IS NULL)::text||':'||(cancellation_exception_until IS NOT NULL)::text FROM registrations WHERE id=$1`, regs[0].ID); got != "REGISTERED:true:true:true:true:true" {
					t.Fatal(got)
				}
				if got := scalar(`SELECT status::text||':'||(promoted_at IS NULL)::text FROM registrations WHERE id=$1`, regs[1].ID); got != "WAITLISTED:true" {
					t.Fatal(got)
				}
				if got := scalar(`SELECT count(*)::text FROM notifications WHERE event_id=$1 AND type IN ('REG_CANCELLED','PROMOTED')`, e); got != "0" {
					t.Fatal(got)
				}
				if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1`, e); got != "0" {
					t.Fatal(got)
				}
			})
		}
	})
	t.Run("audit failure rolls back cancellation promotion counters and notifications", func(t *testing.T) {
		e := event(1, 3)
		regs := seed(e, 2)
		near(e)
		exec(`UPDATE registrations SET cancellation_exception_until=now()+interval '1 hour' WHERE id=$1`, regs[0].ID)
		exec(`CREATE FUNCTION fail_b3_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='CANCEL_ON_BEHALF' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_b3_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION fail_b3_audit()`)
		defer exec(`DROP TRIGGER fail_b3_audit ON activity_logs; DROP FUNCTION fail_b3_audit()`)
		request(organizer, regs[0].ID, `{"reason":"must rollback"}`, 500)
		counters(e, 1, 1)
		if got := scalar(`SELECT status::text||':'||(cancelled_at IS NULL)::text||':'||(cancelled_by IS NULL)::text||':'||(cancel_reason IS NULL)::text||':'||(cancellation_exception_until IS NOT NULL)::text FROM registrations WHERE id=$1`, regs[0].ID); got != "REGISTERED:true:true:true:true" {
			t.Fatal(got)
		}
		if got := scalar(`SELECT status::text||':'||(promoted_at IS NULL)::text FROM registrations WHERE id=$1`, regs[1].ID); got != "WAITLISTED:true" {
			t.Fatal(got)
		}
		if got := scalar(`SELECT count(*)::text FROM notifications WHERE event_id=$1 AND type IN ('REG_CANCELLED','PROMOTED')`, e); got != "0" {
			t.Fatal(got)
		}
		if got := scalar(`SELECT count(*)::text FROM activity_logs WHERE event_id=$1`, e); got != "0" {
			t.Fatal(got)
		}
	})
}
