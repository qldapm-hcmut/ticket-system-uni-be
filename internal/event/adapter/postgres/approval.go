package postgres

import (
	"context"
	"encoding/json"
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
		metadata["reason"] = reason
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
func (r *repository) Approve(ctx context.Context, raw string, v int32, admin string) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	a, err := id(admin)
	if err != nil {
		return domain.Event{}, err
	}
	return event(r.q.ApproveEvent(ctx, db.ApproveEventParams{ID: u, ApprovedBy: a, Version: v}))
}
func (r *repository) Reject(ctx context.Context, raw string, v int32, reason string) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	return event(r.q.RejectEvent(ctx, db.RejectEventParams{ID: u, RejectReason: text(&reason), Version: v}))
}
func (r *repository) Pending(ctx context.Context, page, size int32) ([]domain.Event, int64, error) {
	rows, err := r.q.ListPendingEvents(ctx, db.ListPendingEventsParams{Limit: size, Offset: (page - 1) * size})
	if err != nil {
		return nil, 0, err
	}
	count, err := r.q.CountPendingEvents(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		e, err := event(row, nil)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, count, nil
}
