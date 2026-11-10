//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	eventpg "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/postgres"
	eventapp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	notifhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	regpg "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/postgres"
	regapp "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
	t.Cleanup(func() { admin.Close(ctx) })
	name := fmt.Sprintf("ticket_notification_test_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
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
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestNotificationFlow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	user := func() string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn','Test') RETURNING id::text`)
	}
	owner, student, other, empty := user(), user(), user(), user()
	exec(`UPDATE users SET student_code=substr(gen_random_uuid()::text,1,10) WHERE id IN ($1,$2)`, student, other)
	unit := scalar(`INSERT INTO units(code,name,type) VALUES('NOTIF','Notifications','FACULTY') RETURNING id::text`)
	exec(`INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, owner, unit)
	event := scalar(`INSERT INTO events(unit_id,created_by,title,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,waitlist_limit,status) VALUES($1,$2,'Notification flow','Campus',now()+interval '3 days',now()+interval '4 days',now()-interval '1 day',now()+interval '2 days',1,10,'PUBLISHED') RETURNING id::text`, unit, owner)
	service := a.New(New(pool))
	e := echo.New()
	// Authentication itself is covered by auth integration tests. This middleware
	// selects the authenticated test identity; clients cannot choose it in production.
	notifhttp.New(service).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			actor := c.Request().Header.Get("X-Test-Actor")
			if actor == "" {
				return c.NoContent(401)
			}
			c.Set("auth.profile", auth.Profile{ID: actor})
			return next(c)
		}
	})
	request := func(actor, method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/me/notifications"+path, strings.NewReader(body))
		req.Header.Set("X-Test-Actor", actor)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	type inbox struct {
		Data []a.Notification `json:"data"`
		Meta struct {
			Page, PageSize                      int32
			TotalItems, TotalPages, UnreadCount int64
		} `json:"meta"`
	}
	list := func(actor, path string) inbox {
		t.Helper()
		rec := request(actor, "GET", path, "", 200)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable inbox")
		}
		var out inbox
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	read := func(actor, body string) a.ReadResult {
		t.Helper()
		rec := request(actor, "POST", "/read", body, 200)
		var out struct {
			Data a.ReadResult `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	request("", "GET", "", "", 401)
	request("", "POST", "/read", `{"all":true}`, 401)
	if out := list(empty, ""); out.Data == nil || len(out.Data) != 0 || out.Meta.TotalItems != 0 || out.Meta.UnreadCount != 0 {
		t.Fatalf("empty inbox: %+v", out)
	}
	regs := regapp.New(regpg.New(pool))
	first, err := regs.Register(ctx, other, event)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := regs.Register(ctx, student, event)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != "WAITLISTED" {
		t.Fatal(waiting)
	}
	if _, err := regs.Cancel(ctx, other, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	out := list(student, "")
	if len(out.Data) != 2 || out.Meta.UnreadCount != 2 {
		t.Fatalf("promoted inbox: %+v", out)
	}
	var promotedID string
	for _, n := range out.Data {
		if n.Type == "PROMOTED" {
			promotedID = n.ID
			if n.EventID == nil || *n.EventID != event || n.ReadAt != nil {
				t.Fatal(n)
			}
		}
	}
	if promotedID == "" {
		t.Fatal("missing PROMOTED")
	}
	events := eventapp.New(eventpg.New(pool), kernel.SystemClock{})
	if _, err := events.Patch(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, event, 1, func(f domain.Fields) (domain.Fields, error) { f.Location = "New campus"; return f, nil }); err != nil {
		t.Fatal(err)
	}
	out = list(student, "?unread=true&pageSize=1")
	if len(out.Data) != 1 || out.Data[0].Type != "EVENT_CHANGED" || out.Meta.TotalItems != 3 || out.Meta.TotalPages != 3 || out.Meta.UnreadCount != 3 {
		t.Fatalf("changed inbox: %+v", out)
	}
	changedID := out.Data[0].ID
	otherID := scalar(`SELECT id::text FROM notifications WHERE user_id=$1 LIMIT 1`, other)
	body := fmt.Sprintf(`{"ids":[%q,%q,%q,%q]}`, promotedID, promotedID, otherID, "00000000-0000-0000-0000-000000000000")
	if r := read(student, body); r.UpdatedCount != 1 || r.UnreadCount != 2 {
		t.Fatal(r)
	}
	readAt := scalar(`SELECT read_at::text FROM notifications WHERE id=$1`, promotedID)
	if r := read(student, body); r.UpdatedCount != 0 || r.UnreadCount != 2 {
		t.Fatal("not idempotent", r)
	}
	if now := scalar(`SELECT read_at::text FROM notifications WHERE id=$1`, promotedID); now != readAt {
		t.Fatal("readAt changed")
	}
	if state := scalar(`SELECT (read_at IS NULL)::text FROM notifications WHERE id=$1`, otherID); state != "true" {
		t.Fatal("other user's notification modified")
	}
	if state := scalar(`SELECT email_status::text||':'||email_attempts::text FROM notifications WHERE id=$1`, promotedID); state != "PENDING:0" {
		t.Fatal("outbox modified", state)
	}
	if filtered := list(student, "?unread=true"); len(filtered.Data) != 2 || filtered.Meta.TotalItems != 2 || filtered.Meta.UnreadCount != 2 {
		t.Fatalf("unread filter: %+v", filtered)
	}
	// Concurrent acknowledgements must count a notification exactly once.
	var wg sync.WaitGroup
	results := make(chan a.ReadResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := service.MarkRead(ctx, student, a.ReadOptions{IDs: []string{changedID}})
			results <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var updates int64
	for r := range results {
		updates += r.UpdatedCount
	}
	if updates != 1 {
		t.Fatal("duplicate acknowledgement", updates)
	}
	if r := read(student, `{"all":true}`); r.UpdatedCount != 1 || r.UnreadCount != 0 {
		t.Fatal(r)
	}
	if r := read(student, `{"all":true}`); r.UpdatedCount != 0 || r.UnreadCount != 0 {
		t.Fatal(r)
	}
	if out := list(student, "?unread=true"); out.Data == nil || len(out.Data) != 0 || out.Meta.TotalItems != 0 {
		t.Fatalf("all read: %+v", out)
	}
	// Cancel the real event; registered users receive the committed notification.
	cancelled, err := events.Cancel(ctx, eventapp.Actor{ID: owner, IsOrganizer: true}, event, 2, "Venue unavailable")
	if err != nil || cancelled.AffectedCount != 1 || cancelled.Event.Status != "CANCELLED" {
		t.Fatalf("cancel event: %+v %v", cancelled, err)
	}
	cancelledID := scalar(`SELECT id::text FROM notifications WHERE user_id=$1 AND event_id=$2 AND type='EVENT_CANCELLED'`, student, event)
	if out := list(student, "?unread=true"); len(out.Data) != 1 || out.Data[0].ID != cancelledID || out.Data[0].Type != "EVENT_CANCELLED" {
		t.Fatal(out)
	}
	// Equal timestamps must still produce stable, disjoint pages.
	exec(`UPDATE notifications SET created_at='2026-01-01T00:00:00Z' WHERE user_id=$1`, student)
	all := list(student, "")
	seen := map[string]bool{}
	for page := 1; page <= len(all.Data); page++ {
		p := list(student, fmt.Sprintf("?page=%d&pageSize=1", page))
		if len(p.Data) != 1 || seen[p.Data[0].ID] || p.Data[0].ID != all.Data[page-1].ID {
			t.Fatal("unstable pagination", p)
		}
		seen[p.Data[0].ID] = true
	}
	if p := list(student, "?page=100"); p.Data == nil || len(p.Data) != 0 || p.Meta.TotalItems != 4 {
		t.Fatal(p)
	}
	// A database failure must roll back every read marker.
	exec(`CREATE FUNCTION fail_read() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private failure'; END $$; CREATE TRIGGER fail_read BEFORE UPDATE ON notifications FOR EACH ROW EXECUTE FUNCTION fail_read()`)
	request(student, "POST", "/read", `{"all":true}`, 500)
	if out := list(student, "?unread=true"); len(out.Data) != 1 || out.Data[0].ID != cancelledID {
		t.Fatal("failed write escaped transaction", out)
	}
}
