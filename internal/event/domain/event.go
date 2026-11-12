package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrCapacity   = errors.New("capacity below registered count")
	ErrValidation = errors.New("invalid event fields")
	ErrNotFound   = errors.New("event not found")
	ErrForbidden  = errors.New("organizer role required")
	ErrVersion    = errors.New("event version conflict")
	ErrState      = errors.New("invalid event state")
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

type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }
func (e *FieldError) Unwrap() error { return ErrValidation }

func (f Fields) Validate(now time.Time) error {
	if strings.TrimSpace(f.Title) == "" || utf8.RuneCountInString(f.Title) > 200 || strings.TrimSpace(f.Location) == "" || utf8.RuneCountInString(f.Location) > 255 || f.Capacity < 1 || f.Capacity > 5000 || (f.WaitlistLimit != nil && *f.WaitlistLimit < 0) {
		return ErrValidation
	}
	for _, field := range []struct {
		name  string
		value time.Time
	}{
		{"startAt", f.StartAt}, {"endAt", f.EndAt},
		{"registrationOpensAt", f.RegistrationOpensAt}, {"registrationClosesAt", f.RegistrationClosesAt},
	} {
		if field.value.IsZero() {
			return &FieldError{field.name, "Vui lòng nhập thời gian hợp lệ."}
		}
	}
	if !f.StartAt.After(now) {
		return &FieldError{"startAt", "Thời gian bắt đầu phải sau thời điểm hiện tại."}
	}
	if !f.EndAt.After(f.StartAt) {
		return &FieldError{"endAt", "Thời gian kết thúc phải sau thời gian bắt đầu."}
	}
	if !f.RegistrationOpensAt.Before(f.RegistrationClosesAt) {
		return &FieldError{"registrationOpensAt", "Thời gian mở đăng ký phải trước thời gian đóng đăng ký."}
	}
	if f.RegistrationClosesAt.After(f.StartAt) {
		return &FieldError{"registrationClosesAt", "Thời gian đóng đăng ký phải trước hoặc bằng lúc bắt đầu."}
	}
	return nil
}

// Published events may keep an elapsed deadline when unrelated fields change.
// New events, drafts and approval transitions require a future deadline.
func (f Fields) ValidateRegistrationDeadline(now time.Time) error {
	if !f.RegistrationClosesAt.After(now) {
		return &FieldError{"registrationClosesAt", "Thời gian đóng đăng ký phải sau thời điểm hiện tại."}
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
