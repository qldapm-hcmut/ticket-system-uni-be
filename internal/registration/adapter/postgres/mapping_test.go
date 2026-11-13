package postgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

// Protect the boundary conversion from swapped IDs, dropped queue/code/time,
// accidental cancellation metadata, and lost missing/infrastructure errors.
func TestRegistrationMapping(t *testing.T) {
	var id, event, student pgtype.UUID
	_ = id.Scan("00000000-0000-0000-0000-000000000001")
	_ = event.Scan("00000000-0000-0000-0000-000000000002")
	_ = student.Scan("00000000-0000-0000-0000-000000000003")
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := db.Registration{ID: id, EventID: event, StudentID: student, Status: db.RegistrationStatusWAITLISTED,
		QueueSeq: pgtype.Int8{Int64: 42, Valid: true}, CheckinCode: "ABCD2345",
		CreatedAt: pgtype.Timestamptz{Time: at, Valid: true}, CancelledAt: pgtype.Timestamptz{Time: at, Valid: true},
		CancelledBy: db.NullCancelActor{CancelActor: db.CancelActorSTUDENT, Valid: true}}
	failure := errors.New("database unavailable")
	for _, queryErr := range []error{nil, pgx.ErrNoRows, failure} {
		got, err := mapReg(row, queryErr)
		if queryErr != nil {
			want := queryErr
			if queryErr == pgx.ErrNoRows {
				want = a.ErrNotFound
			}
			if !errors.Is(err, want) || !reflect.DeepEqual(got, a.Registration{}) {
				t.Fatalf("got %+v, %v; want zero, %v", got, err, want)
			}
			continue
		}
		want := a.Registration{ID: "00000000-0000-0000-0000-000000000001", EventID: "00000000-0000-0000-0000-000000000002",
			StudentID: "00000000-0000-0000-0000-000000000003", Status: "WAITLISTED", QueueSeq: 42, CheckinCode: "ABCD2345", CreatedAt: at}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, %v; want %+v", got, err, want)
		}
	}
}
