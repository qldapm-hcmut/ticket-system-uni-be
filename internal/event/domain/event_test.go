package domain

import (
	"errors"
	"testing"
	"time"
)

func TestScheduleValidation(t *testing.T) {
	now := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	valid := Fields{Title: "Demo", Location: "Campus", Capacity: 1, StartAt: now.Add(2 * time.Hour), EndAt: now.Add(3 * time.Hour), RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour)}
	for _, tc := range []struct {
		name  string
		edit  func(*Fields)
		field string
	}{
		{"open in past is allowed", func(f *Fields) {}, ""},
		{"past start", func(f *Fields) { f.StartAt = now.Add(-time.Minute) }, "startAt"},
		{"start now", func(f *Fields) { f.StartAt = now }, "startAt"},
		{"missing opening", func(f *Fields) { f.RegistrationOpensAt = time.Time{} }, "registrationOpensAt"},
		{"end before start", func(f *Fields) { f.EndAt = f.StartAt }, "endAt"},
		{"open equals close", func(f *Fields) { f.RegistrationOpensAt = f.RegistrationClosesAt }, "registrationOpensAt"},
		{"close after start", func(f *Fields) { f.RegistrationClosesAt = f.StartAt.Add(time.Minute) }, "registrationClosesAt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := valid
			tc.edit(&f)
			err := f.Validate(now)
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var field *FieldError
			if !errors.Is(err, ErrValidation) || !errors.As(err, &field) || field.Field != tc.field {
				t.Fatalf("want validation for %s, got %v", tc.field, err)
			}
		})
	}
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		f := valid
		f.RegistrationClosesAt = now.Add(offset)
		err := f.ValidateRegistrationDeadline(now)
		if (err == nil) != (offset > 0) {
			t.Fatalf("deadline %v: %v", offset, err)
		}
	}
}
