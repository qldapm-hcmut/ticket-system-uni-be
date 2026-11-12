package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

type verifierFake struct{ err error }

func (v verifierFake) Verify(context.Context, string) (Identity, error) {
	return Identity{Sub: "sub", Email: "test@hcmut.edu.vn", FullName: "Test"}, v.err
}

type tokenFake struct{}

func (tokenFake) NewRefreshToken() (string, error)              { return "raw-refresh", nil }
func (tokenFake) HashRefreshToken(s string) string              { return "hashed:" + s }
func (tokenFake) AccessToken(string, time.Time) (string, error) { return "access", nil }

type fakeRepo struct {
	Repository
	bySub           User
	subErr          error
	byEmail         User
	emailErr        error
	createErr       error
	sessionErr      error
	consumeErr      error
	userErr         error
	foundUserID     string
	linked, created bool
	consumed        bool
	hash            string
	expires         time.Time
	revokedUser     string
}

func (r *fakeRepo) FindUserBySub(context.Context, string) (User, error) { return r.bySub, r.subErr }
func (r *fakeRepo) FindUserByEmail(context.Context, string) (User, error) {
	return r.byEmail, r.emailErr
}
func (r *fakeRepo) FindUserByID(_ context.Context, id string) (User, error) {
	r.foundUserID = id
	return r.bySub, r.userErr
}
func (r *fakeRepo) CreateUser(context.Context, string, string, string) (User, error) {
	r.created = true
	return User{ID: "user", IsActive: true}, r.createErr
}
func (r *fakeRepo) LinkIdentity(context.Context, string, string) (User, error) {
	r.linked = true
	u := r.byEmail
	u.GoogleSub = "sub"
	return u, nil
}
func (r *fakeRepo) CreateSession(_ context.Context, _ string, hash string, expires time.Time) (Session, error) {
	r.hash = hash
	r.expires = expires
	return Session{}, r.sessionErr
}
func (r *fakeRepo) ConsumeSession(context.Context, string, time.Time) (Session, error) {
	r.consumed = true
	return Session{UserID: "user"}, r.consumeErr
}
func (r *fakeRepo) RevokeSession(_ context.Context, user, hash string, _ time.Time) error {
	r.revokedUser = user
	r.hash = hash
	return nil
}

type fakeUOW struct {
	r          *fakeRepo
	calls      int
	rolledBack bool
	commitErr  error
}

func (u *fakeUOW) Do(ctx context.Context, fn func(context.Context, Repository) error) error {
	u.calls++
	err := fn(ctx, u.r)
	if err == nil {
		err = u.commitErr
	}
	u.rolledBack = err != nil
	return err
}

func TestLogin(t *testing.T) {
	for _, tc := range []struct {
		name            string
		r               fakeRepo
		wantErr         error
		linked, created bool
	}{
		{name: "existing", r: fakeRepo{bySub: User{ID: "user", IsActive: true}}},
		{name: "new", r: fakeRepo{subErr: ErrNotFound, emailErr: ErrNotFound}, created: true},
		{name: "bootstrap", r: fakeRepo{subErr: ErrNotFound, byEmail: User{ID: "seed", IsActive: true, StudentCode: "2210001"}}, linked: true},
		{name: "disabled bootstrap", r: fakeRepo{subErr: ErrNotFound, byEmail: User{ID: "seed", IsActive: false}}, wantErr: ErrUnauthenticated},
		{name: "disabled", r: fakeRepo{bySub: User{ID: "user", IsActive: false}}, wantErr: ErrUnauthenticated},
		{name: "different sub", r: fakeRepo{subErr: ErrNotFound, byEmail: User{IsActive: true, GoogleSub: "other"}}, wantErr: ErrUnauthenticated},
		{name: "db failure", r: fakeRepo{subErr: errors.New("database unavailable")}, wantErr: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &fakeUOW{r: &tc.r}
			s := NewService(u, verifierFake{}, tokenFake{}, fixedClock{})
			result, err := s.Login(context.Background(), "google-token")
			if tc.name == "db failure" {
				if err == nil || errors.Is(err, ErrUnauthenticated) {
					t.Fatalf("lost infrastructure error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v want %v", err, tc.wantErr)
			}
			if tc.r.linked != tc.linked || tc.r.created != tc.created {
				t.Fatal("incorrect identity operation")
			}
			if err == nil {
				if tc.linked && result.User.StudentCode != tc.r.byEmail.StudentCode {
					t.Fatal("bootstrap student code lost")
				}
				if result.RefreshToken != "raw-refresh" || tc.r.hash != "hashed:raw-refresh" || !tc.r.expires.Equal((fixedClock{}).Now().Add(refreshLifetime)) {
					t.Fatal("incorrect session")
				}
			}
		})
	}
}
func TestVerifierFailureDoesNotOpenTransaction(t *testing.T) {
	u := &fakeUOW{r: &fakeRepo{}}
	s := NewService(u, verifierFake{err: errors.New("bad token")}, tokenFake{}, fixedClock{})
	if _, err := s.Login(context.Background(), "bad"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if u.calls != 0 {
		t.Fatal("opened transaction before verification")
	}
}
func TestRefresh(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		consumeErr, sessionErr, userErr error
		active                          bool
	}{
		{name: "success", active: true}, {name: "expired or replay", consumeErr: ErrNotFound, active: true},
		{name: "disabled", active: false}, {name: "replacement fails", active: true, sessionErr: errors.New("insert failed")},
		{name: "user missing", userErr: ErrNotFound}, {name: "user lookup fails", userErr: errors.New("lookup failed")},
		{name: "empty token", active: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRepo{bySub: User{ID: "user", IsActive: tc.active, IsAdmin: true}, consumeErr: tc.consumeErr, sessionErr: tc.sessionErr, userErr: tc.userErr}
			u := &fakeUOW{r: r}
			s := NewService(u, verifierFake{}, tokenFake{}, fixedClock{})
			raw := "old"
			if tc.name == "empty token" {
				raw = " "
			}
			result, err := s.Refresh(context.Background(), raw)
			if tc.name == "success" {
				if err != nil || result.RefreshToken != "raw-refresh" || result.AccessToken != "access" || result.ExpiresIn != 900 || result.User != r.bySub || r.foundUserID != "user" {
					t.Fatalf("incorrect trusted session result: %+v, err=%v", result, err)
				}
			} else {
				if err == nil || result != (AuthResult{}) || (tc.name != "empty token" && !u.rolledBack) {
					t.Fatal("must fail without exposing tokens or user")
				}
				if tc.name == "disabled" || tc.name == "expired or replay" || tc.name == "user missing" || tc.name == "empty token" {
					if !errors.Is(err, ErrUnauthenticated) {
						t.Fatalf("want unauthenticated, got %v", err)
					}
				}
			}
		})
	}
}
func TestLogout(t *testing.T) {
	r := &fakeRepo{}
	s := NewService(&fakeUOW{r: r}, verifierFake{}, tokenFake{}, fixedClock{})
	if err := s.Logout(context.Background(), "owner", "refresh"); err != nil {
		t.Fatal(err)
	}
	if r.revokedUser != "owner" || r.hash != "hashed:refresh" {
		t.Fatal("incorrect revocation")
	}
}
