package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failingTokens struct {
	tokenFake
	refreshErr, accessErr error
}

func (f failingTokens) NewRefreshToken() (string, error) {
	if f.refreshErr != nil {
		return "", f.refreshErr
	}
	return f.tokenFake.NewRefreshToken()
}
func (f failingTokens) AccessToken(id string, expires time.Time) (string, error) {
	if f.accessErr != nil {
		return "", f.accessErr
	}
	return f.tokenFake.AccessToken(id, expires)
}

func TestIssuanceFailuresDoNotExposeTokens(t *testing.T) {
	failure := errors.New("injected failure")
	for _, operation := range []string{"login", "refresh"} {
		for _, stage := range []string{"refresh generation", "access generation", "session insert", "commit"} {
			t.Run(operation+"/"+stage, func(t *testing.T) {
				r := &fakeRepo{bySub: User{ID: "user", IsActive: true}}
				u := &fakeUOW{r: r}
				tokens := failingTokens{}
				switch stage {
				case "refresh generation":
					tokens.refreshErr = failure
				case "access generation":
					tokens.accessErr = failure
				case "session insert":
					r.sessionErr = failure
				case "commit":
					u.commitErr = failure
				}
				s := NewService(u, verifierFake{}, tokens, fixedClock{})
				var result Tokens
				var err error
				if operation == "login" {
					var login AuthResult
					login, err = s.Login(context.Background(), "google")
					result = login.Tokens
					if login.User.ID != "" {
						t.Fatal("user leaked on failure")
					}
				} else {
					var refresh AuthResult
					refresh, err = s.Refresh(context.Background(), "refresh")
					result = refresh.Tokens
					if refresh.User.ID != "" {
						t.Fatal("user leaked on failure")
					}
				}
				if !errors.Is(err, failure) || result != (Tokens{}) || !u.rolledBack {
					t.Fatalf("unexpected result %+v error %v", result, err)
				}
			})
		}
	}
}
