package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	shared "github.com/qldapm-hcmut/ticket-system-uni/internal/shared/application"
)

type Registration struct {
	ID, EventID, StudentID, Status, CheckinCode string
	QueueSeq                                    int64
	WaitlistPosition                            *int64
	CreatedAt                                   time.Time
	CancelledAt                                 *time.Time
	CancelledBy                                 *string
}
type CancellationResult struct {
	Registration Registration
	Promoted     *Registration
}

var (
	ErrValidation     = errors.New("invalid registration input")
	ErrState          = errors.New("invalid registration state")
	ErrCancelDeadline = errors.New("cancellation deadline passed")
	ErrForbidden      = errors.New("required role missing")
	ErrNotFound       = errors.New("registration not found")
	ErrAlreadyActive  = errors.New("already registered")
	ErrClosed         = errors.New("registration closed")
	ErrFull           = errors.New("event full")
)

type RegistrationRepository interface {
	Register(context.Context, string, string) (Registration, error)
	Active(context.Context, string, string) (Registration, error)
	Cancel(context.Context, string, string, string) (CancellationResult, error)
	// Mine must be the first and only repository operation in a fresh unit of
	// work: the adapter establishes a read-only snapshot and DB decision time.
	Mine(context.Context, string, MineFilter) ([]MyRegistration, int64, error)
}
type UnitOfWork interface {
	shared.UnitOfWork[RegistrationRepository]
}
type Service struct {
	uow UnitOfWork
}

func New(u UnitOfWork) *Service { return &Service{uow: u} }

// AlreadyRegistered identifies the active registration, including insert races.
type AlreadyRegistered struct{ RegistrationID string }

func (e *AlreadyRegistered) Error() string { return ErrAlreadyActive.Error() }
func (e *AlreadyRegistered) Unwrap() error { return ErrAlreadyActive }
func (s *Service) Register(ctx context.Context, student, event string) (Registration, error) {
	var out Registration
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		a, e := r.Active(ctx, event, student)
		if e == nil && a.ID != "" {
			return &AlreadyRegistered{RegistrationID: a.ID}
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		out, e = r.Register(ctx, event, student)
		return e
	})
	// An insert unique violation aborts the transaction. Do returns only after
	// rollback; resolve the winner through a new transaction, never the failed one.
	var duplicate *AlreadyRegistered
	if errors.Is(err, ErrAlreadyActive) && !errors.As(err, &duplicate) {
		readErr := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
			winner, e := r.Active(ctx, event, student)
			if errors.Is(e, ErrNotFound) {
				// The winner can be cancelled before this fresh read. The insert
				// still conflicted; do not misreport a missing event or stale ID.
				return ErrAlreadyActive
			}
			if e != nil {
				return e
			}
			return &AlreadyRegistered{RegistrationID: winner.ID}
		})
		return Registration{}, readErr
	}
	return out, err
}

// Cancel accepts an empty reason for body-less student cancellations.
func (s *Service) Cancel(ctx context.Context, actor, id, reason string) (CancellationResult, error) {
	var out CancellationResult
	// OpenAPI maxLength applies to raw input; only the stored value is trimmed.
	if !utf8.ValidString(reason) || strings.ContainsRune(reason, '\x00') || utf8.RuneCountInString(reason) > 255 {
		return out, ErrValidation
	}
	text := strings.TrimSpace(reason)
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		var e error
		out, e = r.Cancel(ctx, id, actor, text)
		return e
	})
	return out, err
}

type MineFilter struct {
	Status         string
	Upcoming       bool
	Page, PageSize int32
}

type MyRegistration struct {
	Registration
	Event EventSummary
}

func (s *Service) Mine(ctx context.Context, student string, f MineFilter) ([]MyRegistration, int64, error) {
	if f.Page < 1 || f.PageSize < 1 || f.PageSize > 100 || int64(f.Page-1)*int64(f.PageSize) > 2147483647 {
		return nil, 0, ErrValidation
	}
	switch f.Status {
	case "", "REGISTERED", "WAITLISTED", "CANCELLED", "CANCELLED_BY_EVENT", "EXPIRED":
	default:
		return nil, 0, ErrValidation
	}
	out := make([]MyRegistration, 0)
	var count int64
	err := s.uow.Do(ctx, func(ctx context.Context, r RegistrationRepository) error {
		rows, n, e := r.Mine(ctx, student, f)
		if rows != nil {
			out = rows
		}
		count = n
		return e
	})
	return out, count, err
}
