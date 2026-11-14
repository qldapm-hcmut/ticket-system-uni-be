//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	eventhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type contractClock struct{ at time.Time }

func (c contractClock) Now() time.Time { return c.at }

func eventContractPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("event_contract_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema, err := os.ReadFile("../../../../db/migrations/0001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestEventContract(t *testing.T) {
	pool := eventContractPool(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	user := func() string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn','Test') RETURNING id::text`)
	}
	owner, student, adminID, other := user(), user(), user(), user()
	exec(`UPDATE users SET student_code='OWNER' WHERE id=$1`, owner)
	exec(`UPDATE users SET student_code='STUDENT' WHERE id=$1`, student)
	exec(`UPDATE users SET is_admin=true WHERE id=$1`, adminID)
	unit := scalar(`INSERT INTO units(code,name,type) VALUES('CONTRACT','Contract','FACULTY') RETURNING id::text`)
	exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, owner, unit)
	event := func(status string) string {
		return scalar(`INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,status) VALUES($1,$2,'Contract event','Campus',now()+interval '3 days',now()+interval '4 days',now()-interval '1 day',now()+interval '2 days',10,$3) RETURNING id::text`, unit, owner, status)
	}
	store := New(pool)
	router := echo.New()
	middleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			uid := c.Request().Header.Get("X-Test-Actor")
			p := auth.Profile{}
			p.ID = uid
			p.IsAdmin = uid == adminID
			if uid == owner {
				p.StudentCode = "OWNER"
				p.Memberships = []auth.Membership{{}}
			}
			if uid == student {
				p.StudentCode = "STUDENT"
			}
			c.Set("auth.profile", p)
			return next(c)
		}
	}
	reader := NewReader(pool, kernel.SystemClock{})
	eventhttp.New(a.New(store, kernel.SystemClock{}), reader).Register(router, middleware)
	eventhttp.NewApprovalHandler(a.NewApprovalService(store, kernel.SystemClock{}), reader).Register(router, middleware)
	eventhttp.NewManagementHandler(a.NewManagementService(store)).Register(router, middleware)
	request := func(t *testing.T, uid, method, path, body string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("X-Test-Actor", uid)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Run("projection and active registration", func(t *testing.T) {
		e := event("PUBLISHED")
		exec(`UPDATE events SET cancel_reason='Public cancellation',reject_reason='Private rejection',approved_by=$2 WHERE id=$1`, e, adminID)
		// Cancelled history must not hide the later active row or alter FIFO rank.
		exec(`INSERT INTO registrations(event_id,student_id,status,checkin_code,cancelled_at) VALUES($1,$2,'CANCELLED','HISTORY1',now())`, e, owner)
		exec(`INSERT INTO registrations(event_id,student_id,status,checkin_code) VALUES($1,$2,'WAITLISTED','FIRST001'),($1,$3,'WAITLISTED','SECOND01')`, e, student, owner)
		for _, uid := range []string{other, student, owner, adminID} {
			out := request(t, uid, "GET", "/events/"+e, "", 200)["data"].(map[string]any)
			for _, key := range []string{"version", "createdBy", "approvedBy", "rejectReason"} {
				_, has := out[key]
				if has != (uid == owner || uid == adminID) {
					t.Fatalf("actor=%s unexpected %s visibility: %v", uid, key, out)
				}
			}
			if out["cancelReason"] != "Public cancellation" {
				t.Fatal(out)
			}
			reg, has := out["myRegistration"]
			if has != (uid == owner || uid == student) {
				t.Fatalf("student field presence: %v", out)
			}
			if uid == owner && reg.(map[string]any)["waitlistPosition"] != float64(2) {
				t.Fatalf("FIFO: %v", reg)
			}
		}
		// The same projection must apply to both list paths.
		for _, path := range []string{"/events", "/manage/events"} {
			uid := owner
			if path == "/events" {
				uid = student
			}
			out := request(t, uid, "GET", path, "", 200)["data"].([]any)
			row := out[0].(map[string]any)
			if _, ok := row["myRegistration"]; !ok {
				t.Fatal(row)
			}
			_, has := row["version"]
			if has != (uid == owner) {
				t.Fatal(row)
			}
		}
		// Effective expiry is calculated at the same injected render instant.
		start := time.Now().UTC().Truncate(time.Microsecond)
		exec(`UPDATE events SET start_at=$2::timestamptz,end_at=$2::timestamptz+interval '1 hour',registration_opens_at=$2::timestamptz-interval '2 hours',registration_closes_at=$2::timestamptz-interval '1 hour' WHERE id=$1`, e, start)
		exact, err := NewReader(pool, contractClock{at: start}).Detail(ctx, a.Actor{ID: owner, IsStudent: true}, e)
		if err != nil || exact["myRegistration"] != nil || exact["waitlistCount"] != int32(0) {
			t.Fatalf("exact start projection: %v %v", exact, err)
		}
		out := request(t, owner, "GET", "/events/"+e, "", 200)["data"].(map[string]any)
		if out["myRegistration"] != nil || out["waitlistCount"] != float64(0) {
			t.Fatal(out)
		}
		exec(`UPDATE registrations SET status='REGISTERED' WHERE event_id=$1 AND student_id=$2 AND status='WAITLISTED'`, e, owner)
		out = request(t, owner, "GET", "/events/"+e, "", 200)["data"].(map[string]any)
		reg := out["myRegistration"].(map[string]any)
		if reg["status"] != "REGISTERED" || reg["waitlistPosition"] != nil {
			t.Fatal(reg)
		}
	})
	t.Run("submission and decision response shape audit and recipient", func(t *testing.T) {
		e := event("DRAFT")
		for _, decision := range []string{"reject", "approve"} {
			out := request(t, owner, "POST", "/events/"+e+"/submit", "", 200)["data"].(map[string]any)
			for _, key := range []string{"id", "unit", "registeredCount", "availableSeats", "cancelDeadline", "version", "myRegistration"} {
				if _, ok := out[key]; !ok {
					t.Fatalf("missing %s: %v", key, out)
				}
			}
			if out["status"] != "PENDING_APPROVAL" || out["myRegistration"] != nil {
				t.Fatal(out)
			}
			body := `{"reason":"` + strings.Repeat("ế", 500) + `"}`
			expected := "REJECTED"
			if decision == "approve" {
				body = `{"comment":"Looks good"}`
				expected = "PUBLISHED"
			}
			out = request(t, adminID, "POST", "/events/"+e+"/"+decision, body, 200)["data"].(map[string]any)
			if out["id"] != e || out["status"] != expected || out["unit"] == nil {
				t.Fatal(out)
			}
			typ := "EVENT_REJECTED"
			if decision == "approve" {
				typ = "EVENT_APPROVED"
			}
			if got := scalar(`SELECT count(*)::text FROM notifications WHERE event_id=$1 AND user_id=$2 AND type::text=$3 AND email_status='PENDING'`, e, owner, typ); got != "1" {
				t.Fatalf("notification count=%s", got)
			}
		}
		logs := request(t, owner, "GET", "/events/"+e+"/approval-logs", "", 200)["data"].([]any)
		if logs[3].(map[string]any)["comment"] != "Looks good" {
			t.Fatal(logs)
		}
		if got := scalar(`SELECT metadata->>'comment' FROM activity_logs WHERE event_id=$1 AND action='APPROVE'`, e); got != "Looks good" {
			t.Fatal(got)
		}
		if got := scalar(`SELECT count(*)::text FROM notifications WHERE event_id=$1`, e); got != "2" {
			t.Fatal(got)
		}
	})
	t.Run("precise approval errors and optional body", func(t *testing.T) {
		e := event("PENDING_APPROVAL")
		err := request(t, student, "POST", "/events/"+e+"/reject", `{"reason":"valid"}`, 403)["error"].(map[string]any)
		if err["code"] != "FORBIDDEN" {
			t.Fatal(err)
		}
		// Owner+Admin uses the same caller and must fail self approval, including rejection.
		exec(`UPDATE events SET created_by=$2 WHERE id=$1`, e, adminID)
		for _, decision := range []string{"approve", "reject"} {
			err = request(t, adminID, "POST", "/events/"+e+"/"+decision, `{"reason":"valid"}`, 403)["error"].(map[string]any)
			if err["code"] != "SELF_APPROVAL_NOT_ALLOWED" {
				t.Fatal(err)
			}
		}
		exec(`UPDATE events SET created_by=$2,start_at=now()-interval '1 hour',end_at=now()+interval '1 hour',registration_opens_at=now()-interval '3 hours',registration_closes_at=now()-interval '2 hours' WHERE id=$1`, e, owner)
		err = request(t, adminID, "POST", "/events/"+e+"/approve", "", 422)["error"].(map[string]any)
		if err["code"] != "VALIDATION_ERROR" {
			t.Fatal(err)
		}
		e = event("PENDING_APPROVAL")
		for _, body := range []string{`{"comment":"` + strings.Repeat("a", 501) + `"}`, `{"comment":null}`, `{"comment":5}`, `{`} {
			request(t, adminID, "POST", "/events/"+e+"/approve", body, 400)
		}
		request(t, adminID, "POST", "/events/"+e+"/reject", `{"reason":"  "}`, 400)
		request(t, adminID, "POST", "/events/"+e+"/reject", `{"reason":"`+strings.Repeat("ế", 501)+`"}`, 400)
		request(t, adminID, "POST", "/events/"+e+"/approve", "", 200)
	})
	t.Run("oldest pending submission before event start", func(t *testing.T) {
		exec(`UPDATE events SET status='DRAFT' WHERE status='PENDING_APPROVAL'`)
		first, second := event("PENDING_APPROVAL"), event("PENDING_APPROVAL")
		exec(`UPDATE events SET updated_at=now()-interval '2 hours',start_at=now()+interval '5 days',end_at=now()+interval '6 days' WHERE id=$1`, first)
		exec(`UPDATE events SET updated_at=now()-interval '1 hour' WHERE id=$1`, second)
		rows := request(t, adminID, "GET", "/approvals/pending?pageSize=1", "", 200)["data"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["id"] != first {
			t.Fatal(rows)
		}
		rows = request(t, adminID, "GET", "/approvals/pending?pageSize=1&page=2", "", 200)["data"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["id"] != second {
			t.Fatal(rows)
		}
		request(t, adminID, "GET", "/approvals/pending?page=2147483647&pageSize=100", "", 400)
	})
	t.Run("inactive unit managed scope", func(t *testing.T) {
		exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
		out := request(t, owner, "GET", "/manage/events", "", 200)
		if len(out["data"].([]any)) != 0 || out["meta"].(map[string]any)["totalItems"] != float64(0) {
			t.Fatal(out)
		}
		out = request(t, adminID, "GET", "/manage/events", "", 200)
		if len(out["data"].([]any)) == 0 {
			t.Fatal(out)
		}
		exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
	})
	t.Run("decision notification and audit failures roll back", func(t *testing.T) {
		for _, table := range []string{"notifications", "activity_logs"} {
			exec(`CREATE FUNCTION fail_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced decision failure'; END $$`)
			exec(`CREATE TRIGGER fail_decision BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION fail_decision()`)
			for _, decision := range []string{"approve", "reject"} {
				e := event("PENDING_APPROVAL")
				request(t, adminID, "POST", "/events/"+e+"/"+decision, `{"reason":"Valid","comment":"Valid"}`, 500)
				if got := scalar(`SELECT status::text||':'||version::text FROM events WHERE id=$1`, e); got != "PENDING_APPROVAL:1" {
					t.Fatal(got)
				}
				if got := scalar(`SELECT ((SELECT count(*) FROM notifications WHERE event_id=$1)+(SELECT count(*) FROM activity_logs WHERE event_id=$1))::text`, e); got != "0" {
					t.Fatal(got)
				}
			}
			exec(`DROP TRIGGER fail_decision ON ` + table + `; DROP FUNCTION fail_decision()`)
		}
	})
}
