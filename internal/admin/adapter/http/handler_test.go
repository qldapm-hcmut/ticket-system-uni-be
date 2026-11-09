package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/application"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type fakeStore struct {
	calls  int
	active bool
	err    error
}

func (f *fakeStore) CreateUnit(context.Context, string, a.UnitCreate) (a.Unit, error) {
	f.calls++
	return a.Unit{ID: id, Code: "C", Name: "Club", Type: "CLUB", IsActive: true}, f.err
}
func (f *fakeStore) SetMembers(context.Context, string, string, []a.Member) ([]a.UnitMember, error) {
	f.calls++
	return []a.UnitMember{}, f.err
}
func (f *fakeStore) ListUsers(context.Context, string, a.Filter) (a.UserList, error) {
	f.calls++
	return a.UserList{Items: []auth.Profile{{User: auth.User{ID: id, StudentCode: "123", IsAdmin: true, IsActive: true}}}, TotalItems: 1}, f.err
}
func (f *fakeStore) UpdateUser(_ context.Context, _, _ string, v bool) (auth.Profile, error) {
	f.calls++
	f.active = v
	return auth.Profile{User: auth.User{ID: id, StudentCode: "123", IsAdmin: true, IsActive: v}}, f.err
}
func setup(f *fakeStore, p auth.Profile) *echo.Echo {
	e := echo.New()
	New(a.New(f)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("auth.profile", p); return next(c) }
	})
	return e
}
func request(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w
}
func TestAdminHTTPValidationAndResponses(t *testing.T) {
	f := &fakeStore{}
	e := setup(f, auth.Profile{User: auth.User{ID: id, IsAdmin: true, IsActive: true}})
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/admin/users/" + id, `{"isActive":false,"isAdmin":true}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"studentCode":"111"}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"roles":["ADMIN"]}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"IsActive":false}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"isActive":true,"IsActive":false}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"isActive":true,"isActive":false}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"isActive":null}`},
		{"PATCH", "/api/v1/admin/users/" + id, `null`},
		{"PATCH", "/api/v1/admin/users/" + id, `[]`},
		{"PATCH", "/api/v1/admin/users/" + id, `{}`},
		{"PATCH", "/api/v1/admin/users/no", `{"isActive":true}`},
		{"PATCH", "/api/v1/admin/users/" + id, `{"isActive":true} {}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":null}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"Members":[]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[],"members":[]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[null]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[{"UserId":"` + id + `","memberRole":"ORGANIZER"}]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[{"userId":"` + id + `","MemberRole":"ORGANIZER"}]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[{"userId":"` + id + `","userId":"` + id + `","memberRole":"ORGANIZER"}]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[{"userId":"` + id + `","memberRole":"ADMIN"}]}`},
		{"PUT", "/api/v1/admin/units/" + id + "/members", `{"members":[{"userId":"` + id + `","memberRole":"ORGANIZER"},{"userId":"` + strings.ToUpper(id) + `","memberRole":"ORGANIZER"}]}`},
		{"GET", "/api/v1/admin/users?role=OWNER", ""},
		{"GET", "/api/v1/admin/users?role=", ""},
		{"GET", "/api/v1/admin/users?role=ADMIN&role=STUDENT", ""},
		{"GET", "/api/v1/admin/users?pageSize=101", ""},
		{"GET", "/api/v1/admin/users?page=0", ""},
		{"GET", "/api/v1/admin/users?page=", ""},
		{"POST", "/api/v1/admin/units", `{"code":"C","name":"N","type":"ADMIN"}`},
		{"POST", "/api/v1/admin/units", `{"Code":"C","name":"N","type":"CLUB"}`},
		{"POST", "/api/v1/admin/units", `{"code":"C","name":"N","type":"CLUB","code":"D"}`},
	} {
		w := request(e, tc.method, tc.path, tc.body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached store")
	}
	w := request(e, "PATCH", "/api/v1/admin/users/"+id, `{"isActive":false}`)
	if w.Code != 200 || f.active || !strings.Contains(w.Body.String(), `"isAdmin":true`) || !strings.Contains(w.Body.String(), `"studentCode":"123"`) {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	w = request(e, "PUT", "/api/v1/admin/units/"+id+"/members", `{"members":[{"userId":"`+id+`","memberRole":"ORGANIZER"}]}`)
	if w.Code != 200 || f.calls != 2 {
		t.Fatalf("valid member keys: %d %s (store calls: %d)", w.Code, w.Body.String(), f.calls)
	}
	w = request(e, "PUT", "/api/v1/admin/units/"+id+"/members", `{"members":[]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("clear: %s", w.Body.String())
	}
	w = request(e, "POST", "/api/v1/admin/units", `{"code":"C","name":"Club","type":"CLUB"}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	w = request(e, "GET", "/api/v1/admin/users?pageSize=1", "")
	var result struct {
		Data []map[string]any
		Meta map[string]int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Meta["totalPages"] != 1 || len(result.Data) != 1 {
		t.Fatalf("list: %s %v", w.Body.String(), err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	f.err = a.ErrConflict
	if w = request(e, "POST", "/api/v1/admin/units", `{"code":"C","name":"Club","type":"CLUB"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "INVALID_STATE_TRANSITION") {
		t.Fatal(w.Body.String())
	}
}
func TestNonAdminDeniedEveryEndpoint(t *testing.T) {
	for _, p := range []auth.Profile{{User: auth.User{ID: id, IsActive: true}}, {User: auth.User{ID: id, IsAdmin: true, IsActive: false}}} {
		f := &fakeStore{}
		e := setup(f, p)
		want := 403
		if !p.IsActive {
			want = 401
		}
		for _, tc := range []struct{ method, path string }{{"POST", "/units"}, {"PUT", "/units/" + id + "/members"}, {"GET", "/users"}, {"PATCH", "/users/" + id}} {
			w := request(e, tc.method, "/api/v1/admin"+tc.path, `{}`)
			if w.Code != want {
				t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
			}
		}
		if f.calls != 0 {
			t.Fatal("unauthorized store call")
		}
	}
}
