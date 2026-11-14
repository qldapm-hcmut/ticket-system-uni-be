package application

import (
	"context"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	shared "github.com/qldapm-hcmut/ticket-system-uni/internal/shared/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type Actor struct {
	ID          string
	IsAdmin     bool
	IsOrganizer bool
	IsStudent   bool
}
type EventRepository interface {
	IsOrganizer(context.Context, string, string) (bool, error)
	Get(context.Context, string, bool) (domain.Event, error)
	Create(context.Context, string, string, domain.Fields) (domain.Event, error)
	Update(context.Context, domain.Event) (domain.Event, error)
	DeleteDraft(context.Context, string) error
	Cancel(context.Context, domain.Event, string, string) (domain.Event, int64, error)
}
type UnitOfWork interface {
	shared.UnitOfWork[EventRepository]
}
type Service struct {
	uow   UnitOfWork
	clock kernel.Clock
}

func New(uow UnitOfWork, clock kernel.Clock) *Service { return &Service{uow, clock} }

func (s *Service) Create(ctx context.Context, actor Actor, unit string, f domain.Fields) (domain.Event, error) {
	if !actor.IsOrganizer {
		return domain.Event{}, domain.ErrForbidden
	}
	if err := f.Validate(s.clock.Now()); err != nil {
		return domain.Event{}, err
	}
	var result domain.Event
	err := s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
		allowed, err := r.IsOrganizer(ctx, actor.ID, unit)
		if err != nil {
			return err
		}
		if !allowed {
			return domain.ErrNotFound
		}
		result, err = r.Create(ctx, unit, actor.ID, f)
		return err
	})
	if err != nil {
		return domain.Event{}, err
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, actor Actor, id string) (domain.Event, error) {
	var result domain.Event
	err := s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
		e, err := r.Get(ctx, id, false)
		if err != nil {
			return err
		}
		if !actor.IsAdmin {
			allowed, err := r.IsOrganizer(ctx, actor.ID, e.UnitID)
			if err != nil {
				return err
			}
			if !allowed {
				return domain.ErrNotFound
			}
		}
		result = e
		return nil
	})
	return result, err
}

// organizerEvent locks before checking scope so each mutation makes its state
// and version decisions on the same authorized event, within its transaction.
func organizerEvent(ctx context.Context, r EventRepository, actor, id string) (domain.Event, error) {
	e, err := r.Get(ctx, id, true)
	if err != nil {
		return domain.Event{}, err
	}
	allowed, err := r.IsOrganizer(ctx, actor, e.UnitID)
	if err != nil {
		return domain.Event{}, err
	}
	if !allowed {
		return domain.Event{}, domain.ErrNotFound
	}
	return e, nil
}

// Patch locks the current event, checks organizer scope, prepares a valid edit,
// then persists it together with its notification and registration effects.
func (s *Service) Patch(ctx context.Context, actor Actor, id string, version int32, merge func(domain.Fields) (domain.Fields, error)) (domain.Event, error) {
	if !actor.IsOrganizer {
		return domain.Event{}, domain.ErrForbidden
	}
	var result domain.Event
	err := s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
		current, err := organizerEvent(ctx, r, actor.ID, id)
		if err != nil {
			return err
		}
		edited, err := s.prepareEventEdit(current, actor, version, merge)
		if err != nil {
			return err
		}
		result, err = r.Update(ctx, edited)
		return err
	})
	if err != nil {
		return domain.Event{}, err
	}
	return result, nil
}
