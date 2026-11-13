package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
)

type Store struct {
	runner *platform.TransactionRunner
	q      *db.Queries
}

func New(pool *pgxpool.Pool) *Store { return &Store{platform.NewTransactionRunner(pool), db.New(pool)} }
func (s *Store) Do(ctx context.Context, fn func(context.Context, application.EventRepository) error) error {
	return s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, &repository{s.q.WithTx(tx)}) })
}

type repository struct{ q *db.Queries }

func id(raw string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(raw); err != nil {
		return u, domain.ErrValidation
	}
	return u, nil
}
func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func text(p *string) pgtype.Text {
	if p == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *p, Valid: true}
}
func integer(p *int32) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *p, Valid: true}
}
func event(row db.Event, err error) (domain.Event, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Event{}, err
	}
	var description *string
	var limit *int32
	if row.Description.Valid {
		description = &row.Description.String
	}
	if row.WaitlistLimit.Valid {
		limit = &row.WaitlistLimit.Int32
	}
	return domain.Event{
		ID:              row.ID.String(),
		UnitID:          row.UnitID.String(),
		CreatedBy:       row.CreatedBy.String(),
		Status:          string(row.Status),
		Version:         row.Version,
		RegisteredCount: row.RegisteredCount,
		WaitlistCount:   row.WaitlistCount,
		Fields: domain.Fields{
			Title:                row.Title,
			Description:          description,
			Location:             row.Location,
			StartAt:              row.StartAt.Time,
			EndAt:                row.EndAt.Time,
			RegistrationOpensAt:  row.RegistrationOpensAt.Time,
			RegistrationClosesAt: row.RegistrationClosesAt.Time,
			Capacity:             row.Capacity,
			WaitlistLimit:        limit,
		},
	}, nil
}
func (r *repository) IsOrganizer(ctx context.Context, user, unit string) (bool, error) {
	u, err := id(user)
	if err != nil {
		return false, err
	}
	v, err := id(unit)
	if err != nil {
		return false, err
	}
	return r.q.IsUnitOrganizer(ctx, db.IsUnitOrganizerParams{UserID: u, UnitID: v})
}
func (r *repository) Get(ctx context.Context, raw string, lock bool) (domain.Event, error) {
	u, err := id(raw)
	if err != nil {
		return domain.Event{}, err
	}
	if lock {
		return event(r.q.GetEventForUpdate(ctx, u))
	}
	return event(r.q.GetEvent(ctx, u))
}
func (r *repository) Create(ctx context.Context, unit, user string, f domain.Fields) (domain.Event, error) {
	unitID, err := id(unit)
	if err != nil {
		return domain.Event{}, err
	}
	creatorID, err := id(user)
	if err != nil {
		return domain.Event{}, err
	}
	return event(r.q.CreateDraftEvent(ctx, db.CreateDraftEventParams{
		UnitID:               unitID,
		CreatedBy:            creatorID,
		Title:                f.Title,
		Description:          text(f.Description),
		Location:             f.Location,
		StartAt:              stamp(f.StartAt),
		EndAt:                stamp(f.EndAt),
		RegistrationOpensAt:  stamp(f.RegistrationOpensAt),
		RegistrationClosesAt: stamp(f.RegistrationClosesAt),
		Capacity:             f.Capacity,
		WaitlistLimit:        integer(f.WaitlistLimit),
	}))
}

// Update persists the edit and its registration effects through the same
// transaction-bound queries. Any error rolls back the entire edit.
func (r *repository) Update(ctx context.Context, e domain.Event) (domain.Event, error) {
	u, err := id(e.ID)
	if err != nil {
		return domain.Event{}, err
	}
	f := e.Fields
	old, err := r.q.GetEventForUpdate(ctx, u)
	if err != nil {
		return event(db.Event{}, err)
	}
	updated, err := event(r.q.UpdateDraftEvent(ctx, db.UpdateDraftEventParams{
		ID:                   u,
		Title:                f.Title,
		Description:          text(f.Description),
		Location:             f.Location,
		StartAt:              stamp(f.StartAt),
		EndAt:                stamp(f.EndAt),
		RegistrationOpensAt:  stamp(f.RegistrationOpensAt),
		RegistrationClosesAt: stamp(f.RegistrationClosesAt),
		Capacity:             f.Capacity,
		WaitlistLimit:        integer(f.WaitlistLimit),
		Version:              e.Version,
	}))
	if err != nil {
		return domain.Event{}, err
	}

	if e.SignificantChange {
		updated.NotifiedCount, err = r.applySignificantEventChange(ctx, u, old, e)
		if err != nil {
			return domain.Event{}, err
		}
		updated.SignificantChange = true
	}

	// The SQL promotes FIFO waitlist entries and writes their notifications.
	if old.Status == db.EventStatusPUBLISHED && f.Capacity > old.Capacity {
		updated.PromotedCount, err = r.q.PromoteWaitlistForCapacity(ctx, db.PromoteWaitlistForCapacityParams{EventID: u, Seats: f.Capacity - old.Capacity})
		if err != nil {
			return domain.Event{}, err
		}
		updated.RegisteredCount += updated.PromotedCount
		updated.WaitlistCount -= updated.PromotedCount
	}
	return updated, nil
}
