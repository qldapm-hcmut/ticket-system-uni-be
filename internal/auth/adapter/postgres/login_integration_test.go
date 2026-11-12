//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type identityStub struct{ identity application.Identity }

func (v identityStub) Verify(context.Context, string) (application.Identity, error) {
	return v.identity, nil
}

type localTokens struct{}

func (localTokens) NewRefreshToken() (string, error) {
	var bytes [32]byte
	_, err := rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:]), err
}
func (localTokens) HashRefreshToken(raw string) string {
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}
func (localTokens) AccessToken(string, time.Time) (string, error) { return "test-access", nil }

func testLoginUseCases(t *testing.T, pool *pgxpool.Pool, uow *UnitOfWork) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, bootstrap := range []bool{false, true} {
		email, sub := "concurrent@hcmut.edu.vn", "concurrent-sub"
		if bootstrap {
			email, sub = "bootstrap@hcmut.edu.vn", "bootstrap-sub"
			if _, err := pool.Exec(ctx, `INSERT INTO users(email,full_name,student_code,is_admin) VALUES($1,'Seed','2219999',true)`, email); err != nil {
				t.Fatal(err)
			}
		}
		s := application.NewService(uow, identityStub{application.Identity{Sub: sub, Email: email, FullName: "Login"}}, localTokens{}, kernel.SystemClock{})
		const concurrentLogins = 20
		results := make(chan application.AuthResult, concurrentLogins)
		errs := make(chan error, concurrentLogins)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < concurrentLogins; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, err := s.Login(ctx, "verified")
				if err != nil {
					errs <- err
				} else {
					results <- r
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			t.Fatal(err)
		}
		var id string
		count := 0
		for r := range results {
			count++
			if id != "" && id != r.User.ID {
				t.Fatal("duplicate user")
			}
			id = r.User.ID
			if bootstrap && (r.User.StudentCode != "2219999" || !r.User.IsAdmin) {
				t.Fatal("bootstrap privileges lost")
			}
			if !bootstrap && (r.User.StudentCode != "" || r.User.IsAdmin) {
				t.Fatal("unexpected privileges")
			}
		}
		if count != concurrentLogins {
			t.Fatal("expected all concurrent logins to succeed")
		}
		var users, sessions int
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE email=$1),(SELECT count(*) FROM auth_sessions WHERE user_id=$2)`, email, id).Scan(&users, &sessions); err != nil {
			t.Fatal(err)
		}
		if users != 1 || sessions != concurrentLogins {
			t.Fatalf("users=%d sessions=%d", users, sessions)
		}
	}
	t.Run("concurrent refresh and replay", func(t *testing.T) {
		s := application.NewService(uow, identityStub{application.Identity{Sub: "refresh-race", Email: "refresh-race@hcmut.edu.vn", FullName: "Refresh"}}, localTokens{}, kernel.SystemClock{})
		login, err := s.Login(ctx, "verified")
		if err != nil {
			t.Fatal(err)
		}
		const requests = 50
		start := make(chan struct{})
		results := make(chan application.AuthResult, requests)
		errs := make(chan error, requests)
		var wg sync.WaitGroup
		for i := 0; i < requests; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, err := s.Refresh(ctx, login.RefreshToken)
				if err != nil {
					errs <- err
				} else {
					results <- r
				}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		successes, rejections := 0, 0
		var replacement application.AuthResult
		for r := range results {
			successes++
			if r.User.ID != login.User.ID {
				t.Fatal("refresh lost trusted session identity")
			}
			replacement = r
		}
		for err := range errs {
			if !errors.Is(err, application.ErrUnauthenticated) {
				t.Errorf("unexpected error: %v", err)
			}
			rejections++
		}
		if successes != 1 || rejections != requests-1 {
			t.Fatalf("success=%d rejection=%d", successes, rejections)
		}
		var active, total int
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE revoked_at IS NULL),count(*) FROM auth_sessions WHERE user_id=$1`, login.User.ID).Scan(&active, &total); err != nil {
			t.Fatal(err)
		}
		if active != 1 || total != 2 {
			t.Fatalf("active=%d total=%d", active, total)
		}
		if r, err := s.Refresh(ctx, login.RefreshToken); !errors.Is(err, application.ErrUnauthenticated) || r.RefreshToken != "" {
			t.Fatal("old token replay accepted")
		}
		if _, err := s.Refresh(ctx, replacement.RefreshToken); err != nil {
			t.Fatalf("replacement unusable: %v", err)
		}
	})
	for _, tc := range []struct {
		email, storedSub string
		active           bool
	}{
		{"disabled-seed@hcmut.edu.vn", "", false}, {"conflict@hcmut.edu.vn", "original-sub", true},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(email,full_name,google_sub,is_active) VALUES($1,'Seed',NULLIF($2,''),$3)`, tc.email, tc.storedSub, tc.active); err != nil {
			t.Fatal(err)
		}
		s := application.NewService(uow, identityStub{application.Identity{Sub: "attempt-" + tc.email, Email: tc.email, FullName: "Attempt"}}, localTokens{}, kernel.SystemClock{})
		if r, err := s.Login(ctx, "verified"); !errors.Is(err, application.ErrUnauthenticated) || r.RefreshToken != "" {
			t.Fatalf("expected rejection, got %v", err)
		}
		var stored string
		var sessions int
		if err := pool.QueryRow(ctx, `SELECT COALESCE(google_sub,''),(SELECT count(*) FROM auth_sessions WHERE user_id=users.id) FROM users WHERE email=$1`, tc.email).Scan(&stored, &sessions); err != nil {
			t.Fatal(err)
		}
		if stored != tc.storedSub || sessions != 0 {
			t.Fatal("rejected login modified identity/session")
		}
	}
}
