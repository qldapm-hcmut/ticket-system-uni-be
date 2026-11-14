package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

type fakeService struct{ logoutID string }

func (*fakeService) Login(context.Context, string) (application.AuthResult, error) {
	return application.AuthResult{User: application.User{ID: "user"}, Tokens: application.Tokens{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: 900}}, nil
}
func (*fakeService) Refresh(context.Context, string) (application.AuthResult, error) {
	return application.AuthResult{User: application.User{ID: "user"}, Tokens: application.Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 900}}, nil
}
func (s *fakeService) Logout(_ context.Context, id, _ string) error { s.logoutID = id; return nil }

type fakeAccess struct{}

func (fakeAccess) VerifyAccessToken(raw string) (string, error) {
	if raw != "valid" {
		return "", application.ErrUnauthenticated
	}
	return "user", nil
}

type fakeProfiles struct {
	disabled bool
	err      error
	readID   string
	profile  *application.Profile
}

func (p *fakeProfiles) ReadProfile(_ context.Context, id string) (application.Profile, error) {
	p.readID = id
	if p.err != nil {
		return application.Profile{}, p.err
	}
	if p.disabled {
		return application.Profile{}, application.ErrUnauthenticated
	}
	if p.profile != nil {
		return *p.profile, nil
	}
	return application.Profile{User: application.User{ID: "user", Email: "test@hcmut.edu.vn", FullName: "Test User", IsActive: true, StudentCode: "2210001", IsAdmin: true}, Memberships: []application.Membership{{UnitID: "unit", Code: "CSE", Name: "Computer Science", MemberRole: "ORGANIZER"}}}, nil
}

// Empty profile collections must remain arrays, and absent studentCode is null.
func TestMeWithoutRoles(t *testing.T) {
	profile := application.Profile{User: application.User{ID: "user", Email: "test@hcmut.edu.vn", FullName: "Test User", IsActive: true}}
	for _, memberships := range [][]application.Membership{nil, {}} {
		profile.Memberships = memberships
		e := echo.New()
		New(&fakeService{}, fakeAccess{}, &fakeProfiles{profile: &profile}).Register(e)
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.Header.Set("Authorization", "Bearer valid")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var got any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"data": map[string]any{
			"id": "user", "email": "test@hcmut.edu.vn", "fullName": "Test User", "studentCode": nil,
			"isAdmin": false, "isActive": true, "roles": []any{}, "units": []any{},
		}}
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !reflect.DeepEqual(got, want) {
			t.Fatalf("profile response: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestHTTPRoutes(t *testing.T) {
	e := echo.New()
	s := &fakeService{}
	p := &fakeProfiles{}
	New(s, fakeAccess{}, p).Register(e)
	for _, tc := range []struct {
		method, path, body, auth string
		status                   int
	}{
		{"POST", "/api/v1/auth/google", `{"idToken":"google"}`, "", 200},
		{"POST", "/api/v1/auth/refresh", `{"refreshToken":"refresh"}`, "", 200},
		{"POST", "/api/v1/auth/google", `{}`, "", 400},
		{"POST", "/api/v1/auth/refresh", `{"refreshToken":42}`, "", 400},
		{"POST", "/api/v1/auth/google", `{"idToken":"x"} {}`, "", 400},
		{"GET", "/api/v1/me", "", "", 401},
		{"GET", "/api/v1/me", "", "Bearer invalid", 401},
		{"GET", "/api/v1/me", "", "Bearer valid", 200},
		{"POST", "/api/v1/auth/logout", `{"refreshToken":"refresh"}`, "Bearer valid", 204},
	} {
		t.Run(tc.method+tc.path+tc.auth+tc.body, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", tc.auth)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Code == 200 {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["data"] == nil {
					t.Fatal("missing data envelope")
				}
			}
		})
	}
	if s.logoutID != "user" {
		t.Fatal("logout not scoped to authenticated user")
	}
	p.disabled = true
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer valid")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("disabled user accepted")
	}
}

// Both issuance routes must read the current profile using trusted identity.
// Returning tokens before that read, accepting client identity, or omitting
// no-store must break this contract for the shared post-commit response path.
func TestTokenResponse(t *testing.T) {
	for _, operation := range []struct {
		name, path, body, accessToken, refreshToken string
	}{
		{"login", "/api/v1/auth/google", `{"idToken":"google","userId":"attacker","isAdmin":false}`, "access", "refresh"},
		{"refresh", "/api/v1/auth/refresh", `{"refreshToken":"old","userId":"attacker","isAdmin":false}`, "new-access", "new-refresh"},
	} {
		for _, tc := range []struct {
			name     string
			profiles fakeProfiles
			status   int
			code     string
		}{
			{name: "current profile", status: 200},
			{name: "disabled after rotation", profiles: fakeProfiles{disabled: true}, status: 401, code: "UNAUTHENTICATED"},
			{name: "deleted after rotation", profiles: fakeProfiles{err: application.ErrNotFound}, status: 401, code: "UNAUTHENTICATED"},
			{name: "profile unavailable", profiles: fakeProfiles{err: errors.New("database unavailable")}, status: 500, code: "INTERNAL_ERROR"},
		} {
			t.Run(operation.name+"/"+tc.name, func(t *testing.T) {
				e := echo.New()
				New(&fakeService{}, fakeAccess{}, &tc.profiles).Register(e)
				req := httptest.NewRequest("POST", operation.path, strings.NewReader(operation.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				if tc.profiles.readID != "user" {
					t.Fatalf("profile identity=%q, want trusted session user", tc.profiles.readID)
				}
				var payload struct {
					Data  map[string]any        `json:"data"`
					Error struct{ Code string } `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if tc.status != 200 {
					if payload.Data != nil || payload.Error.Code != tc.code || strings.Contains(rec.Body.String(), operation.refreshToken) || strings.Contains(rec.Body.String(), operation.accessToken) {
						t.Fatalf("unexpected error response: %s", rec.Body.String())
					}
					return
				}
				if rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("tokens must not be cached")
				}
				want := map[string]any{
					"accessToken": operation.accessToken, "refreshToken": operation.refreshToken, "expiresIn": float64(900),
					"user": map[string]any{
						"id": "user", "email": "test@hcmut.edu.vn", "fullName": "Test User", "studentCode": "2210001",
						"isAdmin": true, "isActive": true, "roles": []any{"STUDENT", "ORGANIZER", "ADMIN"},
						"units": []any{map[string]any{"unit": map[string]any{"id": "unit", "code": "CSE", "name": "Computer Science"}, "memberRole": "ORGANIZER"}},
					},
				}
				if !reflect.DeepEqual(payload.Data, want) {
					t.Fatalf("data=%#v, want %#v", payload.Data, want)
				}
			})
		}
	}
}
