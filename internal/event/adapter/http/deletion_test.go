package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type deletionRepo struct {
	a.EventRepository
	status         string
	allowed        bool
	getErr, delErr error
	deleted        bool
}

func (r *deletionRepo) Get(_ context.Context, _ string, lock bool) (domain.Event, error) {
	if !lock {
		panic("missing lock")
	}
	return domain.Event{ID: "event", UnitID: "unit", Status: r.status}, r.getErr
}
func (r *deletionRepo) IsOrganizer(context.Context, string, string) (bool, error) {
	return r.allowed, nil
}
func (r *deletionRepo) DeleteDraft(context.Context, string) error { r.deleted = true; return r.delErr }

type deletionUOW struct {
	repo  *deletionRepo
	calls int
}

func (u *deletionUOW) Do(ctx context.Context, fn func(context.Context, a.EventRepository) error) error {
	u.calls++
	return fn(ctx, u.repo)
}

func TestDeleteDraftHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, state, code         string
		organizer, admin, allowed bool
		getErr, delErr            error
		status                    int
	}{
		{name: "success", state: "DRAFT", organizer: true, allowed: true, status: 204},
		{name: "admin organizer", state: "DRAFT", organizer: true, admin: true, allowed: true, status: 204},
		{name: "admin only", admin: true, status: 403, code: "FORBIDDEN"},
		{name: "no role", status: 403, code: "FORBIDDEN"},
		{name: "outside scope hides state", state: "PUBLISHED", organizer: true, status: 404, code: "NOT_FOUND"},
		{name: "missing", organizer: true, getErr: domain.ErrNotFound, status: 404, code: "NOT_FOUND"},
		{name: "malformed ID", organizer: true, getErr: domain.ErrValidation, status: 400, code: "VALIDATION_ERROR"},
		{name: "pending", state: "PENDING_APPROVAL", organizer: true, allowed: true, status: 409, code: "INVALID_STATE_TRANSITION"},
		{name: "rejected", state: "REJECTED", organizer: true, allowed: true, status: 409, code: "INVALID_STATE_TRANSITION"},
		{name: "published", state: "PUBLISHED", organizer: true, allowed: true, status: 409, code: "INVALID_STATE_TRANSITION"},
		{name: "cancelled", state: "CANCELLED", organizer: true, allowed: true, status: 409, code: "INVALID_STATE_TRANSITION"},
		{name: "completed", state: "COMPLETED", organizer: true, allowed: true, status: 409, code: "INVALID_STATE_TRANSITION"},
		{name: "storage error is not success", state: "DRAFT", organizer: true, allowed: true, delErr: errors.New("failure"), status: 500, code: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &deletionRepo{status: tc.state, allowed: tc.allowed, getErr: tc.getErr, delErr: tc.delErr}
			u := &deletionUOW{repo: r}
			e := echo.New()
			New(a.New(u, nil), nil).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					p := auth.Profile{ID: "actor", IsAdmin: tc.admin}
					if tc.organizer {
						p.Memberships = []auth.Membership{{}}
					}
					c.Set("auth.profile", p)
					return next(c)
				}
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/v1/events/event", nil))
			if rec.Code != tc.status {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.status == 204 {
				if !r.deleted || rec.Body.Len() != 0 {
					t.Fatal("204 must be empty after deletion")
				}
			} else {
				var body struct{ Error struct{ Code string } }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != tc.code {
					t.Fatal(rec.Body.String(), err)
				}
				if tc.status != 500 && r.deleted {
					t.Fatal("denied request mutated storage")
				}
			}
			if !tc.organizer && u.calls != 0 {
				t.Fatal("generic role denial must precede lookup")
			}
		})
	}
}
