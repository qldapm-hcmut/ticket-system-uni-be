package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrCapacity      = errors.New("capacity below registered count")
	ErrValidation    = errors.New("invalid event fields")
	ErrNotFound      = errors.New("event not found")
	ErrForbidden     = errors.New("organizer role required")
	ErrVersion       = errors.New("event version conflict")
	ErrState         = errors.New("invalid event state")
	ErrSelfApproval  = errors.New("self approval not allowed")
	ErrApprovalStart = errors.New("event has already started")
)

type Fields struct {
	Title                string    `json:"title"`
	Description          *string   `json:"description"`
	Location             string    `json:"location"`
	StartAt              time.Time `json:"startAt"`
	EndAt                time.Time `json:"endAt"`
	RegistrationOpensAt  time.Time `json:"registrationOpensAt"`
	RegistrationClosesAt time.Time `json:"registrationClosesAt"`
	Capacity             int32     `json:"capacity"`
	WaitlistLimit        *int32    `json:"waitlistLimit"`
}

func (f Fields) Validate(now time.Time) error {
	if strings.TrimSpace(f.Title) == "" || utf8.RuneCountInString(f.Title) > 200 ||
		strings.TrimSpace(f.Location) == "" || utf8.RuneCountInString(f.Location) > 255 {
		return ErrValidation
	}
	if f.Capacity < 1 || f.Capacity > 5000 || (f.WaitlistLimit != nil && *f.WaitlistLimit < 0) {
		return ErrValidation
	}
	if !f.StartAt.After(now) || !f.EndAt.After(f.StartAt) ||
		!f.RegistrationOpensAt.Before(f.RegistrationClosesAt) || f.RegistrationClosesAt.After(f.StartAt) {
		return ErrValidation
	}
	return nil
}

type Event struct {
	Fields
	ID                         string
	UnitID                     string
	CreatedBy                  string
	Status                     string
	Version                    int32
	RegisteredCount            int32
	WaitlistCount              int32
	GrantCancellationException bool
	SignificantChange          bool
	NotifiedCount              int64
	ChangedBy                  string
	PromotedCount              int32
}
