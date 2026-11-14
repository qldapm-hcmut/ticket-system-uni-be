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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	eventhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type renderSequenceClock struct{ times []time.Time }

func (c *renderSequenceClock) Now() time.Time {
	at := c.times[0]
	c.times = c.times[1:]
	return at
}

func TestManagementLists(t *testing.T) {
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
	name := fmt.Sprintf("ticket_lists_test_%d", time.Now().UnixNano())
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
	schema, err := os.ReadFile("../../../../db/migrations/0001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(schema))
	user := func() string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn','Test') RETURNING id::text`)
	}
	owner, adminID, outsider := user(), user(), user()
	exec(`UPDATE users SET is_admin=true WHERE id=$1`, adminID)
	unit := scalar(`INSERT INTO units(code,name,type) VALUES('LISTS','Lists','FACULTY') RETURNING id::text`)
	exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, owner, unit)
	event := scalar(`INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,status) VALUES($1,$2,'Lists','Campus',now()+interval '3 days',now()+interval '4 days',now()-interval '1 day',now()+interval '2 days',10,'DRAFT') RETURNING id::text`, unit, owner)
	store := New(pool)
	// Both event list paths must preserve row order, empty arrays, nullable
	// fields and the render-time boundary even when each row reads a new time.
	t.Run("catalog and managed rendering", func(t *testing.T) {
		start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 2; i++ {
			exec(`INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,status,waitlist_count) VALUES($1,$2,$3,'Campus',$4,$5,$6,$7,10,'PUBLISHED',2)`,
				unit, owner, fmt.Sprintf("Render %d", i), start, start.Add(time.Hour), start.Add(-time.Hour), start.Add(-time.Minute))
		}
		for _, managed := range []bool{false, true} {
			clock := &renderSequenceClock{times: []time.Time{start.Add(-time.Nanosecond), start}}
			// Catalog first reads the filter time, then reads once per row.
			if !managed {
				clock.times = append([]time.Time{start.Add(-time.Hour)}, clock.times...)
			}
			reader := NewReader(pool, clock)
			read := func(search string) ([]map[string]any, int64, error) {
				filter := a.Filter{Search: search, Page: 1, PageSize: 20}
				if managed {
					return reader.Managed(ctx, a.Actor{ID: owner}, filter)
				}
				return reader.Catalog(ctx, a.Actor{ID: owner}, a.CatalogFilter{Filter: filter})
			}
			rows, count, err := read("Render")
			if err != nil || count != 2 || len(rows) != 2 || len(clock.times) != 0 {
				t.Fatalf("managed=%v rows=%v count=%d err=%v remaining times=%v", managed, rows, count, err, clock.times)
			}
			if rows[0]["waitlistCount"] != int32(2) || rows[1]["waitlistCount"] != int32(0) || rows[0]["description"] != nil || rows[0]["waitlistLimit"] != nil || rows[0]["id"].(string) >= rows[1]["id"].(string) {
				t.Fatalf("render ordering/boundary/nullability changed: %v", rows)
			}
			clock.times = []time.Time{start}
			rows, count, err = read("missing")
			if err != nil || count != 0 || rows == nil || len(rows) != 0 {
				t.Fatalf("empty list: %v %d %v", rows, count, err)
			}
		}
		exec(`DELETE FROM events WHERE title LIKE 'Render %'`)
	})
	approvals := a.NewApprovalService(store, kernel.SystemClock{})
	actor := a.Actor{ID: owner, IsOrganizer: true}
	schoolAdmin := a.Actor{ID: adminID, IsAdmin: true}
	if _, err := approvals.Submit(ctx, actor, event, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Reject(ctx, schoolAdmin, event, 2, "Need details"); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Submit(ctx, actor, event, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Approve(ctx, schoolAdmin, event, 4, ""); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		u := user()
		exec(`UPDATE users SET full_name=$2,student_code=$3 WHERE id=$1`, u, fmt.Sprintf("Student %d", i), fmt.Sprintf("M00%d", i))
		exec(`INSERT INTO registrations(event_id,student_id,status,checkin_code) VALUES($1,$2,'WAITLISTED',$3)`, event, u, fmt.Sprintf("CODE000%d", i))
	}
	registered := user()
	exec(`UPDATE users SET student_code='REG001' WHERE id=$1`, registered)
	regID := scalar(`INSERT INTO registrations(event_id,student_id,status,checkin_code,checked_in_at,checked_in_by,checkin_method) VALUES($1,$2,'REGISTERED','CHECK001',now(),$3,'QR') RETURNING id::text`, event, registered, owner)
	svc := a.NewManagementService(store)
	e := echo.New()
	eventhttp.NewManagementHandler(svc).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			user := c.Request().Header.Get("X-Test-Actor")
			if user == "" {
				return c.NoContent(401)
			}
			c.Set("auth.profile", auth.Profile{ID: user})
			return next(c)
		}
	})
	type response struct {
		Data []map[string]any
		Meta struct{ TotalItems, TotalPages int64 }
	}
	request := func(actor, path string, status int) response {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/events/"+event+"/"+path, nil)
		req.Header.Set("X-Test-Actor", actor)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var out response
		if status == 200 {
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cacheable PII")
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Data == nil {
				t.Fatal("null array")
			}
		}
		return out
	}
	for _, kind := range []string{"registrations", "check-ins", "approval-logs"} {
		request("", kind, 401)
		request(outsider, kind, 404)
		request(adminID, kind, 200)
	}
	logs := request(owner, "approval-logs", 200)
	if len(logs.Data) != 4 || logs.Data[1]["comment"] != "Need details" || logs.Data[3]["action"] != "APPROVE" {
		t.Fatal(logs)
	}
	rows := request(owner, "registrations?status=WAITLISTED&pageSize=1&page=2", 200)
	if len(rows.Data) != 1 || rows.Meta.TotalItems != 3 || rows.Data[0]["waitlistPosition"] != float64(2) {
		t.Fatal(rows)
	}
	rows = request(owner, "registrations?status=WAITLISTED&q=M003", 200)
	if len(rows.Data) != 1 || rows.Data[0]["waitlistPosition"] != float64(3) {
		t.Fatal("rank computed after filtering", rows)
	}
	rows = request(owner, "registrations?q=Student%202", 200)
	if len(rows.Data) != 1 {
		t.Fatal(rows)
	}
	rows = request(owner, "registrations?q=%25", 200)
	if len(rows.Data) != 0 {
		t.Fatal("wildcard search", rows)
	}
	checks := request(adminID, "check-ins?method=QR", 200)
	if len(checks.Data) != 1 || checks.Data[0]["registrationId"] != regID {
		t.Fatal(checks)
	}
	if rows := request(owner, "check-ins?method=MANUAL", 200); len(rows.Data) != 0 || rows.Meta.TotalItems != 0 {
		t.Fatal(rows)
	}
	exec(`UPDATE registrations SET checked_in_at=NULL,checked_in_by=NULL,checkin_method=NULL WHERE id=$1`, regID)
	if rows := request(owner, "check-ins", 200); len(rows.Data) != 0 {
		t.Fatal("undo not reflected", rows)
	}
	exec(`UPDATE events SET start_at=now()-interval '1 hour',registration_closes_at=now()-interval '2 hours' WHERE id=$1`, event)
	rows = request(owner, "registrations?status=EXPIRED", 200)
	if len(rows.Data) != 3 || rows.Meta.TotalItems != 3 || rows.Data[0]["waitlistPosition"] != nil {
		t.Fatal(rows)
	}
	if rows := request(owner, "registrations?status=WAITLISTED", 200); len(rows.Data) != 0 {
		t.Fatal(rows)
	}
	for _, path := range []string{"registrations?page=0", "registrations?pageSize=101", "registrations?status=BAD", "check-ins?method=BAD", "registrations?page=1&page=2"} {
		request(owner, path, 400)
	}
	exec(`UPDATE units SET is_active=false WHERE id=$1`, unit)
	request(owner, "registrations", 404)
	request(adminID, "registrations", 200)
	exec(`UPDATE users SET is_active=false WHERE id=$1`, adminID)
	request(adminID, "approval-logs", 404)
	if _, err := svc.List(ctx, owner, "invalid", "registrations", a.ManagementFilter{Page: 1, PageSize: 20}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	// Audit failure rolls back the transition, not merely the log insert.
	exec(`UPDATE units SET is_active=true WHERE id=$1`, unit)
	exec(`UPDATE events SET status='DRAFT' WHERE id=$1`, event)
	exec(`CREATE FUNCTION fail_approval_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit failed'; END $$; CREATE TRIGGER fail_approval_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION fail_approval_audit()`)
	if _, err := approvals.Submit(ctx, actor, event, 5); err == nil {
		t.Fatal("audit error ignored")
	}
	if status := scalar(`SELECT status::text FROM events WHERE id=$1`, event); status != "DRAFT" {
		t.Fatal("partial approval commit", status)
	}
}
