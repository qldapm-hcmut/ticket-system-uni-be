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
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type approvalResponseRepo struct {
	a.ApprovalRepository
	event domain.Event
}

func (r *approvalResponseRepo) Get(context.Context, string, bool) (domain.Event, error) {
	return r.event, nil
}
func (r *approvalResponseRepo) IsOrganizer(context.Context, string, string) (bool, error) {
	return true, nil
}
func (r *approvalResponseRepo) Submit(context.Context, string, int32) (domain.Event, error) {
	r.event.Status = "PENDING_APPROVAL"
	r.event.Version++
	return r.event, nil
}
func (r *approvalResponseRepo) Approve(context.Context, string, int32, string) (domain.Event, error) {
	r.event.Status = "PUBLISHED"
	r.event.Version++
	return r.event, nil
}
func (r *approvalResponseRepo) Reject(context.Context, string, int32, string) (domain.Event, error) {
	r.event.Status = "REJECTED"
	r.event.Version++
	return r.event, nil
}
func (r *approvalResponseRepo) Audit(context.Context, string, string, string, string) error {
	return nil
}

type approvalResponseUOW struct{ repo *approvalResponseRepo }

func (u approvalResponseUOW) DoApproval(ctx context.Context, fn func(a.ApprovalRepository) error) error {
	return fn(u.repo)
}

type approvalResponseReader struct {
	a.Reader
	repo  *approvalResponseRepo
	err   error
	calls int
}

func (r *approvalResponseReader) Detail(_ context.Context, id string) (map[string]any, error) {
	r.calls++
	if id != r.repo.event.ID {
		return nil, errors.New("wrong event ID")
	}
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{"id": id, "status": r.repo.event.Status, "version": r.repo.event.Version, "unit": map[string]any{"id": "unit"}}, nil
}

func TestApprovalResponsesUseEventReadModel(t *testing.T) {
	for _, action := range []string{"submit", "approve", "reject"} {
		for _, readFails := range []bool{false, true} {
			name := action
			if readFails {
				name += "/read failure"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
				status := "PENDING_APPROVAL"
				want := "PUBLISHED"
				if action == "submit" {
					status = "DRAFT"
					want = "PENDING_APPROVAL"
				}
				if action == "reject" {
					want = "REJECTED"
				}
				repo := &approvalResponseRepo{event: domain.Event{ID: "event", UnitID: "unit", CreatedBy: "creator", Status: status, Version: 1, Fields: domain.Fields{Title: "Test", Location: "Hall", Capacity: 2, StartAt: now.Add(48 * time.Hour), EndAt: now.Add(49 * time.Hour), RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(24 * time.Hour)}}}
				reader := &approvalResponseReader{repo: repo}
				if readFails {
					reader.err = errors.New("private database failure")
				}
				e := echo.New()
				NewApprovalHandler(a.NewApprovalService(approvalResponseUOW{repo}, patchClock{now}), reader).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
					return func(c echo.Context) error {
						c.Set("auth.profile", auth.Profile{ID: "actor", IsAdmin: true, Memberships: []auth.Membership{{}}})
						return next(c)
					}
				})
				req := httptest.NewRequest("POST", "/api/v1/events/event/"+action, strings.NewReader(`{"reason":"Needs revision"}`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if readFails {
					if rec.Code != 500 || strings.Contains(rec.Body.String(), "private database failure") {
						t.Fatalf("unexpected read failure response: %d %s", rec.Code, rec.Body.String())
					}
				} else {
					var body struct{ Data map[string]any }
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if rec.Code != 200 || body.Data["id"] != "event" || body.Data["status"] != want || body.Data["version"] != float64(2) || body.Data["unit"] == nil {
						t.Fatalf("wrong Event response: %d %s", rec.Code, rec.Body.String())
					}
					for _, key := range []string{"ID", "Status", "Version", "GrantCancellationException", "ChangedBy"} {
						if _, exists := body.Data[key]; exists {
							t.Fatalf("leaked domain field %s", key)
						}
					}
				}
				if reader.calls != 1 {
					t.Fatalf("Detail calls=%d, want 1", reader.calls)
				}
			})
		}
	}
}
