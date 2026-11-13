package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

type Store struct {
	runner *platform.TransactionRunner
	q      *db.Queries
	random io.Reader
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{runner: platform.NewTransactionRunner(pool), q: db.New(pool), random: rand.Reader}
}
func (s *Store) Do(ctx context.Context, fn func(context.Context, a.RegistrationRepository) error) error {
	return s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return translate(fn(ctx, &repo{q: s.q.WithTx(tx), tx: tx, random: s.random}))
	})
}

type repo struct {
	q      *db.Queries
	tx     pgx.Tx
	random io.Reader
}

func translate(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) {
		if p.Code == "23505" && p.ConstraintName == "uq_active_registration" {
			return a.ErrAlreadyActive
		}
		if p.Code == "22P02" || p.Code == "22003" {
			return a.ErrValidation
		}
	}
	return err
}
func uuid(v string) (pgtype.UUID, error) {
	var x pgtype.UUID
	if err := x.Scan(v); err != nil {
		return x, a.ErrValidation
	}
	return x, nil
}
func (r *repo) notify(ctx context.Context, reg a.Registration, kind db.NotificationType) error {
	i, err := uuid(reg.ID)
	if err != nil {
		return err
	}
	return r.q.NotifyRegistration(ctx, db.NotifyRegistrationParams{RegistrationID: i, Kind: kind, Title: string(kind), Body: "Your event registration status has changed."})
}
func mapReg(id, eventID, studentID pgtype.UUID, status db.RegistrationStatus, queue pgtype.Int8, code string, created pgtype.Timestamptz, e error) (a.Registration, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return a.Registration{}, a.ErrNotFound
	}
	if e != nil {
		return a.Registration{}, e
	}
	return a.Registration{ID: id.String(), EventID: eventID.String(), StudentID: studentID.String(), Status: string(status), QueueSeq: queue.Int64, CheckinCode: code, CreatedAt: created.Time}, nil
}
func (r *repo) Mine(ctx context.Context, student string, f a.MineFilter) ([]a.MyRegistration, int64, error) {
	// Must be the first repository operation in a fresh Store.Do transaction:
	// SET TRANSACTION cannot change isolation after any query. Service.Mine
	// deliberately calls only this method. Count, status, brief and rank share
	// the snapshot established by the following single DB clock read.
	if _, err := r.tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return nil, 0, err
	}
	s, err := uuid(student)
	if err != nil {
		return nil, 0, err
	}
	decision, err := r.q.GetCancellationDecisionTime(ctx)
	if err != nil {
		return nil, 0, err
	}
	n, err := r.q.CountMyRegistrations(ctx, db.CountMyRegistrationsParams{StudentID: s, StatusFilter: f.Status, Upcoming: f.Upcoming, DecisionAt: decision})
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListMyRegistrations(ctx, db.ListMyRegistrationsParams{StudentID: s, StatusFilter: f.Status, Upcoming: f.Upcoming, DecisionAt: decision, PageSize: f.PageSize, PageOffset: (f.Page - 1) * f.PageSize})
	if err != nil {
		return nil, 0, err
	}
	out := make([]a.MyRegistration, 0, len(rows))
	for _, row := range rows {
		x := row.Registration
		v, _ := mapReg(x.ID, x.EventID, x.StudentID, db.RegistrationStatus(row.EffectiveStatus), x.QueueSeq, x.CheckinCode, x.CreatedAt, nil)
		if v.Status == "WAITLISTED" && row.WaitlistPosition >= 1 {
			position := row.WaitlistPosition
			v.WaitlistPosition = &position
		}
		if x.CancelledAt.Valid {
			v.CancelledAt = &x.CancelledAt.Time
		}
		if x.CancelledBy.Valid {
			by := string(x.CancelledBy.CancelActor)
			v.CancelledBy = &by
		}
		out = append(out, a.MyRegistration{Registration: v, Event: a.EventSummary{ID: x.EventID.String(), Title: row.EventTitle, Location: row.EventLocation, StartAt: row.EventStartAt.Time, EndAt: row.EventEndAt.Time, Status: string(row.EventStatus)}})
	}
	return out, n, nil
}
