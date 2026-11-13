package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

// applySignificantEventChange keeps exceptions, notifications and audit in the
// caller's event-update transaction, before any capacity-driven promotion.
func (r *repository) applySignificantEventChange(ctx context.Context, eventID pgtype.UUID, previous db.Event, edited domain.Event) (int64, error) {
	granted, err := r.q.UpdateCancellationExceptions(ctx, db.UpdateCancellationExceptionsParams{
		EventID:        eventID,
		NewStart:       stamp(edited.StartAt),
		GrantException: edited.GrantCancellationException,
	})
	if err != nil {
		return 0, err
	}
	notified, err := r.q.NotifyEventChanged(ctx, eventID)
	if err != nil {
		return 0, err
	}

	actorID, err := id(edited.ChangedBy)
	if err != nil {
		return 0, err
	}
	metadata, err := json.Marshal(map[string]any{
		"isSignificant":         true,
		"exceptionGrantedCount": granted,
		"oldStartAt":            previous.StartAt.Time,
		"newStartAt":            edited.StartAt,
		"oldEndAt":              previous.EndAt.Time,
		"newEndAt":              edited.EndAt,
		"oldLocation":           previous.Location,
		"newLocation":           edited.Location,
	})
	if err != nil {
		return 0, err
	}
	if err := r.q.AuditEventChange(ctx, db.AuditEventChangeParams{
		ActorID:  actorID,
		EventID:  eventID,
		Metadata: metadata,
	}); err != nil {
		return 0, err
	}
	return notified, nil
}
