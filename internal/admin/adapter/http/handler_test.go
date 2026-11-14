package http

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"reflect"
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
	filter a.Filter
	list   *a.UserList
	user   *auth.Profile
}

func (f *fakeStore) CreateUnit(context.Context, string, a.UnitCreate) (a.Unit, error) {
	f.calls++
	return a.Unit{ID: id, Code: "C", Name: "Club", Type: "CLUB", IsActive: true}, f.err
}
func (f *fakeStore) SetMembers(context.Context, string, string, []a.Member) ([]a.UnitMember, error) {
	f.calls++
	return []a.UnitMember{}, f.err
}
func (f *fakeStore) ListUsers(_ context.Context, _ string, filter a.Filter) (a.UserList, error) {
	f.calls++
	f.filter = filter
	if f.list != nil {
		return *f.list, f.err
	}
	return a.UserList{Items: []auth.Profile{{User: auth.User{ID: id, StudentCode: "123", IsAdmin: true, IsActive: true}}}, TotalItems: 1}, f.err
}
func (f *fakeStore) UpdateUser(_ context.Context, _, _ string, v bool) (auth.Profile, error) {
	f.calls++
	f.active = v
	if f.user != nil {
		return *f.user, f.err
	}
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

// Lists and updates must retain the full profile shape even for inactive users.
func TestAdminProfileResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile auth.Profile
		want    map[string]any
	}{
		{"no roles", auth.Profile{User: auth.User{ID: id, Email: "test@hcmut.edu.vn", FullName: "Test User"}}, map[string]any{
			"id": id, "email": "test@hcmut.edu.vn", "fullName": "Test User", "studentCode": nil,
			"isAdmin": false, "isActive": false, "roles": []any{}, "units": []any{},
		}},
		{"overlapping roles", auth.Profile{User: auth.User{ID: id, Email: "test@hcmut.edu.vn", FullName: "Test User", StudentCode: "123", IsAdmin: true}, Memberships: []auth.Membership{
			{UnitID: "second", Code: "CLUB", Name: "Club", MemberRole: "ORGANIZER"},
			{UnitID: "first", Code: "CSE", Name: "Computer Science", MemberRole: "ORGANIZER"},
		}}, map[string]any{
			"id": id, "email": "test@hcmut.edu.vn", "fullName": "Test User", "studentCode": "123",
			"isAdmin": true, "isActive": false, "roles": []any{"STUDENT", "ORGANIZER", "ADMIN"},
			"units": []any{
				map[string]any{"unit": map[string]any{"id": "second", "code": "CLUB", "name": "Club"}, "memberRole": "ORGANIZER"},
				map[string]any{"unit": map[string]any{"id": "first", "code": "CSE", "name": "Computer Science"}, "memberRole": "ORGANIZER"},
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{list: &a.UserList{Items: []auth.Profile{tc.profile}, TotalItems: 1}, user: &tc.profile}
			e := setup(store, auth.Profile{User: auth.User{ID: id, IsAdmin: true, IsActive: true}})
			for _, method := range []string{"GET", "PATCH"} {
				path, body := "/api/v1/admin/users", ""
				if method == "PATCH" {
					path, body = path+"/"+id, `{"isActive":false}`
				}
				response := request(e, method, path, body)
				var payload struct{ Data any }
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				var want any = tc.want
				if method == "GET" {
					want = []any{tc.want}
				}
				if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || !reflect.DeepEqual(payload.Data, want) {
					t.Fatalf("%s profile response: %d %s", method, response.Code, response.Body.String())
				}
			}
		})
	}
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

// Pin defaults, query forwarding, empty arrays and overflow-safe page totals.
func TestListUsersPagination(t *testing.T) {
	for _, tc := range []struct {
		name, query  string
		total, pages int64
		filter       a.Filter
	}{
		{"defaults and empty results", "", 0, 0, a.Filter{Page: 1, PageSize: 20}},
		{"partial final page", "?q=%25&role=ORGANIZER&page=2&pageSize=2", 5, 3, a.Filter{Q: "%", Role: "ORGANIZER", Page: 2, PageSize: 2}},
		{"exact final page", "?page=3&pageSize=2", 6, 3, a.Filter{Page: 3, PageSize: 2}},
		{"large total", "?pageSize=100", math.MaxInt64, 92233720368547759, a.Filter{Page: 1, PageSize: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{list: &a.UserList{TotalItems: tc.total}}
			e := setup(store, auth.Profile{User: auth.User{ID: id, IsAdmin: true, IsActive: true}})
			response := request(e, "GET", "/api/v1/admin/users"+tc.query, "")
			var body struct {
				Data []json.RawMessage
				Meta struct{ Page, PageSize, TotalItems, TotalPages int64 }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || body.Data == nil || len(body.Data) != 0 || body.Meta.Page != tc.filter.Page || body.Meta.PageSize != tc.filter.PageSize || body.Meta.TotalItems != tc.total || body.Meta.TotalPages != tc.pages {
				t.Fatalf("pagination response: %d %s", response.Code, response.Body.String())
			}
			if store.calls != 1 || store.filter != tc.filter {
				t.Fatalf("query not preserved: %+v", store.filter)
			}
		})
	}
}

func TestListUsersRejectsInvalidPaginationBeforeStore(t *testing.T) {
	for _, query := range []string{
		"page=1&page=2", "pageSize=1&pageSize=2", "pageSize=0", "pageSize=-1",
		"page=9223372036854775808", "page=9223372036854775807&pageSize=2",
	} {
		t.Run(query, func(t *testing.T) {
			store := &fakeStore{}
			e := setup(store, auth.Profile{User: auth.User{ID: id, IsAdmin: true, IsActive: true}})
			response := request(e, "GET", "/api/v1/admin/users?"+query, "")
			if response.Code != 400 || store.calls != 0 || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
				t.Fatalf("invalid query: %d %s (store calls: %d)", response.Code, response.Body.String(), store.calls)
			}
		})
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
