package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type Reader struct {
	q     *db.Queries
	clock kernel.Clock
}

func NewReader(pool *pgxpool.Pool, clock kernel.Clock) *Reader { return &Reader{db.New(pool), clock} }
func (r *Reader) Units(ctx context.Context, kind string) ([]map[string]any, error) {
	rows, err := r.q.ListActiveUnits(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, u := range rows {
		out = append(out, map[string]any{"id": u.ID.String(), "code": u.Code, "name": u.Name, "type": u.Type, "isActive": u.IsActive})
	}
	return out, nil
}
func (r *Reader) render(ctx context.Context, row db.Event) (map[string]any, error) {
	unit, err := r.q.GetEventUnit(ctx, row.UnitID)
	if err != nil {
		return nil, err
	}
	creator, err := r.q.GetEventCreator(ctx, row.CreatedBy)
	if err != nil {
		return nil, err
	}
	var description any
	var limit any
	if row.Description.Valid {
		description = row.Description.String
	}
	if row.WaitlistLimit.Valid {
		limit = row.WaitlistLimit.Int32
	}
	now := r.clock.Now()
	waitlist := row.WaitlistCount
	if !now.Before(row.StartAt.Time) {
		waitlist = 0
	}
	return map[string]any{"id": row.ID.String(), "unit": map[string]any{"id": unit.ID.String(), "code": unit.Code, "name": unit.Name}, "createdBy": map[string]any{"id": creator.ID.String(), "fullName": creator.FullName}, "title": row.Title, "description": description, "location": row.Location, "startAt": row.StartAt.Time, "endAt": row.EndAt.Time, "registrationOpensAt": row.RegistrationOpensAt.Time, "registrationClosesAt": row.RegistrationClosesAt.Time, "capacity": row.Capacity, "waitlistLimit": limit, "registeredCount": row.RegisteredCount, "waitlistCount": waitlist, "availableSeats": row.Capacity - row.RegisteredCount, "isRegistrationOpen": row.Status == db.EventStatusPUBLISHED && !now.Before(row.RegistrationOpensAt.Time) && now.Before(row.RegistrationClosesAt.Time), "cancelDeadline": row.StartAt.Time.Add(-24 * time.Hour), "status": row.Status, "version": row.Version}, nil
}
func (r *Reader) Detail(ctx context.Context, raw string) (map[string]any, error) {
	u, err := id(raw)
	if err != nil {
		return nil, err
	}
	row, err := r.q.GetEvent(ctx, u)
	if err != nil {
		_, mapped := event(row, err)
		return nil, mapped
	}
	return r.render(ctx, row)
}
func (r *Reader) Managed(ctx context.Context, actor application.Actor, f application.Filter) ([]map[string]any, int64, error) {
	uid, err := id(actor.ID)
	if err != nil {
		return nil, 0, err
	}
	count, err := r.q.CountManagedEvents(ctx, db.CountManagedEventsParams{IsAdmin: actor.IsAdmin, ActorID: uid, UnitFilter: f.UnitID, StatusFilter: f.Status, Search: f.Search})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListManagedEvents(ctx, db.ListManagedEventsParams{IsAdmin: actor.IsAdmin, ActorID: uid, UnitFilter: f.UnitID, StatusFilter: f.Status, Search: f.Search, PageSize: f.PageSize, PageOffset: (f.Page - 1) * f.PageSize})
	if err != nil {
		return nil, 0, err
	}
	return r.renderPage(ctx, rows, count)
}

func (r *Reader) Catalog(ctx context.Context, f application.CatalogFilter) ([]map[string]any, int64, error) {
	timestamp := func(t *time.Time) pgtype.Timestamptz {
		if t == nil {
			return pgtype.Timestamptz{}
		}
		return pgtype.Timestamptz{Time: *t, Valid: true}
	}
	params := db.CountPublishedEventsParams{UnitFilter: f.UnitID, Search: f.Search, FromAt: timestamp(f.From), ToAt: timestamp(f.To), AvailableOnly: f.AvailableOnly, NowAt: pgtype.Timestamptz{Time: r.clock.Now(), Valid: true}}
	count, err := r.q.CountPublishedEvents(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListPublishedEvents(ctx, db.ListPublishedEventsParams{UnitFilter: params.UnitFilter, Search: params.Search, FromAt: params.FromAt, ToAt: params.ToAt, AvailableOnly: params.AvailableOnly, NowAt: params.NowAt, PageSize: f.PageSize, PageOffset: (f.Page - 1) * f.PageSize})
	if err != nil {
		return nil, 0, err
	}
	return r.renderPage(ctx, rows, count)
}

func (r *Reader) renderPage(ctx context.Context, rows []db.Event, count int64) ([]map[string]any, int64, error) {
	out := []map[string]any{}
	for _, row := range rows {
		item, err := r.render(ctx, row)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, item)
	}
	return out, count, nil
}
