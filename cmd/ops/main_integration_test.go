//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCLIRealDatabaseImportAndAdmin(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal("connect test database")
	}
	defer root.Close()
	name := fmt.Sprintf("ops_cli_%d", time.Now().UnixNano())
	if _, err = root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal("create test database")
	}
	defer root.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse test database")
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect isolated database")
	}
	defer pool.Close()
	schema, err := os.ReadFile("../../db/migrations/0001_initial_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal("parse test URL")
	}
	parsedURL.Path = "/" + name
	t.Setenv("DATABASE_URL", parsedURL.String())
	file := filepath.Join(t.TempDir(), "roster.csv")
	if err = os.WriteFile(file, []byte("email,fullName,studentCode\na@hcmut.edu.vn,Student,001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		args = append(args, "--operator", "dev-test", "--ticket", "OPS-1")
		if err := run(ctx, args, &out, openDatabase); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if !strings.Contains(call("students", "import", "--file", file, "--dry-run"), "dry-run") {
		t.Fatal("missing dry run")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatal("dry run committed")
	}
	call("students", "import", "--file", file)
	call("admin", "bootstrap", "--email", "admin@hcmut.edu.vn", "--full-name", "Admin")
	var id string
	if err = pool.QueryRow(ctx, `SELECT id::text FROM users WHERE email='a@hcmut.edu.vn'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	call("admin", "grant", "--user-id", id)
	var activeAdmin bool
	if err = pool.QueryRow(ctx, `SELECT is_admin FROM users WHERE id=$1 AND is_active`, id).Scan(&activeAdmin); err != nil || !activeAdmin {
		t.Fatal("grant not immediately readable")
	}
	call("admin", "revoke", "--user-id", id)
	if err = pool.QueryRow(ctx, `SELECT is_admin FROM users WHERE id=$1 AND is_active`, id).Scan(&activeAdmin); err != nil || activeAdmin {
		t.Fatal("revoke not immediately readable")
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}
