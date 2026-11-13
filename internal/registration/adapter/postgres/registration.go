package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

func (r *repo) Register(ctx context.Context, event, student string) (a.Registration, error) {
	eventID, err := uuid(event)
	if err != nil {
		return a.Registration{}, err
	}
	studentID, err := uuid(student)
	if err != nil {
		return a.Registration{}, err
	}

	// Active has already locked the event. Insert the seat/waitlist entry and
	// update its counter together; code collisions retry inside savepoints.
	row, err := r.insertRegistration(ctx, db.RegisterStudentParams{ID: eventID, StudentID: studentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return a.Registration{}, r.registrationUnavailable(ctx, eventID)
	}
	registration, err := mapReg(row.ID, row.EventID, row.StudentID, row.Status, row.QueueSeq, row.CheckinCode, row.CreatedAt, err)
	if err != nil {
		return a.Registration{}, err
	}
	if err = r.notify(ctx, registration, db.NotificationType(registration.Status)); err != nil {
		return a.Registration{}, err
	}
	if registration.Status == "WAITLISTED" {
		position, err := r.q.GetWaitlistPosition(ctx, row.ID)
		if err != nil {
			return a.Registration{}, err
		}
		if position >= 1 {
			registration.WaitlistPosition = &position
		}
	}
	return registration, nil
}

// A guarded insert returns no row for a closed window or an exhausted waitlist.
// Keep the same transaction-start clock semantics as the insert SQL.
func (r *repo) registrationUnavailable(ctx context.Context, eventID pgtype.UUID) error {
	event, err := r.q.GetEventForRegistration(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return a.ErrNotFound
	}
	if err != nil {
		return err
	}
	open, err := r.q.IsRegistrationWindowOpen(ctx, eventID)
	if err != nil {
		return err
	}
	if event.Status != db.EventStatusPUBLISHED || !open.Valid || !open.Bool {
		return a.ErrClosed
	}
	return a.ErrFull
}

func (r *repo) Active(ctx context.Context, event, student string) (a.Registration, error) {
	eventID, err := uuid(event)
	if err != nil {
		return a.Registration{}, err
	}
	// Lock before checking duplicates: every registration for this event serializes
	// here, and subsequent statements receive a fresh READ COMMITTED snapshot.
	if _, err := r.q.GetEventForRegistration(ctx, eventID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a.Registration{}, a.ErrNotFound
		}
		return a.Registration{}, err
	}
	studentID, err := uuid(student)
	if err != nil {
		return a.Registration{}, err
	}
	row, err := r.q.GetActiveRegistration(ctx, db.GetActiveRegistrationParams{EventID: eventID, StudentID: studentID})
	return mapReg(row.ID, row.EventID, row.StudentID, row.Status, row.QueueSeq, row.CheckinCode, row.CreatedAt, err)
}
