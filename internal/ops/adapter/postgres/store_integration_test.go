//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/ops/application"
)

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal("connect test database")
	}
	name := fmt.Sprintf("ops_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatal("create isolated database")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("parse test database config")
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect isolated database")
	}
	t.Cleanup(func() { pool.Close(); _, _ = root.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); root.Close() })
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "db", "migrations", "0001_initial_schema.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	return pool
}
func scalar(t *testing.T, p *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func execute(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func request(rows ...a.Student) a.ImportRequest {
	return a.ImportRequest{Attribution: a.Attribution{Operator: "dev-17", Ticket: "OPS-1"}, Students: rows}
}
func student(email, code string) a.Student {
	return a.Student{Email: email, FullName: "Roster Name", StudentCode: code, Row: 2}
}
func admin(action, id string) a.AdminRequest {
	return a.AdminRequest{Action: action, UserID: id, Attribution: a.Attribution{Operator: "dev-17", Ticket: "OPS-1"}}
}

func TestImportPreservesLinkedIdentityAndOverlappingRoles(t *testing.T) {
	p := database(t)
	ctx := context.Background()
	s := New(p)
	id := scalar(t, p, `INSERT INTO users(email,full_name,google_sub,is_admin,is_active) VALUES('existing@hcmut.edu.vn','Existing Name','google-original',true,false) RETURNING id::text`)
	unit := scalar(t, p, `INSERT INTO units(code,name,type) VALUES('TEST','Test','CLUB') RETURNING id::text`)
	execute(t, p, `INSERT INTO unit_memberships(user_id,unit_id,member_role) VALUES($1,$2,'ORGANIZER')`, id, unit)
	execute(t, p, `INSERT INTO auth_sessions(user_id,refresh_token_hash,expires_at) VALUES($1,'hash',now()+interval '1 day')`, id)
	req := request(student("existing@hcmut.edu.vn", "00123"), student("new@hcmut.edu.vn", "00234"))
	out, err := s.Import(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if out != (a.ImportResult{Created: 1, Enriched: 1}) {
		t.Fatalf("result=%+v", out)
	}
	if scalar(t, p, `SELECT (id=$1 AND full_name='Existing Name' AND google_sub='google-original' AND is_admin AND NOT is_active AND student_code='00123')::text FROM users WHERE email='existing@hcmut.edu.vn'`, id) != "true" {
		t.Fatal("identity changed")
	}
	if scalar(t, p, `SELECT count(*)::text FROM unit_memberships WHERE user_id=$1`, id) != "1" || scalar(t, p, `SELECT count(*)::text FROM auth_sessions WHERE user_id=$1`, id) != "1" {
		t.Fatal("references lost")
	}
	if scalar(t, p, `SELECT (google_sub IS NULL AND NOT is_admin AND is_active AND full_name='Roster Name')::text FROM users WHERE email='new@hcmut.edu.vn'`) != "true" {
		t.Fatal("unsafe new identity")
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs WHERE action='UPDATE_USER' AND actor_id IS NULL AND metadata->>'operator'='dev-17' AND metadata->>'ticket'='OPS-1'`) != "2" {
		t.Fatal("missing audit")
	}
	metadata := scalar(t, p, `SELECT string_agg(metadata::text,'') FROM activity_logs`)
	for _, secret := range []string{"existing@", "new@", "00123", "00234", "Roster Name", "google-original"} {
		if strings.Contains(metadata, secret) {
			t.Fatal("PII in audit")
		}
	}
	out, err = s.Import(ctx, req)
	if err != nil || out.Unchanged != 2 {
		t.Fatalf("rerun=%+v err=%v", out, err)
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "2" {
		t.Fatal("rerun fabricated audit")
	}
}
func TestImportConflictsRollbackEntireBatch(t *testing.T) {
	for _, kind := range []string{"different code", "code owner"} {
		t.Run(kind, func(t *testing.T) {
			p := database(t)
			s := New(p)
			ctx := context.Background()
			execute(t, p, `INSERT INTO users(email,full_name,student_code) VALUES('owner@hcmut.edu.vn','Owner','OLD')`)
			conflict := student("owner@hcmut.edu.vn", "NEW")
			if kind == "code owner" {
				conflict = student("other@hcmut.edu.vn", "OLD")
			}
			_, err := s.Import(ctx, request(student("aaa@hcmut.edu.vn", "FIRST"), conflict))
			if !errors.Is(err, a.ErrConflict) {
				t.Fatalf("err=%v", err)
			}
			if strings.Contains(err.Error(), "owner@") || strings.Contains(err.Error(), "OLD") {
				t.Fatal("unsafe error")
			}
			if scalar(t, p, `SELECT count(*)::text FROM users`) != "1" || scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "0" {
				t.Fatal("partial commit")
			}
		})
	}
}
func TestDryRunRollsBackUsersAndAudit(t *testing.T) {
	p := database(t)
	s := New(p)
	execute(t, p, `INSERT INTO users(email,full_name) VALUES('existing@hcmut.edu.vn','Existing')`)
	req := request(student("existing@hcmut.edu.vn", "001"), student("new@hcmut.edu.vn", "002"))
	req.DryRun = true
	out, err := s.Import(context.Background(), req)
	if err != nil || out.Created != 1 || out.Enriched != 1 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if scalar(t, p, `SELECT count(*)::text FROM users`) != "1" || scalar(t, p, `SELECT count(student_code)::text FROM users`) != "0" || scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "0" {
		t.Fatal("dry run committed")
	}
}
func rejectAudit(t *testing.T, p *pgxpool.Pool) {
	execute(t, p, `CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private database detail'; END $$; CREATE TRIGGER fail_audit BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_audit()`)
}
func TestAuditFailureRollsBackEnrichmentAndRevoke(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	id := scalar(t, p, `INSERT INTO users(email,full_name,is_admin) VALUES('existing@hcmut.edu.vn','Existing',true) RETURNING id::text`)
	rejectAudit(t, p)
	if _, err := s.Import(ctx, request(student("existing@hcmut.edu.vn", "001"))); err == nil {
		t.Fatal("enrichment audit failure ignored")
	}
	if scalar(t, p, `SELECT (student_code IS NULL)::text FROM users WHERE id=$1`, id) != "true" {
		t.Fatal("enrichment committed")
	}
	if _, err := s.Admin(ctx, admin("revoke", id)); err == nil {
		t.Fatal("revoke audit failure ignored")
	}
	if scalar(t, p, `SELECT is_admin::text FROM users WHERE id=$1`, id) != "true" {
		t.Fatal("revoke committed")
	}
}

func TestAuditFailureRollsBackImportAndAdmin(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	id := scalar(t, p, `INSERT INTO users(email,full_name) VALUES('existing@hcmut.edu.vn','Existing') RETURNING id::text`)
	rejectAudit(t, p)
	if _, err := s.Import(ctx, request(student("new@hcmut.edu.vn", "001"))); err == nil {
		t.Fatal("audit failure ignored")
	}
	if scalar(t, p, `SELECT count(*)::text FROM users`) != "1" {
		t.Fatal("import committed")
	}
	if _, err := s.Admin(ctx, admin("grant", id)); err == nil {
		t.Fatal("audit failure ignored")
	}
	if scalar(t, p, `SELECT is_admin::text FROM users WHERE id=$1`, id) != "false" {
		t.Fatal("grant committed")
	}
	req := admin("bootstrap", "")
	req.Email = "bootstrap@hcmut.edu.vn"
	req.FullName = "Bootstrap"
	if _, err := s.Admin(ctx, req); err == nil {
		t.Fatal("bootstrap audit failure ignored")
	}
	if scalar(t, p, `SELECT count(*)::text FROM users`) != "1" {
		t.Fatal("bootstrap committed")
	}
}
func TestAdminTransitionsPreserveIdentityAndDoNotRestoreRevokedBootstrap(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	req := admin("bootstrap", "")
	req.Email = "bootstrap@hcmut.edu.vn"
	req.FullName = "Bootstrap"
	out, err := s.Admin(ctx, req)
	if err != nil || !out.Changed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	id := scalar(t, p, `SELECT id::text FROM users WHERE email='bootstrap@hcmut.edu.vn'`)
	out, err = s.Admin(ctx, req)
	if err != nil || out.Changed {
		t.Fatal("bootstrap rerun")
	}
	execute(t, p, `UPDATE users SET google_sub='sub',student_code='001',is_active=false WHERE id=$1`, id)
	for _, action := range []string{"revoke", "revoke", "grant", "grant", "revoke"} {
		if _, err = s.Admin(ctx, admin(action, id)); err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, p, `SELECT (google_sub='sub' AND student_code='001' AND NOT is_active AND NOT is_admin)::text FROM users WHERE id=$1`, id) != "true" {
		t.Fatal("identity changed")
	}
	if _, err = s.Admin(ctx, req); !errors.Is(err, a.ErrConflict) {
		t.Fatalf("bootstrap restored revoked user: %v", err)
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "4" {
		t.Fatal("missing or fabricated transitions")
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs WHERE metadata ? 'before' AND metadata ? 'after' AND metadata->>'operator'='dev-17' AND metadata->>'ticket'='OPS-1'`) != "4" {
		t.Fatal("incomplete attribution")
	}
	if _, err = s.Admin(ctx, admin("grant", "11111111-1111-1111-1111-111111111111")); !errors.Is(err, a.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
func TestConcurrentImportPreservesUserAndRejectsMSSVCollision(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Import(ctx, request(student("race@hcmut.edu.vn", "001")))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, p, `SELECT count(*)::text FROM users`) != "1" || scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "1" {
		t.Fatal("duplicate identity or audit")
	}
	start := make(chan struct{})
	errs = make(chan error, 2)
	for _, email := range []string{"a@hcmut.edu.vn", "b@hcmut.edu.vn"} {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			<-start
			_, err := s.Import(ctx, request(student(email, "002")))
			errs <- err
		}(email)
	}
	close(start)
	wg.Wait()
	close(errs)
	conflicts := 0
	for err := range errs {
		if errors.Is(err, a.ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d", conflicts)
	}
}
func TestImportRacesWithGoogleStyleUserCreation(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO users(email,full_name,google_sub) VALUES('login@hcmut.edu.vn','Google Name','google-sub') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.Import(ctx, request(student("login@hcmut.edu.vn", "001"))); done <- err }()
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if scalar(t, p, `SELECT (id=$1 AND google_sub='google-sub' AND full_name='Google Name' AND student_code='001')::text FROM users WHERE email='login@hcmut.edu.vn'`, id) != "true" {
		t.Fatal("login identity replaced")
	}
}

func TestGoogleStyleLinkingPreservesImportedStudent(t *testing.T) {
	p := database(t)
	s := New(p)
	ctx := context.Background()
	if _, err := s.Import(ctx, request(student("link@hcmut.edu.vn", "001"))); err != nil {
		t.Fatal(err)
	}
	id := scalar(t, p, `SELECT id::text FROM users WHERE email='link@hcmut.edu.vn'`)
	// This is the existing auth linking primitive; ops never supplies Google sub.
	execute(t, p, `UPDATE users SET google_sub='verified-sub',updated_at=now() WHERE id=$1 AND google_sub IS NULL AND is_active=true`, id)
	if _, err := s.Import(ctx, request(student("link@hcmut.edu.vn", "001"))); err != nil {
		t.Fatal(err)
	}
	if scalar(t, p, `SELECT (google_sub='verified-sub' AND student_code='001')::text FROM users WHERE id=$1`, id) != "true" {
		t.Fatal("linked student changed")
	}
}

func TestSeedOnlyAuditsInsertedIdentity(t *testing.T) {
	p := database(t)
	ctx := context.Background()
	seed, err := os.ReadFile("../../../../db/seed/seed.sql")
	if err != nil {
		t.Fatal(err)
	}
	run := func(email string) error {
		args := []string{"-X", "-v", "ON_ERROR_STOP=1", "-v", "admin_email=" + email, "-v", "operator=dev-17", "-v", "ticket=OPS-1"}
		var cmd *exec.Cmd
		if container := os.Getenv("TEST_PSQL_CONTAINER"); container != "" {
			args = append([]string{"exec", "-i", container, "psql", "-U", "postgres", "-d", p.Config().ConnConfig.Database}, args...)
			cmd = exec.CommandContext(ctx, "podman", args...)
		} else {
			cmd = exec.CommandContext(ctx, "psql", append([]string{p.Config().ConnString()}, args...)...)
		}
		cmd.Stdin = strings.NewReader(string(seed))
		return cmd.Run()
	}
	if err = run("new@hcmut.edu.vn"); err != nil {
		t.Fatal("seed failed")
	}
	if err = run("new@hcmut.edu.vn"); err != nil {
		t.Fatal("seed rerun failed")
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "1" {
		t.Fatal("seed rerun audit")
	}
	execute(t, p, `INSERT INTO users(email,full_name,is_admin) VALUES('old@hcmut.edu.vn','Old',true)`)
	if err = run("old@hcmut.edu.vn"); err != nil {
		t.Fatal("existing admin seed failed")
	}
	if scalar(t, p, `SELECT count(*)::text FROM activity_logs`) != "1" {
		t.Fatal("fabricated initial bootstrap")
	}
	execute(t, p, `UPDATE users SET is_admin=false WHERE email='new@hcmut.edu.vn'`)
	if err = run("new@hcmut.edu.vn"); err == nil {
		t.Fatal("seed silently accepted non-admin")
	}
	if scalar(t, p, `SELECT is_admin::text FROM users WHERE email='new@hcmut.edu.vn'`) != "false" {
		t.Fatal("seed restored revoked privilege")
	}
}
