package application

import (
	"context"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type ApprovalRepository interface {
	Get(context.Context, string, bool) (domain.Event, error)
	IsOrganizer(context.Context, string, string) (bool, error)
	Submit(context.Context, string, int32) (domain.Event, error)
	Approve(context.Context, string, int32, string) (domain.Event, error)
	Reject(context.Context, string, int32, string) (domain.Event, error)
	Pending(context.Context, int32, int32) ([]domain.Event, int64, error)
	Audit(context.Context, string, string, string, string) error
}
type ApprovalUnitOfWork interface {
	DoApproval(context.Context, func(ApprovalRepository) error) error
}
type ApprovalService struct{ uow ApprovalUnitOfWork }

func NewApprovalService(u ApprovalUnitOfWork) *ApprovalService { return &ApprovalService{u} }

// Submit locks the event, checks organizer scope and state, then records the
// submission and its audit entry in the same transaction.
func (s *ApprovalService) Submit(ctx context.Context, a Actor, id string, version int32) (domain.Event, error) {
	var result domain.Event
	err := s.uow.DoApproval(ctx, func(r ApprovalRepository) error {
		current, err := r.Get(ctx, id, true)
		if err != nil {
			return err
		}
		allowed, err := r.IsOrganizer(ctx, a.ID, current.UnitID)
		if err != nil {
			return err
		}
		if !allowed {
			return domain.ErrNotFound
		}
		if version != 0 && current.Version != version {
			return domain.ErrVersion
		}
		switch current.Status {
		case "DRAFT", "REJECTED":
			// Both a new draft and a revised rejection can be submitted.
		default:
			return domain.ErrState
		}

		result, err = r.Submit(ctx, id, current.Version)
		if err != nil {
			return err
		}
		return r.Audit(ctx, a.ID, id, "SUBMIT", "")
	})
	return result, err
}

// Approve checks the administrator before locking, then checks self-approval,
// version and pending state before publishing and auditing atomically.
func (s *ApprovalService) Approve(ctx context.Context, a Actor, id string, version int32) (domain.Event, error) {
	if !a.IsAdmin {
		return domain.Event{}, domain.ErrForbidden
	}
	var result domain.Event
	err := s.uow.DoApproval(ctx, func(r ApprovalRepository) error {
		current, err := r.Get(ctx, id, true)
		if err != nil {
			return err
		}
		if current.CreatedBy == a.ID {
			return domain.ErrForbidden
		}
		if version != 0 && current.Version != version {
			return domain.ErrVersion
		}
		if current.Status != "PENDING_APPROVAL" {
			return domain.ErrState
		}

		result, err = r.Approve(ctx, id, current.Version, a.ID)
		if err != nil {
			return err
		}
		return r.Audit(ctx, a.ID, id, "APPROVE", "")
	})
	return result, err
}

func (s *ApprovalService) Reject(ctx context.Context, a Actor, id string, version int32, reason string) (domain.Event, error) {
	if !a.IsAdmin || len(reason) == 0 || len(reason) > 500 {
		return domain.Event{}, domain.ErrValidation
	}
	var result domain.Event
	err := s.uow.DoApproval(ctx, func(r ApprovalRepository) error {
		current, err := r.Get(ctx, id, true)
		if err != nil {
			return err
		}
		if version != 0 && current.Version != version {
			return domain.ErrVersion
		}
		if current.Status != "PENDING_APPROVAL" {
			return domain.ErrState
		}

		result, err = r.Reject(ctx, id, current.Version, reason)
		if err != nil {
			return err
		}
		return r.Audit(ctx, a.ID, id, "REJECT", reason)
	})
	return result, err
}
