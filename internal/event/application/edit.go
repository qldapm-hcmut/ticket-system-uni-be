package application

import (
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

// prepareEventEdit runs after the event is locked and organizer scope is checked.
// Cutoffs are based on the old start, even when the patch moves the event later.
func (s *Service) prepareEventEdit(current domain.Event, actor Actor, version int32, merge func(domain.Fields) (domain.Fields, error)) (domain.Event, error) {
	if current.Version != version {
		return domain.Event{}, domain.ErrVersion
	}
	switch current.Status {
	case "DRAFT", "REJECTED", "PUBLISHED":
		// Drafts and rejected events are editable; published edits have cutoffs below.
	default:
		return domain.Event{}, domain.ErrState
	}

	previous := current.Fields
	edited := current
	var err error
	edited.Fields, err = merge(previous)
	if err != nil {
		return domain.Event{}, err
	}

	scheduleOrLocationChanged := !previous.StartAt.Equal(edited.StartAt) ||
		!previous.EndAt.Equal(edited.EndAt) || previous.Location != edited.Location
	edited.SignificantChange = edited.Status == "PUBLISHED" && scheduleOrLocationChanged
	if edited.SignificantChange && !s.clock.Now().Before(previous.StartAt.Add(-time.Hour)) {
		return domain.Event{}, domain.ErrState
	}
	edited.GrantCancellationException = edited.SignificantChange && !s.clock.Now().Before(previous.StartAt.Add(-24*time.Hour))
	edited.ChangedBy = actor.ID

	if edited.Capacity < edited.RegisteredCount {
		return domain.Event{}, domain.ErrCapacity
	}
	if edited.WaitlistLimit != nil && *edited.WaitlistLimit < edited.WaitlistCount {
		return domain.Event{}, domain.ErrValidation
	}
	if err := edited.Fields.Validate(s.clock.Now()); err != nil {
		return domain.Event{}, err
	}
	return edited, nil
}
