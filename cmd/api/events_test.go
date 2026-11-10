package main

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestEventRoutesRequireAuthentication(t *testing.T) {
	e := echo.New()
	registerEventHandlers(e, nil, nil, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { return c.NoContent(401) }
	})
	for _, route := range []struct{ method, path string }{{"DELETE", "/api/v1/events/id"}, {"GET", "/api/v1/events"}, {"GET", "/api/v1/approvals/pending"}, {"POST", "/api/v1/events/id/submit"}, {"POST", "/api/v1/events/id/approve"}, {"POST", "/api/v1/events/id/reject"}} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code != 401 {
			t.Errorf("%s %s: got %d", route.method, route.path, rec.Code)
		}
	}
}
