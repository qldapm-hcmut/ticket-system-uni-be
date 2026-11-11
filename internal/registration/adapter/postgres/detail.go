package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

// OwnedRegistration reads the student's own registration in a single query;
// another student's registration is indistinguishable from a missing one.
func (s *Store) OwnedRegistration(ctx context.Context, student, registration string) (a.RegistrationView, error) {
	rid, err := uuid(registration)
	if err != nil {
		return a.RegistrationView{}, err
	}
	sid, err := uuid(student)
	if err != nil {
		return a.RegistrationView{}, err
	}
	row, err := s.q.GetOwnedRegistrationView(ctx, db.GetOwnedRegistrationViewParams{RegistrationID: rid, StudentID: sid})
	if errors.Is(err, pgx.ErrNoRows) {
		return a.RegistrationView{}, a.ErrNotFound
	}
	if err != nil {
		return a.RegistrationView{}, err
	}
	v := a.RegistrationView{ID: row.ID.String(), EventID: row.EventID.String(), Status: string(row.Status),
		CheckinCode: row.CheckinCode, CreatedAt: row.CreatedAt.Time,
		Event: a.EventSummary{ID: row.EventID.String(), Title: row.EventTitle, Location: row.EventLocation,
			StartAt: row.EventStartAt.Time, EndAt: row.EventEndAt.Time, Status: string(row.EventStatus)}}
	if row.Status == "WAITLISTED" && row.WaitlistPosition >= 1 {
		v.WaitlistPosition = &row.WaitlistPosition
	}
	if row.CheckedInAt.Valid {
		v.CheckIn = &a.CheckinSummary{CheckedInAt: row.CheckedInAt.Time, Method: string(row.CheckinMethod.CheckinMethod)}
	}
	return v, nil
}
