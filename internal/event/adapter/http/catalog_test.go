package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
)

type catalogReader struct {
	application.Reader
	filter application.CatalogFilter
}

func (r *catalogReader) Catalog(_ context.Context, _ application.Actor, f application.CatalogFilter) ([]map[string]any, int64, error) {
	r.filter = f
	return []map[string]any{{"title": "Test"}}, 21, nil
}

func TestCatalog(t *testing.T) {
	e := echo.New()
	reader := &catalogReader{}
	New(nil, reader).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set("auth.profile", auth.Profile{}); return next(c) }
	})
	for _, query := range []string{"page=0", "pageSize=101", "page=2147483647&pageSize=100", "unitId=bad", "from=yesterday", "from=2026-11-03T00:00:00Z&to=2026-11-01T00:00:00Z", "availableOnly=yes"} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events?"+query, nil))
			if rec.Code != 400 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
		})
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/events?page=2&pageSize=10&q=Test&availableOnly=true", nil))
	if rec.Code != 200 || reader.filter.Page != 2 || reader.filter.Search != "Test" || !reader.filter.AvailableOnly {
		t.Fatalf("response %d %s filter %+v", rec.Code, rec.Body, reader.filter)
	}
	var body struct {
		Meta struct{ TotalItems, TotalPages int64 }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Meta.TotalItems != 21 || body.Meta.TotalPages != 3 {
		t.Fatalf("meta %+v", body.Meta)
	}
}
