package application

import (
	"errors"
	"testing"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type editClock struct{ now time.Time }

func (c editClock) Now() time.Time { return c.now }

// These cases catch using the new start for cutoffs, changing inclusive time
// boundaries, or treating non-significant edits as schedule changes.
func TestPrepareEventEditSignificance(t *testing.T) {
	start := time.Date(2030, 1, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		status      string
		beforeStart time.Duration
		change      string
		significant bool
		exception   bool
		want        error
	}{
		{"outside 24 hours", "PUBLISHED", 24*time.Hour + time.Nanosecond, "start", true, false, nil},
		{"exactly 24 hours", "PUBLISHED", 24 * time.Hour, "start", true, true, nil},
		{"before location freeze", "PUBLISHED", time.Hour + time.Nanosecond, "location", true, true, nil},
		{"exactly location freeze", "PUBLISHED", time.Hour, "location", false, false, domain.ErrState},
		{"after location freeze", "PUBLISHED", time.Hour - time.Nanosecond, "end", false, false, domain.ErrState},
		{"title during freeze", "PUBLISHED", 30 * time.Minute, "title", false, false, nil},
		{"unchanged schedule", "PUBLISHED", 30 * time.Minute, "none", false, false, nil},
		{"draft schedule", "DRAFT", 30 * time.Minute, "start", false, false, nil},
		{"rejected schedule", "REJECTED", 30 * time.Minute, "start", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := editableEvent(start)
			current.Status = tc.status
			s := New(nil, editClock{start.Add(-tc.beforeStart)})
			out, err := s.prepareEventEdit(current, Actor{ID: "organizer"}, 2, func(f domain.Fields) (domain.Fields, error) {
				switch tc.change {
				case "start":
					f.StartAt = f.StartAt.Add(30 * time.Minute)
				case "end":
					f.EndAt = f.EndAt.Add(time.Hour)
				case "location":
					f.Location = "New hall"
				case "title":
					f.Title = "New title"
				}
				return f, nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err == nil && (out.SignificantChange != tc.significant || out.GrantCancellationException != tc.exception || out.ChangedBy != "organizer") {
				t.Fatalf("unexpected edit effects: %+v", out)
			}
		})
	}
}

// Failed preconditions must not reach the merge callback, and each later
// validation must retain its existing error precedence.
func TestPrepareEventEditValidation(t *testing.T) {
	start := time.Date(2030, 1, 3, 12, 0, 0, 0, time.UTC)
	mergeErr := errors.New("invalid patch")
	for _, tc := range []struct {
		name    string
		status  string
		version int32
		change  string
		want    error
	}{
		{"version before state", "CANCELLED", 1, "must not merge", domain.ErrVersion},
		{"pending", "PENDING_APPROVAL", 2, "must not merge", domain.ErrState},
		{"cancelled", "CANCELLED", 2, "must not merge", domain.ErrState},
		{"completed", "COMPLETED", 2, "must not merge", domain.ErrState},
		{"merge error", "DRAFT", 2, "merge error", mergeErr},
		{"capacity before other fields", "DRAFT", 2, "capacity", domain.ErrCapacity},
		{"waitlist below existing count", "DRAFT", 2, "waitlist", domain.ErrValidation},
		{"invalid fields", "DRAFT", 2, "title", domain.ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := editableEvent(start)
			current.Status = tc.status
			s := New(nil, editClock{start.Add(-48 * time.Hour)})
			_, err := s.prepareEventEdit(current, Actor{ID: "organizer"}, tc.version, func(f domain.Fields) (domain.Fields, error) {
				switch tc.change {
				case "must not merge":
					t.Fatal("precondition failure reached merge")
				case "merge error":
					return f, mergeErr
				case "capacity":
					f.Capacity, f.Title = 1, ""
				case "waitlist":
					limit := int32(0)
					f.WaitlistLimit = &limit
				case "title":
					f.Title = ""
				}
				return f, nil
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func editableEvent(start time.Time) domain.Event {
	return domain.Event{
		ID: "event", UnitID: "unit", CreatedBy: "owner", Status: "PUBLISHED", Version: 2,
		RegisteredCount: 2, WaitlistCount: 1,
		Fields: domain.Fields{
			Title: "Workshop", Location: "Hall", Capacity: 10,
			StartAt: start, EndAt: start.Add(time.Hour),
			RegistrationOpensAt: start.Add(-72 * time.Hour), RegistrationClosesAt: start.Add(-time.Hour),
		},
	}
}
