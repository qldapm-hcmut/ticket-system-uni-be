package application

import (
	"context"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

// DeleteDraft serializes scope/state checks and deletion with event mutations.
func (s *Service) DeleteDraft(ctx context.Context, actor Actor, id string) error {
	if !actor.IsOrganizer {
		return domain.ErrForbidden
	}
	return s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
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
		if e.Status != "DRAFT" {
			return domain.ErrState
		}
		return r.DeleteDraft(ctx, e.ID)
	})
}
