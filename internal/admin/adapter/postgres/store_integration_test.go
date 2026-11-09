//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	adminhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/adapter/http"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/application"
	authhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/http"
	authpg "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/postgres"
)

func isolatedDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL required")
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ticket_admin_test_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
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
	if _, err = pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Token claims contain only identity. Authorization always reads the database.
type identityToken struct{}

func (identityToken) VerifyAccessToken(raw string) (string, error) { return raw, nil }
func TestAdminFlow(t *testing.T) {
	ctx := context.Background()
	pool := isolatedDB(t)
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
	user := func(name string) string {
		return scalar(`INSERT INTO users(email,full_name) VALUES(gen_random_uuid()::text||'@hcmut.edu.vn',$1) RETURNING id::text`, name)
	}
	admin, student, other := user("Admin"), user("Student Alpha"), user("Staff")
	exec(`UPDATE users SET is_admin=true WHERE id=$1`, admin)
	exec(`UPDATE users SET student_code='2210001',is_admin=true WHERE id=$1`, student)
	svc := a.New(New(pool))
	unit, err := svc.CreateUnit(ctx, admin, a.UnitCreate{Code: "CSE", Name: "Faculty", Type: "FACULTY"})
	if err != nil {
		t.Fatal(err)
	}
	if scalar(`SELECT count(*)::text FROM activity_logs WHERE entity_id=$1 AND action='CREATE_UNIT' AND actor_id=$2`, unit.ID, admin) != "1" {
		t.Fatal("missing create audit")
	}
	if _, err = svc.CreateUnit(ctx, admin, a.UnitCreate{Code: "CSE", Name: "Duplicate", Type: "CLUB"}); !errors.Is(err, a.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, actor := range []string{other} {
		if _, err := svc.CreateUnit(ctx, actor, a.UnitCreate{Code: "NO", Name: "No", Type: "CLUB"}); !errors.Is(err, a.ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := svc.SetMembers(ctx, actor, unit.ID, []a.Member{}); !errors.Is(err, a.ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := svc.ListUsers(ctx, actor, a.Filter{Page: 1, PageSize: 20}); !errors.Is(err, a.ErrForbidden) {
			t.Fatal(err)
		}
		if _, err := svc.UpdateUser(ctx, actor, student, false); !errors.Is(err, a.ErrForbidden) {
			t.Fatal(err)
		}
	}
	initial := []a.Member{{UserID: student, MemberRole: "ORGANIZER"}}
	if out, err := svc.SetMembers(ctx, admin, unit.ID, initial); err != nil || len(out) != 1 {
		t.Fatalf("initial members: %+v %v", out, err)
	}
	missing := "00000000-0000-0000-0000-000000000000"
	// A valid insert followed by a missing FK must restore the deleted old set,
	// roll back the first insert, and leave no audit for the failed replacement.
	if _, err := svc.SetMembers(ctx, admin, unit.ID, []a.Member{{UserID: other, MemberRole: "ORGANIZER"}, {UserID: missing, MemberRole: "ORGANIZER"}}); !errors.Is(err, a.ErrNotFound) {
		t.Fatalf("missing referenced user: %v", err)
	}
	if scalar(`SELECT user_id::text FROM unit_memberships WHERE unit_id=$1`, unit.ID) != student || scalar(`SELECT count(*)::text FROM activity_logs WHERE action='SET_UNIT_MEMBERS'`) != "1" {
		t.Fatal("replacement did not roll back")
	}
	if _, err := svc.SetMembers(ctx, admin, missing, []a.Member{}); !errors.Is(err, a.ErrNotFound) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, q string
		total   int64
	}{{"STUDENT", "", 1}, {"ORGANIZER", "", 1}, {"ADMIN", "", 2}, {"", "ALPHA", 1}, {"", "2210001", 1}, {"", "@hcmut.edu.vn", 3}, {"", "%", 0}} {
		out, err := svc.ListUsers(ctx, admin, a.Filter{Role: tc.role, Q: tc.q, Page: 1, PageSize: 1})
		if err != nil || out.TotalItems != tc.total {
			t.Fatalf("filter %+v: %+v %v", tc, out, err)
		}
		if tc.role == "STUDENT" && (len(out.Items) != 1 || len(out.Items[0].Memberships) != 1 || !out.Items[0].IsAdmin || out.Items[0].StudentCode != "2210001") {
			t.Fatalf("overlapping roles: %+v", out)
		}
	}
	out, err := svc.ListUsers(ctx, admin, a.Filter{Page: 100, PageSize: 1})
	if err != nil || len(out.Items) != 0 || out.TotalItems != 3 {
		t.Fatalf("empty page: %+v %v", out, err)
	}
	p, err := svc.UpdateUser(ctx, admin, student, false)
	if err != nil || p.IsActive || !p.IsAdmin || p.StudentCode != "2210001" || len(p.Memberships) != 1 {
		t.Fatalf("active-only update: %+v %v", p, err)
	}
	if scalar(`SELECT metadata::text FROM activity_logs WHERE action='UPDATE_USER'`) != `{"after": {"isActive": false}, "before": {"isActive": true}}` {
		t.Fatal("incorrect user audit")
	}
	if _, err := svc.UpdateUser(ctx, admin, missing, false); !errors.Is(err, a.ErrNotFound) {
		t.Fatal(err)
	}
	// Real authentication middleware must observe revocation/disable on the next request.
	e := echo.New()
	authHandler := authhttp.New(nil, identityToken{}, authpg.NewProfileReader(pool))
	authHandler.Register(e)
	adminhttp.New(svc).Register(e, authHandler.Authenticate)
	request := func(method, path, actor, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+actor)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/me", student, ""); w.Code != 401 {
		t.Fatalf("disabled next request: %d %s", w.Code, w.Body.String())
	}
	if _, err = svc.UpdateUser(ctx, admin, student, true); err != nil {
		t.Fatal(err)
	}
	if w := request("PATCH", "/api/v1/admin/users/"+student, admin, `{"isActive":false,"studentCode":"999","isAdmin":false}`); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	if scalar(`SELECT (is_active AND is_admin AND student_code='2210001')::text FROM users WHERE id=$1`, student) != "true" {
		t.Fatal("privilege fields modified")
	}
	if w := request("PUT", "/api/v1/admin/units/"+unit.ID+"/members", admin, `{"members":[]}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal(w.Body.String())
	}
	w := request("GET", "/api/v1/me", student, "")
	var me struct {
		Data struct {
			Roles []string
			Units []any
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != 200 || len(me.Data.Units) != 0 || strings.Contains(w.Body.String(), "ORGANIZER") {
		t.Fatalf("revoked membership stale: %s %v", w.Body.String(), err)
	}
	if scalar(`SELECT metadata->'before'->0->>'userId' FROM activity_logs WHERE action='SET_UNIT_MEMBERS' ORDER BY id DESC LIMIT 1`) != student || scalar(`SELECT (metadata->'after')::text FROM activity_logs WHERE action='SET_UNIT_MEMBERS' ORDER BY id DESC LIMIT 1`) != "[]" {
		t.Fatal("missing replacement before/after audit")
	}
	exec(`UPDATE users SET is_admin=false WHERE id=$1`, admin)
	if w := request("GET", "/api/v1/admin/users", admin, ""); w.Code != 403 {
		t.Fatalf("revoked admin: %d", w.Code)
	}
	exec(`UPDATE users SET is_admin=true,is_active=false WHERE id=$1`, admin)
	if _, err := svc.ListUsers(ctx, admin, a.Filter{Page: 1, PageSize: 20}); !errors.Is(err, a.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if w := request("GET", "/api/v1/admin/users", admin, ""); w.Code != 401 {
		t.Fatalf("disabled admin: %d", w.Code)
	}
	exec(`UPDATE users SET is_active=true WHERE id=$1`, admin)
	// Audit failure is a transaction failure for all three mutations.
	exec(`CREATE FUNCTION fail_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END $$; CREATE TRIGGER fail_admin_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION fail_admin_audit()`)
	if _, err := svc.CreateUnit(ctx, admin, a.UnitCreate{Code: "FAIL", Name: "Fail", Type: "CLUB"}); err == nil {
		t.Fatal("audit failure accepted")
	}
	if scalar(`SELECT count(*)::text FROM units WHERE code='FAIL'`) != "0" {
		t.Fatal("create survived failed audit")
	}
	if _, err := svc.UpdateUser(ctx, admin, student, false); err == nil {
		t.Fatal("audit failure accepted")
	}
	if scalar(`SELECT is_active::text FROM users WHERE id=$1`, student) != "true" {
		t.Fatal("update survived failed audit")
	}
	if _, err := svc.SetMembers(ctx, admin, unit.ID, initial); err == nil {
		t.Fatal("audit failure accepted")
	}
	if scalar(`SELECT count(*)::text FROM unit_memberships WHERE unit_id=$1`, unit.ID) != "0" {
		t.Fatal("membership survived failed audit")
	}
	exec(`DROP TRIGGER fail_admin_audit ON activity_logs`)
	for _, initiallyEmpty := range []bool{true, false} {
		t.Run(fmt.Sprintf("concurrent_replacements/initially_empty=%t", initiallyEmpty), func(t *testing.T) {
			testConcurrentReplacements(t, pool, svc, admin, unit.ID, student, other, initiallyEmpty)
		})
	}
}

func TestAdminUserPagination(t *testing.T) {
	ctx := context.Background()
	pool := isolatedDB(t)
	// Insert equal names in reverse ID order so insertion order cannot satisfy
	// the required (full_name, id) ordering accidentally.
	ids := []string{
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1",
	}
	admin := ids[0]
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,full_name,is_admin) VALUES($1,$2,'Same Name',$3)`, id, id+"@hcmut.edu.vn", id == admin); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(ids)
	svc := a.New(New(pool))
	// Repeat both pages to check stability as well as completeness/no overlap.
	for attempt := 0; attempt < 3; attempt++ {
		var got []string
		seen := make(map[string]bool)
		for page := int64(1); page <= 2; page++ {
			out, err := svc.ListUsers(ctx, admin, a.Filter{Page: page, PageSize: 2})
			if err != nil || out.TotalItems != 4 || len(out.Items) != 2 {
				t.Fatalf("page %d: %+v %v", page, out, err)
			}
			for _, u := range out.Items {
				if seen[u.ID] {
					t.Fatalf("pages overlap at user %s", u.ID)
				}
				seen[u.ID] = true
				got = append(got, u.ID)
			}
		}
		if !reflect.DeepEqual(got, ids) {
			t.Fatalf("equal-name ID tie-breaker: got %v, want %v", got, ids)
		}
	}
}

func testConcurrentReplacements(t *testing.T, pool *pgxpool.Pool, svc *a.Service, admin, unit, student, other string, initiallyEmpty bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	initial := []a.Member{}
	before := []map[string]string{}
	if !initiallyEmpty {
		initial = append(initial, a.Member{UserID: student, MemberRole: "ORGANIZER"})
		before = append(before, map[string]string{"userId": student, "memberRole": "ORGANIZER"})
	}
	if _, err := svc.SetMembers(ctx, admin, unit, initial); err != nil {
		t.Fatal(err)
	}
	var lastID int64
	if err := pool.QueryRow(ctx, `SELECT max(id) FROM activity_logs`).Scan(&lastID); err != nil {
		t.Fatal(err)
	}

	// Both PUTs must reach the parent lock before it is released. This also
	// exercises empty replacements, which have no child rows to lock.
	parent, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Rollback(context.Background())
	if _, err := parent.Exec(ctx, `SELECT id FROM units WHERE id=$1 FOR UPDATE`, unit); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, replacement := range [][]a.Member{{}, {{UserID: other, MemberRole: "ORGANIZER"}}} {
		go func(in []a.Member) {
			_, err := svc.SetMembers(ctx, admin, unit, in)
			errs <- err
		}(replacement)
	}

	waitCtx, stopWaiting := context.WithTimeout(ctx, 5*time.Second)
	defer stopWaiting()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err := pool.QueryRow(waitCtx, `
			SELECT count(*) FROM pg_stat_activity a
			WHERE a.datname = current_database()
			  AND a.wait_event_type = 'Lock'
			  AND strpos(a.query, '-- name: AdminLockUnit :one') > 0
			  AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid AND NOT l.granted)
			  AND cardinality(pg_blocking_pids(a.pid)) > 0`).Scan(&waiting)
		if err != nil {
			t.Fatalf("observe both replacements waiting on the parent unit lock: %v", err)
		}
		if waiting == 2 {
			break
		}
		select {
		case err := <-errs:
			t.Fatalf("replacement finished before parent lock release: %v", err)
		case <-waitCtx.Done():
			t.Fatalf("only %d/2 replacements observed waiting: %v", waiting, waitCtx.Err())
		case <-ticker.C:
		}
	}
	if err := parent.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	rows, err := pool.Query(ctx, `SELECT metadata FROM activity_logs WHERE entity_id=$1 AND action='SET_UNIT_MEMBERS' AND id>$2 ORDER BY id`, unit, lastID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count, empty, nonempty := 0, 0, 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var audit struct{ Before, After []map[string]string }
		if err := json.Unmarshal(raw, &audit); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(audit.Before, before) {
			t.Fatalf("incoherent audit chain: before=%v, want %v", audit.Before, before)
		}
		switch {
		case reflect.DeepEqual(audit.After, []map[string]string{}):
			empty++
		case reflect.DeepEqual(audit.After, []map[string]string{{"userId": other, "memberRole": "ORGANIZER"}}):
			nonempty++
		default:
			t.Fatalf("unexpected replacement audit: %s", raw)
		}
		before = audit.After
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 || empty != 1 || nonempty != 1 {
		t.Fatalf("replacement audits: total=%d empty=%d nonempty=%d", count, empty, nonempty)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('userId',user_id,'memberRole',member_role) ORDER BY user_id), '[]'::jsonb) FROM unit_memberships WHERE unit_id=$1`, unit).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var final []map[string]string
	if err := json.Unmarshal(raw, &final); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(final, before) {
		t.Fatalf("final membership %v does not match last audit %v", final, before)
	}
}
