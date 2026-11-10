package postgres

import (
	"context"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

func (r *repository) DeleteDraft(ctx context.Context, raw string) error {
	u, err := id(raw)
	if err != nil {
		return err
	}
	// Keep NO ACTION foreign keys intact: never cascade or discard history.
	n, err := r.q.DeleteDraftEvent(ctx, u)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
