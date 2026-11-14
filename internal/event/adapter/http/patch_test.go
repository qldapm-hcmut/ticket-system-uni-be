package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type patchRepository struct {
	a.EventRepository
	current domain.Event
	updated *domain.Event
}

func (r *patchRepository) Get(context.Context, string, bool) (domain.Event, error) {
	return r.current, nil
}
func (r *patchRepository) IsOrganizer(context.Context, string, string) (bool, error) {
	return true, nil
}
func (r *patchRepository) Update(_ context.Context, event domain.Event) (domain.Event, error) {
	r.updated = &event
	return event, nil
}

type patchUOW struct{ repo *patchRepository }

func (u patchUOW) Do(ctx context.Context, fn func(context.Context, a.EventRepository) error) error {
	return fn(ctx, u.repo)
}

type patchClock struct{ now time.Time }

func (c patchClock) Now() time.Time { return c.now }

type patchReader struct{ a.Reader }

func (patchReader) Detail(context.Context, a.Actor, string) (map[string]any, error) {
	return map[string]any{"id": "event"}, nil
}

// Catch overlays that lose omitted fields, retain explicit nulls, or decode an
// earlier duplicate value rather than the last value accepted by the HTTP body.
func TestPatchOverlay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	description, limit := "original", int32(10)
	original := domain.Fields{Title: "Original", Description: &description, Location: "Room",
		StartAt: now.Add(48 * time.Hour), EndAt: now.Add(49 * time.Hour),
		RegistrationOpensAt: now.Add(time.Hour), RegistrationClosesAt: now.Add(47 * time.Hour),
		Capacity: 20, WaitlistLimit: &limit}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"partial", `{"title":"Changed"}`, 200},
		{"nullable", `{"description":null,"waitlistLimit":null}`, 200},
		{"duplicate last wins", `{"capacity":"bad","capacity":30}`, 200},
		{"wrong scalar", `{"capacity":"bad"}`, 400},
		{"wrong nullable", `{"waitlistLimit":"bad"}`, 400},
		{"required null", `{"title":null}`, 400},
		{"unknown", `{"unitId":"other"}`, 400},
		{"empty", `{}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &patchRepository{current: domain.Event{ID: "event", UnitID: "unit", Status: "DRAFT", Version: 3, Fields: original}}
			e := echo.New()
			New(a.New(patchUOW{r}, patchClock{now}), patchReader{}).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.profile", auth.Profile{Memberships: []auth.Membership{{}}})
					return next(c)
				}
			})
			req := httptest.NewRequest("PATCH", "/api/v1/events/event", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", "3")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status != 200 {
				if r.updated != nil {
					t.Fatal("invalid patch reached update")
				}
				return
			}
			want := original
			switch tc.name {
			case "partial":
				want.Title = "Changed"
			case "nullable":
				want.Description, want.WaitlistLimit = nil, nil
			case "duplicate last wins":
				want.Capacity = 30
			}
			if r.updated == nil || !reflect.DeepEqual(r.updated.Fields, want) {
				t.Fatalf("updated = %+v, want fields %+v", r.updated, want)
			}
			var body struct {
				Data map[string]any
				Meta map[string]any
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Data["id"] != "event" || body.Meta["significantChange"] != false || body.Meta["notifiedCount"] != float64(0) || body.Meta["promotedCount"] != float64(0) {
				t.Fatalf("response: %s (%v)", rec.Body.String(), err)
			}
		})
	}
}
