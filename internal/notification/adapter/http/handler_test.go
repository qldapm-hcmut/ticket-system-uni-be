package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
)

type fakeStore struct {
	calls   int
	filter  a.Filter
	options a.ReadOptions
	actor   string
	err     error
}

func (f *fakeStore) List(_ context.Context, actor string, v a.Filter) (a.List, error) {
	f.calls++
	f.actor = actor
	f.filter = v
	return a.List{Items: []a.Notification{}, TotalItems: 3, UnreadCount: 2}, f.err
}
func (f *fakeStore) MarkRead(_ context.Context, actor string, v a.ReadOptions) (a.ReadResult, error) {
	f.calls++
	f.actor = actor
	f.options = v
	return a.ReadResult{UpdatedCount: 1, UnreadCount: 2}, f.err
}
func setup(f *fakeStore, authenticated bool) *echo.Echo {
	e := echo.New()
	New(a.New(f)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !authenticated {
				return c.NoContent(401)
			}
			c.Set("auth.profile", auth.Profile{ID: "actor"})
			return next(c)
		}
	})
	return e
}
func TestNotificationHTTP(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "", "", 200}, {"GET", "?unread=true&page=2&pageSize=1", "", 200},
		{"GET", "?unread=false", "", 200}, {"GET", "?unread=true&unread=false", "", 400}, {"GET", "?pageSize=1&pageSize=2", "", 400},
		{"GET", "?page=0", "", 400}, {"GET", "?pageSize=101", "", 400}, {"GET", "?page=2147483647&pageSize=100", "", 400}, {"GET", "?page=no", "", 400}, {"GET", "?unread=1", "", 400}, {"GET", "?unread=", "", 400}, {"GET", "?page=1&page=2", "", 400},
		{"POST", "/read", `{"all":true}`, 200}, {"POST", "/read", `{"ids":["00000000-0000-0000-0000-000000000001"]}`, 200},
		{"POST", "/read", `{"ids":null,"all":true}`, 200}, {"POST", "/read", `{"ids":["00000000-0000-0000-0000-000000000001"],"all":null}`, 200},
		{"POST", "/read", `{"ids":["00000000-0000-0000-0000-000000000001"],"all":false}`, 400}, {"POST", "/read", `{"all":true} null`, 400},
		{"POST", "/read", `{}`, 400}, {"POST", "/read", `{"all":false}`, 400}, {"POST", "/read", `{"ids":[]}`, 400}, {"POST", "/read", `{"ids":["bad"]}`, 400}, {"POST", "/read", `{"all":true,"ids":[]}`, 400}, {"POST", "/read", `{"all":true,"userId":"other"}`, 400}, {"POST", "/read", `{"all":true} {}`, 400}, {"POST", "/read", `null`, 400}, {"POST", "/read", `{`, 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			f := &fakeStore{}
			e := setup(f, true)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/api/v1/me/notifications"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			e.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.status == 400 {
				if f.calls != 0 || !strings.Contains(rec.Body.String(), "VALIDATION_ERROR") {
					t.Fatal(f, rec.Body.String())
				}
				return
			}
			if f.calls != 1 || f.actor != "actor" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(f, rec.Header())
			}
			if tc.method == "GET" {
				if !strings.Contains(rec.Body.String(), `"data":[]`) || !strings.Contains(rec.Body.String(), `"unreadCount":2`) {
					t.Fatal(rec.Body.String())
				}
				want := a.Filter{Page: 1, PageSize: 20}
				if tc.path == "?unread=true&page=2&pageSize=1" {
					want = a.Filter{Page: 2, PageSize: 1, Unread: true}
				}
				if f.filter != want {
					t.Fatalf("filter=%+v want %+v", f.filter, want)
				}
			} else if strings.Contains(tc.body, `"all":true`) {
				if !f.options.All || len(f.options.IDs) != 0 {
					t.Fatal(f.options)
				}
			} else if f.options.All || len(f.options.IDs) != 1 || f.options.IDs[0] != "00000000-0000-0000-0000-000000000001" {
				t.Fatal(f.options)
			}
		})
	}
}
func TestAuthenticationAndErrors(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		path := "/api/v1/me/notifications"
		if method == "POST" {
			path += "/read"
		}
		f := &fakeStore{}
		rec := httptest.NewRecorder()
		setup(f, false).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != 401 || f.calls != 0 {
			t.Fatal(rec.Code, f)
		}
		f.err = errors.New("private database detail")
		req := httptest.NewRequest(method, path, strings.NewReader(`{"all":true}`))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		setup(f, true).ServeHTTP(rec, req)
		if rec.Code != 500 || strings.Contains(rec.Body.String(), "private") {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	f := &fakeStore{}
	req := httptest.NewRequest("POST", "/api/v1/me/notifications/read", strings.NewReader(`{"all":true}`))
	rec := httptest.NewRecorder()
	setup(f, true).ServeHTTP(rec, req)
	if rec.Code != 400 || f.calls != 0 {
		t.Fatal(rec.Code)
	}
}
