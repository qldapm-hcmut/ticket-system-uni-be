//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	authhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/http"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/token"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func testHTTPFlow(t *testing.T, pool *pgxpool.Pool, uow *UnitOfWork) {
	tokens, err := token.New("test-only-key-at-least-32-bytes-long", kernel.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(uow, identityStub{application.Identity{Sub: "http-sub", Email: "http@hcmut.edu.vn", FullName: "HTTP"}}, tokens, kernel.SystemClock{})
	e := echo.New()
	authhttp.New(service, tokens, NewProfileReader(pool)).Register(e)
	request := func(method, path, body, access string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var out struct{ Data map[string]any }
		if rec.Code == 200 {
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("auth response must not be cached")
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		} else if strings.Contains(rec.Body.String(), "accessToken") || strings.Contains(rec.Body.String(), "refreshToken") || strings.Contains(rec.Body.String(), `"data"`) {
			t.Fatalf("error response leaked auth data: %s", rec.Body.String())
		}
		return out.Data
	}
	login := request("POST", "/api/v1/auth/google", `{"idToken":"verified-by-stub"}`, "", 200)
	access := login["accessToken"].(string)
	refresh := login["refreshToken"].(string)
	request("GET", "/api/v1/me", "", access, 200)
	id := login["user"].(map[string]any)["id"].(string)
	assertProfile := func(data map[string]any, admin bool, roles, units []any) {
		t.Helper()
		want := map[string]any{"id": id, "email": "http@hcmut.edu.vn", "fullName": "HTTP", "studentCode": nil, "isActive": true, "isAdmin": admin, "roles": roles, "units": units}
		accessToken, _ := data["accessToken"].(string)
		refreshToken, _ := data["refreshToken"].(string)
		if !reflect.DeepEqual(data["user"], want) || accessToken == "" || refreshToken == "" || data["expiresIn"] != float64(900) {
			t.Fatalf("unexpected auth response: %#v, want user %#v", data, want)
		}
	}
	assertProfile(login, false, []any{}, []any{})
	ctx := context.Background()
	var unitID string
	if err := pool.QueryRow(ctx, `INSERT INTO units(code,name,type) VALUES('B5','Refresh Unit','CLUB') RETURNING id`).Scan(&unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO unit_memberships(unit_id,user_id,member_role) VALUES($1,$2,'ORGANIZER')`, unitID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_admin=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rotated := request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`","userId":"untrusted"}`, "", 200)
	assertProfile(rotated, true, []any{"ORGANIZER", "ADMIN"}, []any{map[string]any{"unit": map[string]any{"id": unitID, "code": "B5", "name": "Refresh Unit"}, "memberRole": "ORGANIZER"}})
	if _, err := pool.Exec(ctx, `DELETE FROM unit_memberships WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_admin=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 401)
	rotated = request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+rotated["refreshToken"].(string)+`"}`, "", 200)
	assertProfile(rotated, false, []any{}, []any{})
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 401)
	request("POST", "/api/v1/auth/logout", `{"refreshToken":"`+rotated["refreshToken"].(string)+`"}`, access, 204)
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+rotated["refreshToken"].(string)+`"}`, "", 401)
	login = request("POST", "/api/v1/auth/google", `{"idToken":"verified-by-stub"}`, "", 200)
	refresh = login["refreshToken"].(string)
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/v1/me", "", access, 401)
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 401)
	var revoked bool
	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL,(SELECT count(*) FROM auth_sessions WHERE user_id=$2) FROM auth_sessions WHERE refresh_token_hash=$1`, tokens.HashRefreshToken(refresh), id).Scan(&revoked, &sessionCount); err != nil {
		t.Fatal(err)
	}
	if revoked || sessionCount != 4 {
		t.Fatalf("disabled refresh must roll back: revoked=%v sessions=%d", revoked, sessionCount)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	// The same token works again: rejecting an inactive user rolled back consumption.
	rotated = request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 200)
	assertProfile(rotated, false, []any{}, []any{})

	// Inject only the profile read failure: the real rotation still commits.
	e = echo.New()
	authhttp.New(service, tokens, unavailableProfile{}).Register(e)
	refresh = rotated["refreshToken"].(string)
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 500)
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL,(SELECT count(*) FROM auth_sessions WHERE user_id=$2) FROM auth_sessions WHERE refresh_token_hash=$1`, tokens.HashRefreshToken(refresh), id).Scan(&revoked, &sessionCount); err != nil {
		t.Fatal(err)
	}
	if !revoked || sessionCount != 6 {
		t.Fatalf("profile error must leave rotation committed: revoked=%v sessions=%d", revoked, sessionCount)
	}
	e = echo.New()
	authhttp.New(service, tokens, NewProfileReader(pool)).Register(e)
	request("POST", "/api/v1/auth/refresh", `{"refreshToken":"`+refresh+`"}`, "", 401)
	request("POST", "/api/v1/auth/google", `{"idToken":"verified-by-stub"}`, "", 200)
}

type unavailableProfile struct{}

func (unavailableProfile) ReadProfile(context.Context, string) (application.Profile, error) {
	return application.Profile{}, errors.New("profile unavailable")
}
