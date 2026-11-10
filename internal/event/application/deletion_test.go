package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type deleteRepo struct {
	EventRepository
	event                    domain.Event
	allowed                  bool
	getErr, scopeErr, delErr error
	calls                    []string
}

func (r *deleteRepo) Get(_ context.Context, _ string, lock bool) (domain.Event, error) {
	if !lock {
		panic("delete must lock event")
	}
	r.calls = append(r.calls, "lock")
	return r.event, r.getErr
}
func (r *deleteRepo) IsOrganizer(_ context.Context, actor, unit string) (bool, error) {
	if actor != "actor" || unit != "unit" {
		panic("incorrect scope check")
	}
	r.calls = append(r.calls, "scope")
	return r.allowed, r.scopeErr
}
func (r *deleteRepo) DeleteDraft(_ context.Context, id string) error {
	if id != "event" {
		panic("incorrect event")
	}
	r.calls = append(r.calls, "delete")
	return r.delErr
}

type deleteUOW struct {
	repo  *deleteRepo
	calls int
	err   error
}

func (u *deleteUOW) Do(ctx context.Context, fn func(context.Context, EventRepository) error) error {
	u.calls++
	if err := fn(ctx, u.repo); err != nil {
		return err
	}
	return u.err
}

func TestDeleteDraftPolicy(t *testing.T) {
	broken := errors.New("storage failure")
	organizer := Actor{ID: "actor", IsOrganizer: true}
	for _, tc := range []struct {
		name                     string
		actor                    Actor
		status                   string
		allowed                  bool
		getErr, scopeErr, delErr error
		commitErr, want          error
		calls                    []string
	}{
		{name: "assigned non-creator", actor: organizer, status: "DRAFT", allowed: true, calls: []string{"lock", "scope", "delete"}},
		{name: "admin and organizer", actor: Actor{ID: "actor", IsOrganizer: true, IsAdmin: true}, status: "DRAFT", allowed: true, calls: []string{"lock", "scope", "delete"}},
		{name: "admin alone", actor: Actor{IsAdmin: true}, want: domain.ErrForbidden},
		{name: "no organizer role", want: domain.ErrForbidden},
		{name: "missing", actor: organizer, getErr: domain.ErrNotFound, want: domain.ErrNotFound, calls: []string{"lock"}},
		{name: "malformed", actor: organizer, getErr: domain.ErrValidation, want: domain.ErrValidation, calls: []string{"lock"}},
		{name: "outside active unit scope hides state", actor: organizer, status: "PUBLISHED", want: domain.ErrNotFound, calls: []string{"lock", "scope"}},
		{name: "get failure", actor: organizer, getErr: broken, want: broken, calls: []string{"lock"}},
		{name: "scope failure", actor: organizer, scopeErr: broken, want: broken, calls: []string{"lock", "scope"}},
		{name: "delete failure", actor: organizer, status: "DRAFT", allowed: true, delErr: broken, want: broken, calls: []string{"lock", "scope", "delete"}},
		{name: "commit failure", actor: organizer, status: "DRAFT", allowed: true, commitErr: broken, want: broken, calls: []string{"lock", "scope", "delete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &deleteRepo{event: domain.Event{ID: "event", UnitID: "unit", CreatedBy: "someone-else", Status: tc.status}, allowed: tc.allowed, getErr: tc.getErr, scopeErr: tc.scopeErr, delErr: tc.delErr}
			u := &deleteUOW{repo: r, err: tc.commitErr}
			err := New(u, nil).DeleteDraft(context.Background(), tc.actor, "event")
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(r.calls, tc.calls) {
				t.Fatalf("error=%v want=%v calls=%v want=%v", err, tc.want, r.calls, tc.calls)
			}
			if !tc.actor.IsOrganizer && u.calls != 0 {
				t.Fatal("role denial accessed storage")
			}
		})
	}
	for _, status := range []string{"PENDING_APPROVAL", "REJECTED", "PUBLISHED", "CANCELLED", "COMPLETED"} {
		t.Run(status, func(t *testing.T) {
			r := &deleteRepo{event: domain.Event{UnitID: "unit", Status: status}, allowed: true}
			err := New(&deleteUOW{repo: r}, nil).DeleteDraft(context.Background(), organizer, "event")
			if !errors.Is(err, domain.ErrState) || !reflect.DeepEqual(r.calls, []string{"lock", "scope"}) {
				t.Fatal(err, r.calls)
			}
		})
	}
}
