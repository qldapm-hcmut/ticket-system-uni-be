package postgres

import (
	"context"
	"errors"

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
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{runner: platform.NewTransactionRunner(pool), q: db.New(pool)}
}
func (s *Store) Do(ctx context.Context, fn func(context.Context, a.RegistrationRepository) error) error {
	return s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error { return translate(fn(ctx, &repo{s.q.WithTx(tx)})) })
}

type repo struct{ q *db.Queries }

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
func (r *repo) Register(ctx context.Context, event, student string) (a.Registration, error) {
	e, err := uuid(event)
	if err != nil {
		return a.Registration{}, err
	}
	s, err := uuid(student)
	if err != nil {
		return a.Registration{}, err
	}
	x, err := r.q.RegisterStudent(ctx, db.RegisterStudentParams{ID: e, StudentID: s})
	if errors.Is(err, pgx.ErrNoRows) {
		// Active has already acquired the event lock in this transaction.
		// Distinguish a closed registration window from an exhausted waitlist.
		state, readErr := r.q.GetEventForRegistration(ctx, e)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return a.Registration{}, a.ErrNotFound
		}
		if readErr != nil {
			return a.Registration{}, readErr
		}
		open, readErr := r.q.IsRegistrationWindowOpen(ctx, e)
		if readErr != nil {
			return a.Registration{}, readErr
		}
		if state.Status != db.EventStatusPUBLISHED || !open.Valid || !open.Bool {
			return a.Registration{}, a.ErrClosed
		}
		return a.Registration{}, a.ErrFull
	}
	reg, err := mapReg(x.ID, x.EventID, x.StudentID, x.Status, x.QueueSeq, x.CheckinCode, x.CreatedAt, err)
	if err != nil {
		return a.Registration{}, err
	}
	if err = r.notify(ctx, reg, db.NotificationType(reg.Status)); err != nil {
		return a.Registration{}, err
	}
	return reg, nil
}
func (r *repo) Active(ctx context.Context, event, student string) (a.Registration, error) {
	e, err := uuid(event)
	if err != nil {
		return a.Registration{}, err
	}
	// Lock before checking duplicates: every registration for this event serializes
	// here, and subsequent statements receive a fresh READ COMMITTED snapshot.
	if _, err := r.q.GetEventForRegistration(ctx, e); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a.Registration{}, a.ErrNotFound
		}
		return a.Registration{}, err
	}
	s, err := uuid(student)
	if err != nil {
		return a.Registration{}, err
	}
	x, err := r.q.GetActiveRegistration(ctx, db.GetActiveRegistrationParams{EventID: e, StudentID: s})
	return mapReg(x.ID, x.EventID, x.StudentID, x.Status, x.QueueSeq, x.CheckinCode, x.CreatedAt, err)
}
func (r *repo) Cancel(ctx context.Context, id, student string) (a.CancellationResult, error) {
	i, err := uuid(id)
	if err != nil {
		return a.CancellationResult{}, err
	}
	s, err := uuid(student)
	if err != nil {
		return a.CancellationResult{}, err
	}
	original, err := r.q.GetRegistration(ctx, i)
	if errors.Is(err, pgx.ErrNoRows) {
		return a.CancellationResult{}, a.ErrNotFound
	}
	if err != nil {
		return a.CancellationResult{}, err
	}
	if original.StudentID != s {
		return a.CancellationResult{}, a.ErrNotFound
	}
	if _, err = r.q.GetEventForRegistration(ctx, original.EventID); err != nil {
		return a.CancellationResult{}, err
	}
	original, err = r.q.GetRegistration(ctx, i)
	if err != nil {
		return a.CancellationResult{}, err
	}
	x, err := r.q.CancelRegistration(ctx, db.CancelRegistrationParams{ID: i, CancelledBy: db.NullCancelActor{CancelActor: db.CancelActorSTUDENT, Valid: true}, CancelledByUser: s})
	if errors.Is(err, pgx.ErrNoRows) {
		if original.Status != db.RegistrationStatusREGISTERED && original.Status != db.RegistrationStatusWAITLISTED {
			return a.CancellationResult{}, a.ErrState
		}
		return a.CancellationResult{}, a.ErrCancelDeadline
	}
	if err != nil {
		return a.CancellationResult{}, err
	}
	if err = r.q.DecrementRegistrationCounter(ctx, db.DecrementRegistrationCounterParams{EventID: original.EventID, WasRegistered: original.Status == db.RegistrationStatusREGISTERED}); err != nil {
		return a.CancellationResult{}, err
	}
	cancelled, err := mapReg(x.ID, x.EventID, x.StudentID, x.Status, x.QueueSeq, x.CheckinCode, x.CreatedAt, nil)
	if err != nil {
		return a.CancellationResult{}, err
	}
	if err = r.notify(ctx, cancelled, db.NotificationTypeREGCANCELLED); err != nil {
		return a.CancellationResult{}, err
	}
	var promoted *a.Registration
	if original.Status == db.RegistrationStatusREGISTERED {
		if head, e := r.q.FindWaitlistHead(ctx, original.EventID); e == nil {
			p, e := r.q.PromoteRegistration(ctx, head.ID)
			if e != nil {
				return a.CancellationResult{}, e
			}
			v, e := mapReg(p.ID, p.EventID, p.StudentID, p.Status, p.QueueSeq, p.CheckinCode, p.CreatedAt, nil)
			if e != nil {
				return a.CancellationResult{}, e
			}
			if e = r.notify(ctx, v, db.NotificationTypePROMOTED); e != nil {
				return a.CancellationResult{}, e
			}
			promoted = &v
			if e = r.q.IncrementPromotedCounters(ctx, original.EventID); e != nil {
				return a.CancellationResult{}, e
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return a.CancellationResult{}, e
		}
	}
	return a.CancellationResult{Registration: cancelled, Promoted: promoted}, nil
}
func (r *repo) Mine(ctx context.Context, student, status string, upcoming bool, offset, size int32) ([]a.Registration, int64, error) {
	s, err := uuid(student)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListMyRegistrations(ctx, db.ListMyRegistrationsParams{
		StudentID: s, StatusFilter: status, Upcoming: upcoming, PageSize: size, PageOffset: offset,
	})
	if err != nil {
		return nil, 0, err
	}
	n, err := r.q.CountMyRegistrations(ctx, db.CountMyRegistrationsParams{StudentID: s, StatusFilter: status, Upcoming: upcoming})
	if err != nil {
		return nil, 0, err
	}
	out := make([]a.Registration, 0, len(rows))
	for _, x := range rows {
		reg := x.Registration
		v, _ := mapReg(reg.ID, reg.EventID, reg.StudentID, reg.Status, reg.QueueSeq, reg.CheckinCode, reg.CreatedAt, nil)
		v.Event = &a.EventSummary{
			ID: v.EventID, Title: x.Title, Location: x.Location,
			StartAt: x.StartAt.Time, EndAt: x.EndAt.Time, Status: string(x.EventStatus),
		}
		if reg.Status == db.RegistrationStatusWAITLISTED {
			v.WaitlistPosition = &x.WaitlistPosition
		}
		out = append(out, v)
	}
	return out, n, nil
}
