package postgres

import (
	"context"
	"errors"
	"fmt"

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

func (r *repo) cancellationActor(ctx context.Context, original db.Registration, actor, unit pgtype.UUID, roles db.GetUserRolesRow) (db.CancelActor, error) {
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
	allowed, err := r.q.IsUnitOrganizer(ctx, db.IsUnitOrganizerParams{UserID: actor, UnitID: unit})
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", a.ErrNotFound
	}
	return db.CancelActorORGANIZER, nil
}

func (r *repo) Cancel(ctx context.Context, id, actor, reason string) (a.CancellationResult, error) {
	i, err := uuid(id)
	if err != nil {
		return a.CancellationResult{}, err
	}
	s, err := uuid(actor)
	if err != nil {
		return a.CancellationResult{}, err
	}
	roles, err := r.cancellationRoles(ctx, s)
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
	// Non-locking owner/scope preflight. Do not queue unauthorized callers on
	// the event row. Event/registration ownership is immutable in this API.
	var unit pgtype.UUID
	if original.StudentID != s && roles.IsOrganizer {
		event, err := r.q.GetEvent(ctx, original.EventID)
		if err != nil {
			return a.CancellationResult{}, err
		}
		unit = event.UnitID
	}
	by, err := r.cancellationActor(ctx, original, s, unit, roles)
	if err != nil {
		return a.CancellationResult{}, err
	}
	if by == db.CancelActorORGANIZER && reason == "" {
		return a.CancellationResult{}, a.ErrValidation
	}
	event, err := r.q.GetEventForRegistration(ctx, original.EventID)
	if err != nil {
		return a.CancellationResult{}, err
	}
	roles, err = r.cancellationRoles(ctx, s)
	if err != nil {
		return a.CancellationResult{}, err
	}
	original, err = r.q.GetRegistration(ctx, i)
	if err != nil {
		return a.CancellationResult{}, err
	}
	by, err = r.cancellationActor(ctx, original, s, event.UnitID, roles)
	if err != nil {
		return a.CancellationResult{}, err
	}
	// One fresh post-lock instant governs the entire cancellation and any
	// promotion; transaction-start now() may predate a long lock wait.
	decisionAt, err := r.q.GetCancellationDecisionTime(ctx)
	if err != nil {
		return a.CancellationResult{}, err
	}
	if original.Status == db.RegistrationStatusWAITLISTED && !decisionAt.Time.Before(event.StartAt.Time) {
		return a.CancellationResult{}, a.ErrState // Effectively EXPIRED, even before the worker persists it.
	}
	x, err := r.q.CancelRegistration(ctx, db.CancelRegistrationParams{
		ID: i, CancelledBy: db.NullCancelActor{CancelActor: by, Valid: true}, ActorID: s,
		Reason: pgtype.Text{String: reason, Valid: reason != ""}, DecisionAt: decisionAt,
	})
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
	if x.CancelledAt.Valid {
		cancelled.CancelledAt = &x.CancelledAt.Time
	}
	if x.CancelledBy.Valid {
		v := string(x.CancelledBy.CancelActor)
		cancelled.CancelledBy = &v
	}
	if err = r.notify(ctx, cancelled, db.NotificationTypeREGCANCELLED); err != nil {
		return a.CancellationResult{}, err
	}
	var promoted *a.Registration
	if original.Status == db.RegistrationStatusREGISTERED {
		if head, e := r.q.FindWaitlistHead(ctx, original.EventID); e == nil {
			p, e := r.q.PromoteRegistration(ctx, db.PromoteRegistrationParams{ID: head.ID, DecisionAt: decisionAt})
			if e != nil {
				return a.CancellationResult{}, fmt.Errorf("promote selected waitlist head %s for event %s: %w", head.ID.String(), original.EventID.String(), e)
			}
			v, e := mapReg(p.ID, p.EventID, p.StudentID, p.Status, p.QueueSeq, p.CheckinCode, p.CreatedAt, nil)
			if e != nil {
				return a.CancellationResult{}, e
			}
			if e = r.notify(ctx, v, db.NotificationTypePROMOTED); e != nil {
				return a.CancellationResult{}, e
			}
			promoted = &v
			n, e := r.q.IncrementPromotedCounters(ctx, original.EventID)
			if e != nil {
				return a.CancellationResult{}, fmt.Errorf("increment promoted counters for event %s: %w", original.EventID.String(), e)
			}
			if n != 1 {
				return a.CancellationResult{}, fmt.Errorf("increment promoted counters for event %s: expected 1 row, got %d", original.EventID.String(), n)
			}
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return a.CancellationResult{}, e
		}
	}
	if by == db.CancelActorORGANIZER {
		n, err := r.q.AuditRegistrationCancellation(ctx, i)
		if err != nil {
			return a.CancellationResult{}, fmt.Errorf("audit cancellation %s: %w", id, err)
		}
		if n != 1 {
			return a.CancellationResult{}, fmt.Errorf("audit cancellation %s: expected 1 row, got %d", id, n)
		}
	}
	return a.CancellationResult{Registration: cancelled, Promoted: promoted}, nil
}
func (r *repo) Mine(ctx context.Context, student, status string, offset, size int32) ([]a.Registration, int64, error) {
	s, err := uuid(student)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.ListMyRegistrations(ctx, db.ListMyRegistrationsParams{StudentID: s, Column2: status, Limit: size, Offset: offset})
	if err != nil {
		return nil, 0, err
	}
	n, err := r.q.CountMyRegistrations(ctx, db.CountMyRegistrationsParams{StudentID: s, Column2: status})
	out := make([]a.Registration, 0, len(rows))
	for _, x := range rows {
		v, _ := mapReg(x.ID, x.EventID, x.StudentID, x.Status, x.QueueSeq, x.CheckinCode, x.CreatedAt, nil)
		out = append(out, v)
	}
	return out, n, err
}
