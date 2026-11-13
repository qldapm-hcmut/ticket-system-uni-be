package application

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type CancellationResult struct {
	Event         domain.Event
	AffectedCount int64
}

// Cancel locks the event before registrations, matching registration/check-in
// lock order. A zero version means no optional If-Match precondition was sent.
func (s *Service) Cancel(ctx context.Context, actor Actor, id string, version int32, reason string) (CancellationResult, error) {
	if !actor.IsOrganizer {
		return CancellationResult{}, domain.ErrForbidden
	}
	if strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 500 || version < 0 {
		return CancellationResult{}, domain.ErrValidation
	}
	var out CancellationResult
	err := s.uow.Do(ctx, func(ctx context.Context, r EventRepository) error {
		e, err := organizerEvent(ctx, r, actor.ID, id)
		if err != nil {
			return err
		}
		if version != 0 && version != e.Version {
			return domain.ErrVersion
		}
		if e.Status != "PUBLISHED" {
			return domain.ErrState
		}
		out.Event, out.AffectedCount, err = r.Cancel(ctx, e, actor.ID, reason)
		return err
	})
	if err != nil {
		return CancellationResult{}, err
	}
	return out, nil
}
