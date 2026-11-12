package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type scheduleClock struct{ now time.Time }

func (c scheduleClock) Now() time.Time { return c.now }

func scheduleFields(now time.Time) domain.Fields {
	return domain.Fields{Title: "Demo", Location: "Campus", Capacity: 1, StartAt: now.Add(2 * time.Hour), EndAt: now.Add(3 * time.Hour), RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour)}
}

type scheduleRepo struct {
	EventRepository
	event   domain.Event
	written bool
}

func (r *scheduleRepo) IsOrganizer(context.Context, string, string) (bool, error) { return true, nil }
func (r *scheduleRepo) Get(context.Context, string, bool) (domain.Event, error)   { return r.event, nil }
func (r *scheduleRepo) Create(_ context.Context, _, _ string, f domain.Fields) (domain.Event, error) {
	r.written = true
	return domain.Event{Fields: f}, nil
}
func (r *scheduleRepo) Update(_ context.Context, e domain.Event) (domain.Event, error) {
	r.written = true
	return e, nil
}

type scheduleUOW struct{ repo *scheduleRepo }

func (u scheduleUOW) Do(ctx context.Context, fn func(context.Context, EventRepository) error) error {
	return fn(ctx, u.repo)
}

func TestCreateAndEditRegistrationDeadline(t *testing.T) {
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	actor := Actor{ID: "owner", IsOrganizer: true}
	for _, tc := range []struct {
		name, status                      string
		create, changeDeadline, wantError bool
	}{
		{"create closed window", "", true, false, true},
		{"edit expired draft", "DRAFT", false, false, true},
		{"edit expired rejected event", "REJECTED", false, false, true},
		{"edit published title after closing", "PUBLISHED", false, false, false},
		{"move published deadline into past", "PUBLISHED", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := scheduleFields(now)
			f.RegistrationClosesAt = now.Add(-time.Minute)
			r := &scheduleRepo{event: domain.Event{Fields: f, Status: tc.status, Version: 1}}
			s := New(scheduleUOW{r}, scheduleClock{now})
			var err error
			if tc.create {
				_, err = s.Create(context.Background(), actor, "unit", f)
			} else {
				_, err = s.Patch(context.Background(), actor, "event", 1, func(f domain.Fields) (domain.Fields, error) {
					f.Title = "Edited"
					if tc.changeDeadline {
						f.RegistrationClosesAt = now.Add(-2 * time.Minute)
					}
					return f, nil
				})
			}
			if tc.wantError {
				var field *domain.FieldError
				if !errors.As(err, &field) || field.Field != "registrationClosesAt" || r.written {
					t.Fatalf("err=%v written=%v", err, r.written)
				}
			} else if err != nil || !r.written {
				t.Fatalf("err=%v written=%v", err, r.written)
			}
		})
	}
}

type scheduleApprovalRepo struct {
	ApprovalRepository
	event   domain.Event
	written bool
}

func (r *scheduleApprovalRepo) Get(context.Context, string, bool) (domain.Event, error) {
	return r.event, nil
}
func (r *scheduleApprovalRepo) IsOrganizer(context.Context, string, string) (bool, error) {
	return true, nil
}
func (r *scheduleApprovalRepo) Submit(context.Context, string, int32) (domain.Event, error) {
	r.written = true
	return r.event, nil
}
func (r *scheduleApprovalRepo) Approve(context.Context, string, int32, string) (domain.Event, error) {
	r.written = true
	return r.event, nil
}
func (r *scheduleApprovalRepo) Audit(context.Context, string, string, string, string) error {
	return nil
}

type scheduleApprovalUOW struct{ repo *scheduleApprovalRepo }

func (u scheduleApprovalUOW) DoApproval(_ context.Context, fn func(ApprovalRepository) error) error {
	return fn(u.repo)
}

func TestApprovalRechecksSchedule(t *testing.T) {
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	for _, approve := range []bool{false, true} {
		for _, kind := range []string{"valid", "deadline elapsed", "event started"} {
			t.Run(kind+map[bool]string{true: " approve", false: " submit"}[approve], func(t *testing.T) {
				f := scheduleFields(now)
				if kind == "deadline elapsed" {
					f.RegistrationClosesAt = now
				}
				if kind == "event started" {
					f.StartAt = now
				}
				r := &scheduleApprovalRepo{event: domain.Event{Fields: f, CreatedBy: "owner", Status: map[bool]string{true: "PENDING_APPROVAL", false: "DRAFT"}[approve]}}
				s := NewApprovalService(scheduleApprovalUOW{r}, scheduleClock{now})
				var err error
				if approve {
					_, err = s.Approve(context.Background(), Actor{ID: "admin", IsAdmin: true}, "event", 0)
				} else {
					_, err = s.Submit(context.Background(), Actor{ID: "owner", IsOrganizer: true}, "event", 0)
				}
				if kind == "valid" {
					if err != nil || !r.written {
						t.Fatalf("err=%v written=%v", err, r.written)
					}
				} else if !errors.Is(err, domain.ErrValidation) || r.written {
					t.Fatalf("err=%v written=%v", err, r.written)
				}
			})
		}
	}
}
