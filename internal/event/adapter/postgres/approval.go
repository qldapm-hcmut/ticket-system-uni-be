package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

func (s *Store) DoApproval(ctx context.Context, fn func(application.ApprovalRepository) error) error {
	return s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error { return fn(&repository{q: s.q.WithTx(tx)}) })
}

func (r *repository) Audit(ctx context.Context, actor, eventID, action, reason string) error {
	aid, err := id(actor)
	if err != nil {
		return err
	}
	eid, err := id(eventID)
	if err != nil {
		return err
	}
	metadata := map[string]any{}
	if reason != "" {
		// P5 historically reads reason as its public comment. Preserve that
		// representation while recording the explicit optional approval comment.
		metadata["reason"] = reason
		if action == "APPROVE" {
			metadata["comment"] = reason
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return r.q.AuditApprovalAction(ctx, db.AuditApprovalActionParams{ActorID: aid, EventID: eid, Action: action, Metadata: raw})
}

func (r *repository) Submit(ctx context.Context, raw string, v int32) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	return event(r.q.SubmitEvent(ctx, db.SubmitEventParams{ID: u, Version: v}))
}
func (r *repository) Approve(ctx context.Context, raw string, v int32, admin string, now time.Time) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	a, err := id(admin)
	if err != nil {
		return domain.Event{}, err
	}
	row, err := r.q.ApproveEvent(ctx, db.ApproveEventParams{ID: u, ApprovedBy: a, Version: v, NowAt: stamp(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, domain.ErrApprovalStart
	}
	return event(row, err)
}
func (r *repository) Reject(ctx context.Context, raw string, v int32, reason string) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	return event(r.q.RejectEvent(ctx, db.RejectEventParams{ID: u, RejectReason: text(&reason), Version: v}))
}
func (r *repository) NotifyDecision(ctx context.Context, raw string, approved bool, body string) error {
	eid, err := id(raw)
	if err != nil {
		return err
	}
	kind := db.NotificationTypeEVENTREJECTED
	if approved {
		kind = db.NotificationTypeEVENTAPPROVED
	}
	return r.q.NotifyApprovalDecision(ctx, db.NotifyApprovalDecisionParams{EventID: eid, NotificationType: kind, Body: body})
}
