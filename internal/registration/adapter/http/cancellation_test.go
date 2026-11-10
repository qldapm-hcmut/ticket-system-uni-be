package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

type cancelStore struct {
	a.RegistrationRepository
	calls         int
	actor, reason string
	err           error
}

func (s *cancelStore) Do(ctx context.Context, fn func(context.Context, a.RegistrationRepository) error) error {
	return fn(ctx, s)
}
func (s *cancelStore) Cancel(_ context.Context, _, actor, reason string) (a.CancellationResult, error) {
	s.calls++
	s.actor = actor
	s.reason = reason
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	by := "ORGANIZER"
	return a.CancellationResult{Registration: a.Registration{ID: "reg", Status: "CANCELLED", CancelledAt: &at, CancelledBy: &by}, Promoted: &a.Registration{ID: "promoted"}}, s.err
}
func TestCancelHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        error
		status     int
		calls      int
	}{
		{"no body", "", nil, 200, 1}, {"object", "{}", nil, 200, 1},
		{"reason", `{"reason":"  conflict  "}`, nil, 200, 1},
		{"unknown allowed", `{"other":true}`, nil, 200, 1},
		{"unicode max", `{"reason":"` + strings.Repeat("á", 255) + `"}`, nil, 200, 1},
		{"NUL", `{"reason":"a\u0000b"}`, nil, 400, 0},
		{"padded boundary", `{"reason":" ` + strings.Repeat("á", 253) + ` "}`, nil, 200, 1},
		{"padded over limit", `{"reason":" ` + strings.Repeat("á", 255) + `"}`, nil, 400, 0},
		{"too long", `{"reason":"` + strings.Repeat("á", 256) + `"}`, nil, 400, 0},
		{"null object", "null", nil, 400, 0}, {"array", "[]", nil, 400, 0},
		{"null reason", `{"reason":null}`, nil, 400, 0}, {"numeric reason", `{"reason":7}`, nil, 400, 0},
		{"whitespace is not absent", " \n\t", nil, 400, 0},
		{"malformed", "{", nil, 400, 0}, {"trailing", `{} {}`, nil, 400, 0},
		{"oversize", `{"reason":"` + strings.Repeat("x", 17000) + `"}`, nil, 400, 0},
		{"missing role", "{}", a.ErrForbidden, 403, 1}, {"scope", "{}", a.ErrNotFound, 404, 1},
		{"reason required", "{}", a.ErrValidation, 400, 1}, {"deadline", "{}", a.ErrCancelDeadline, 422, 1},
		{"state", "{}", a.ErrState, 409, 1}, {"audit", "{}", errors.New("audit failed"), 500, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &cancelStore{err: tc.err}
			e := echo.New()
			New(a.New(s)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.profile", auth.Profile{ID: "no-mssv-organizer"})
					return next(c)
				}
			})
			req := httptest.NewRequest("POST", "/api/v1/registrations/reg/cancel", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.status || s.calls != tc.calls {
				t.Fatalf("status=%d body=%s calls=%d", rec.Code, rec.Body, s.calls)
			}
			if rec.Code == 200 {
				var out struct {
					Data struct {
						CancelledAt *time.Time
						CancelledBy string
						CheckinCode *string
					}
					Meta struct{ PromotedRegistrationID string }
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if s.actor != "no-mssv-organizer" || out.Data.CancelledAt == nil || out.Data.CancelledBy != "ORGANIZER" || out.Data.CheckinCode != nil || out.Meta.PromotedRegistrationID != "promoted" {
					t.Fatalf("%+v", out)
				}
				if tc.name == "padded boundary" && s.reason != strings.Repeat("á", 253) {
					t.Fatal(s.reason)
				}
				if tc.name == "reason" && s.reason != "conflict" {
					t.Fatal(s.reason)
				}
			}
		})
	}
}
