package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

func (r *repo) Cancel(ctx context.Context, id, actor, reason string) (a.CancellationResult, error) {
	registrationID, err := uuid(id)
	if err != nil {
		return a.CancellationResult{}, err
	}
	actorID, err := uuid(actor)
	if err != nil {
		return a.CancellationResult{}, err
	}

	// Authorize before waiting, then lock the event and make a fresh decision.
	decision, err := r.prepareCancellation(ctx, registrationID, actorID, reason)
	if err != nil {
		return a.CancellationResult{}, err
	}
	cancelled, err := r.cancelRegistration(ctx, registrationID, actorID, reason, decision)
	if err != nil {
		return a.CancellationResult{}, err
	}

	// Only a registered cancellation releases a seat for the FIFO waitlist.
	var promoted *a.Registration
	if decision.original.Status == db.RegistrationStatusREGISTERED {
		promoted, err = r.promoteWaitlistHead(ctx, decision.original.EventID, decision.at)
		if err != nil {
			return a.CancellationResult{}, err
		}
	}
	// Record on-behalf cancellations in the same transaction.
	if decision.by == db.CancelActorORGANIZER {
		n, err := r.q.AuditRegistrationCancellation(ctx, registrationID)
		if err != nil {
			return a.CancellationResult{}, fmt.Errorf("audit cancellation %s: %w", id, err)
		}
		if n != 1 {
			return a.CancellationResult{}, fmt.Errorf("audit cancellation %s: expected 1 row, got %d", id, n)
		}
	}
	return a.CancellationResult{Registration: cancelled, Promoted: promoted}, nil
}

// The event stays locked for the transaction. Reuse this single post-lock
// instant for deadline checks, cancellation and promotion, even across start.
type cancellationDecision struct {
	original db.Registration
	by       db.CancelActor
	at       pgtype.Timestamptz
}

func (r *repo) prepareCancellation(ctx context.Context, id, actor pgtype.UUID, reason string) (cancellationDecision, error) {
	roles, err := r.cancellationRoles(ctx, actor)
	if err != nil {
		return cancellationDecision{}, err
	}
	original, err := r.q.GetRegistration(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return cancellationDecision{}, a.ErrNotFound
	}
	if err != nil {
		return cancellationDecision{}, err
	}
	// Non-locking owner/scope preflight. Do not queue unauthorized callers on
	// the event row. Event/registration ownership is immutable in this API.
	by, err := r.cancellationActor(ctx, original, actor, nil, roles)
	if err != nil {
		return cancellationDecision{}, err
	}
	if by == db.CancelActorORGANIZER && reason == "" {
		return cancellationDecision{}, a.ErrValidation
	}

	event, err := r.q.GetEventForRegistration(ctx, original.EventID)
	if err != nil {
		return cancellationDecision{}, err
	}
	roles, err = r.cancellationRoles(ctx, actor)
	if err != nil {
		return cancellationDecision{}, err
	}
	original, err = r.q.GetRegistration(ctx, id)
	if err != nil {
		return cancellationDecision{}, err
	}
	by, err = r.cancellationActor(ctx, original, actor, &event, roles)
	if err != nil {
		return cancellationDecision{}, err
	}
	// Transaction-start now() may predate a long lock wait. Read the clock
	// only after the lock and authoritative authorization checks.
	decisionAt, err := r.q.GetCancellationDecisionTime(ctx)
	if err != nil {
		return cancellationDecision{}, err
	}
	if original.Status == db.RegistrationStatusWAITLISTED && !decisionAt.Time.Before(event.StartAt.Time) {
		return cancellationDecision{}, a.ErrState // Effectively EXPIRED, even before the worker persists it.
	}
	return cancellationDecision{original: original, by: by, at: decisionAt}, nil
}

// cancellationRoles must run before looking up the target and again after the
// event lock: preflight prevents existence leaks and unauthorized lock waits;
// only the fresh post-lock result is authoritative.
func (r *repo) cancellationRoles(ctx context.Context, actor pgtype.UUID) (db.GetUserRolesRow, error) {
	roles, err := r.q.GetUserRoles(ctx, actor)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !roles.IsStudent && !roles.IsOrganizer) {
		return roles, a.ErrForbidden
	}
	return roles, err
}

// A nil event is the non-locking preflight; after locking, pass the event
// already read. Only on-behalf cancellation needs an event scope lookup.
func (r *repo) cancellationActor(ctx context.Context, original db.Registration, actor pgtype.UUID, event *db.Event, roles db.GetUserRolesRow) (db.CancelActor, error) {
	if original.StudentID == actor {
		// Ownership always wins over staff privileges, including Admin.
		if !roles.IsStudent {
			return "", a.ErrForbidden
		}
		return db.CancelActorSTUDENT, nil
	}
	if !roles.IsOrganizer {
		return "", a.ErrNotFound
	}
	if event == nil {
		row, err := r.q.GetEvent(ctx, original.EventID)
		if err != nil {
			return "", err
		}
		event = &row
	}
	allowed, err := r.q.IsUnitOrganizer(ctx, db.IsUnitOrganizerParams{UserID: actor, UnitID: event.UnitID})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", a.ErrNotFound
	}
	return db.CancelActorORGANIZER, nil
}

func (r *repo) cancelRegistration(ctx context.Context, id, actor pgtype.UUID, reason string, decision cancellationDecision) (a.Registration, error) {
	row, err := r.q.CancelRegistration(ctx, db.CancelRegistrationParams{
		ID: id, CancelledBy: db.NullCancelActor{CancelActor: decision.by, Valid: true}, ActorID: actor,
		Reason: pgtype.Text{String: reason, Valid: reason != ""}, DecisionAt: decision.at,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if decision.original.Status != db.RegistrationStatusREGISTERED && decision.original.Status != db.RegistrationStatusWAITLISTED {
			return a.Registration{}, a.ErrState
		}
		return a.Registration{}, a.ErrCancelDeadline
	}
	if err != nil {
		return a.Registration{}, err
	}
	if err = r.q.DecrementRegistrationCounter(ctx, db.DecrementRegistrationCounterParams{
		EventID: decision.original.EventID, WasRegistered: decision.original.Status == db.RegistrationStatusREGISTERED,
	}); err != nil {
		return a.Registration{}, err
	}
	cancelled, _ := mapReg(row, nil)
	if row.CancelledAt.Valid {
		cancelled.CancelledAt = &row.CancelledAt.Time
	}
	if row.CancelledBy.Valid {
		by := string(row.CancelledBy.CancelActor)
		cancelled.CancelledBy = &by
	}
	if err = r.notify(ctx, cancelled, db.NotificationTypeREGCANCELLED); err != nil {
		return a.Registration{}, err
	}
	return cancelled, nil
}

func (r *repo) promoteWaitlistHead(ctx context.Context, eventID pgtype.UUID, decisionAt pgtype.Timestamptz) (*a.Registration, error) {
	head, err := r.q.FindWaitlistHead(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row, err := r.q.PromoteRegistration(ctx, db.PromoteRegistrationParams{ID: head.ID, DecisionAt: decisionAt})
	if err != nil {
		// A selected head must be promoted, not silently skipped.
		return nil, fmt.Errorf("promote selected waitlist head %s for event %s: %w", head.ID.String(), eventID.String(), err)
	}
	promoted, _ := mapReg(row, nil)
	if err = r.notify(ctx, promoted, db.NotificationTypePROMOTED); err != nil {
		return nil, err
	}
	n, err := r.q.IncrementPromotedCounters(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("increment promoted counters for event %s: %w", eventID.String(), err)
	}
	if n != 1 {
		return nil, fmt.Errorf("increment promoted counters for event %s: expected 1 row, got %d", eventID.String(), n)
	}
	return &promoted, nil
}
