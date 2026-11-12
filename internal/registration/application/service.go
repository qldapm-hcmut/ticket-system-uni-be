package application

import (
	"context"
	"errors"
	shared "github.com/qldapm-hcmut/ticket-system-uni/internal/shared/application"
	"time"
)

type Registration struct {
	ID, EventID, StudentID, Status, CheckinCode string
	QueueSeq                                    int64
	CreatedAt                                   time.Time
	Event                                       *EventSummary
	WaitlistPosition                            *int64
}
type CancellationResult struct {
	Registration Registration
	Promoted     *Registration
}

var (
	ErrValidation     = errors.New("invalid registration input")
	ErrState          = errors.New("invalid registration state")
	ErrCancelDeadline = errors.New("cancellation deadline passed")
	ErrForbidden      = errors.New("student role required")
	ErrNotFound       = errors.New("registration not found")
	ErrAlreadyActive  = errors.New("already registered")
	ErrClosed         = errors.New("registration closed")
	ErrFull           = errors.New("event full")
)

type RegistrationRepository interface {
	Register(context.Context, string, string) (Registration, error)
	Active(context.Context, string, string) (Registration, error)
	Cancel(context.Context, string, string) (CancellationResult, error)
	Mine(context.Context, string, string, bool, int32, int32) ([]Registration, int64, error)
}
type UnitOfWork interface {
	shared.UnitOfWork[RegistrationRepository]
}
type Service struct{ uow UnitOfWork }

func New(u UnitOfWork) *Service { return &Service{u} }
func (s *Service) Register(ctx context.Context, student, event string) (Registration, error) {
	var out Registration
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		a, e := r.Active(ctx, event, student)
		if e == nil && a.ID != "" {
			return ErrAlreadyActive
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		out, e = r.Register(ctx, event, student)
		return e
	})
	return out, err
}
func (s *Service) Cancel(ctx context.Context, student, id string) (CancellationResult, error) {
	var out CancellationResult
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		var e error
		out, e = r.Cancel(ctx, id, student)
		return e
	})
	return out, err
}
func (s *Service) Mine(ctx context.Context, student, status string, upcoming bool, page, size int32) ([]Registration, int64, error) {
	var out []Registration
	var count int64
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		var e error
		out, count, e = r.Mine(ctx, student, status, upcoming, (page-1)*size, size)
		return e
	})
	return out, count, err
}
