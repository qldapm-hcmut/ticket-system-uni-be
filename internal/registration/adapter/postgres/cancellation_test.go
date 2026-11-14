package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

func TestCancellationActorOwnerPrecedence(t *testing.T) {
	owner, _ := uuid("00000000-0000-0000-0000-000000000001")
	other, _ := uuid("00000000-0000-0000-0000-000000000002")
	for _, tc := range []struct {
		name  string
		actor pgtype.UUID
		roles db.GetUserRolesRow
		want  db.CancelActor
		err   error
	}{
		{"student owner", owner, db.GetUserRolesRow{IsStudent: true}, db.CancelActorSTUDENT, nil},
		{"organizer student owner", owner, db.GetUserRolesRow{IsStudent: true, IsOrganizer: true}, db.CancelActorSTUDENT, nil},
		{"admin organizer student owner", owner, db.GetUserRolesRow{IsStudent: true, IsOrganizer: true, IsAdmin: true}, db.CancelActorSTUDENT, nil},
		{"owner without Student", owner, db.GetUserRolesRow{}, "", a.ErrForbidden},
		{"organizer owner without Student", owner, db.GetUserRolesRow{IsOrganizer: true}, "", a.ErrForbidden},
		{"admin organizer owner without Student", owner, db.GetUserRolesRow{IsOrganizer: true, IsAdmin: true}, "", a.ErrForbidden},
		{"student not owner", other, db.GetUserRolesRow{IsStudent: true}, "", a.ErrNotFound},
		{"admin student not owner", other, db.GetUserRolesRow{IsStudent: true, IsAdmin: true}, "", a.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// These decisions must not query organizer scope: ownership wins,
			// and Admin alone cannot turn a non-owner into an Organizer.
			r := &repo{}
			for _, event := range []*db.Event{nil, {}} {
				got, err := r.cancellationActor(context.Background(), db.Registration{StudentID: owner}, tc.actor, event, tc.roles)
				if got != tc.want || !errors.Is(err, tc.err) {
					t.Fatalf("actor=%q err=%v; want actor=%q err=%v", got, err, tc.want, tc.err)
				}
			}
		})
	}
}
