package application

import (
	"context"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	shared "github.com/qldapm-hcmut/ticket-system-uni/internal/shared/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type Actor struct {
	ID          string
	IsAdmin     bool
	IsOrganizer bool
}
type EventRepository interface {
	IsOrganizer(context.Context, string, string) (bool, error)
	Get(context.Context, string, bool) (domain.Event, error)
	Create(context.Context, string, string, domain.Fields) (domain.Event, error)
	Update(context.Context, domain.Event) (domain.Event, error)
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
	now := s.clock.Now()
	if err := f.Validate(now); err != nil {
		return domain.Event{}, err
	}
	if err := f.ValidateRegistrationDeadline(now); err != nil {
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

// Patch runs parsing/merging after fetching the current version under a row lock.
func (s *Service) Patch(ctx context.Context, actor Actor, id string, version int32, merge func(domain.Fields) (domain.Fields, error)) (domain.Event, error) {
	if !actor.IsOrganizer {
		return domain.Event{}, domain.ErrForbidden
	}
	var result domain.Event
	err := s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
		e, err := r.Get(ctx, id, true)
		if err != nil {
			return err
		}
		allowed, err := r.IsOrganizer(ctx, actor.ID, e.UnitID)
		if err != nil {
			return err
		}
		if !allowed {
			return domain.ErrNotFound
		}
		if e.Version != version {
			return domain.ErrVersion
		}
		if e.Status != "DRAFT" && e.Status != "REJECTED" && e.Status != "PUBLISHED" {
			return domain.ErrState
		}
		old := e.Fields
		e.Fields, err = merge(e.Fields)
		if err != nil {
			return err
		}
		e.SignificantChange = e.Status == "PUBLISHED" && (!old.StartAt.Equal(e.StartAt) || !old.EndAt.Equal(e.EndAt) || old.Location != e.Location)
		if e.SignificantChange && !s.clock.Now().Before(old.StartAt.Add(-time.Hour)) {
			return domain.ErrState
		}
		e.GrantCancellationException = e.SignificantChange && !s.clock.Now().Before(old.StartAt.Add(-24*time.Hour))
		e.ChangedBy = actor.ID
		if e.Capacity < e.RegisteredCount {
			return domain.ErrCapacity
		}
		if e.WaitlistLimit != nil && *e.WaitlistLimit < e.WaitlistCount {
			return domain.ErrValidation
		}
		if err = e.Fields.Validate(s.clock.Now()); err != nil {
			return err
		}
		if e.Status != "PUBLISHED" || !old.RegistrationClosesAt.Equal(e.RegistrationClosesAt) {
			if err = e.Fields.ValidateRegistrationDeadline(s.clock.Now()); err != nil {
				return err
			}
		}
		result, err = r.Update(ctx, e)
		return err
	})
	if err != nil {
		return domain.Event{}, err
	}
	return result, nil
}
