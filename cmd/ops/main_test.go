package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	a "github.com/qldapm-hcmut/ticket-system-uni/internal/ops/application"
)

type fakeStore struct {
	imported     a.ImportRequest
	administered a.AdminRequest
	err          error
}

func (s *fakeStore) Import(_ context.Context, in a.ImportRequest) (a.ImportResult, error) {
	s.imported = in
	return a.ImportResult{Created: 1, Enriched: 2, Unchanged: 3}, s.err
}
func (s *fakeStore) Admin(_ context.Context, in a.AdminRequest) (a.AdminResult, error) {
	s.administered = in
	return a.AdminResult{Changed: true}, s.err
}
func TestCLIImportsDryRunAndReportsCountsWithoutPII(t *testing.T) {
	file := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(file, []byte("email,fullName,studentCode\na@hcmut.edu.vn,Name,001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	var out bytes.Buffer
	err := run(context.Background(), []string{"students", "import", "--file", file, "--operator", "dev", "--ticket", "OPS-1", "--dry-run"}, &out, func(context.Context) (a.Store, func(), error) { return store, func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !store.imported.DryRun || store.imported.Students[0].StudentCode != "001" {
		t.Fatal("input lost")
	}
	if !strings.Contains(out.String(), "dry-run") || !strings.Contains(out.String(), "created=1 enriched=2 unchanged=3") {
		t.Fatalf("output=%s", out.String())
	}
	for _, pii := range []string{"a@", "Name", "001"} {
		if strings.Contains(out.String(), pii) {
			t.Fatal("PII in output")
		}
	}
}
func TestCLIRejectsArgumentsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{
		{}, {"admin", "grant"}, {"admin", "grant", "--user-id", "bad", "--operator", "dev", "--ticket", "T"},
		{"admin", "grant", "--user-id", "11111111-1111-1111-1111-111111111111", "--operator", "dev", "--ticket", "T", "extra"},
		{"admin", "bootstrap", "--email", "a@evil.test", "--full-name", "A", "--operator", "dev", "--ticket", "T"},
		{"students", "import", "--file", "/nonexistent-private-roster", "--operator", "dev", "--ticket", "T"},
		{"admin", "grant", "--user-id", "11111111-1111-1111-1111-111111111111", "--operator", "dev", "--ticket", "T", "--is-admin"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			connected := false
			err := run(context.Background(), args, &bytes.Buffer{}, func(context.Context) (a.Store, func(), error) { connected = true; return &fakeStore{}, func() {}, nil })
			if err == nil || connected {
				t.Fatalf("err=%v connected=%v", err, connected)
			}
			if strings.Contains(err.Error(), "private-roster") || strings.Contains(err.Error(), "evil") {
				t.Fatal("unsafe diagnostic")
			}
		})
	}
}
func TestCLIAdminCommandsAndSafeFailure(t *testing.T) {
	for _, action := range []string{"bootstrap", "grant", "revoke"} {
		t.Run(action, func(t *testing.T) {
			args := []string{"admin", action, "--operator", "dev", "--ticket", "T"}
			if action == "bootstrap" {
				args = append(args, "--email", "a@hcmut.edu.vn", "--full-name", "Name")
			} else {
				args = append(args, "--user-id", "11111111-1111-1111-1111-111111111111")
			}
			store := &fakeStore{}
			closed := false
			open := func(context.Context) (a.Store, func(), error) { return store, func() { closed = true }, nil }
			var out bytes.Buffer
			if err := run(context.Background(), args, &out, open); err != nil {
				t.Fatal(err)
			}
			if store.administered.Action != action || !closed || !strings.Contains(out.String(), "changed=true") {
				t.Fatal("admin not applied")
			}
			store.err = errors.New("private@example.test database password")
			out.Reset()
			err := run(context.Background(), args, &out, open)
			if err == nil || strings.Contains(err.Error(), "private") || out.Len() != 0 {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}
func TestCLIConflictReportsOnlySafeRowNumber(t *testing.T) {
	file := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(file, []byte("email,fullName,studentCode\na@hcmut.edu.vn,Name,001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{err: &a.RowConflict{Row: 2}}
	var out bytes.Buffer
	err := run(context.Background(), []string{"students", "import", "--file", file, "--operator", "dev", "--ticket", "T"}, &out, func(context.Context) (a.Store, func(), error) { return store, func() {}, nil })
	if err == nil || !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), "conflict") || out.Len() != 0 {
		t.Fatalf("err=%v output=%s", err, out.String())
	}
}

func TestCLIDatabaseOpenFailureIsSanitized(t *testing.T) {
	err := run(context.Background(), []string{"admin", "grant", "--user-id", "11111111-1111-1111-1111-111111111111", "--operator", "dev", "--ticket", "T"}, &bytes.Buffer{}, func(context.Context) (a.Store, func(), error) {
		return nil, nil, errors.New("postgres://sensitive-connection")
	})
	if err == nil || strings.Contains(err.Error(), "postgres") {
		t.Fatalf("err=%v", err)
	}
}
