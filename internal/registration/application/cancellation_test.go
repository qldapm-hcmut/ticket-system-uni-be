package application

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type cancellationFake struct {
	RegistrationRepository
	calls             int
	id, actor, reason string
	err               error
}

func (f *cancellationFake) Do(ctx context.Context, fn func(context.Context, RegistrationRepository) error) error {
	f.calls++
	return fn(ctx, f)
}
func (f *cancellationFake) Cancel(_ context.Context, id, actor, reason string) (CancellationResult, error) {
	f.id, f.actor, f.reason = id, actor, reason
	return CancellationResult{Registration: Registration{ID: id}}, f.err
}
func TestCancellationInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  string
		want    string
		invalid bool
	}{
		{name: "optional empty"},
		{name: "trim", reason: "  schedule conflict  ", want: "schedule conflict"},
		{name: "unicode length", reason: strings.Repeat("á", 255), want: strings.Repeat("á", 255)},
		{name: "padded raw boundary", reason: " " + strings.Repeat("á", 253) + " ", want: strings.Repeat("á", 253)},
		{name: "padded raw over limit", reason: " " + strings.Repeat("á", 255), invalid: true},
		{name: "too long", reason: strings.Repeat("á", 256), invalid: true},
		{name: "invalid utf8", reason: string([]byte{0xff}), invalid: true},
		{name: "NUL", reason: "a\x00b", invalid: true},
		{name: "NUL only", reason: "\x00", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &cancellationFake{}
			out, err := New(f).Cancel(context.Background(), "organizer-without-student-code", "reg", tc.reason)
			if tc.invalid {
				if !errors.Is(err, ErrValidation) || f.calls != 0 {
					t.Fatalf("err=%v calls=%d", err, f.calls)
				}
				return
			}
			if err != nil || f.calls != 1 || f.actor != "organizer-without-student-code" || f.id != "reg" || f.reason != tc.want || out.Registration.ID != "reg" {
				t.Fatalf("out=%+v err=%v fake=%+v", out, err, f)
			}
		})
	}
}
func TestCancellationPropagatesTransactionFailure(t *testing.T) {
	for _, err := range []error{ErrForbidden, ErrNotFound, ErrValidation, ErrCancelDeadline, ErrState, errors.New("audit failed")} {
		f := &cancellationFake{err: err}
		if _, got := New(f).Cancel(context.Background(), "actor", "reg", "reason"); !errors.Is(got, err) {
			t.Fatalf("got %v want %v", got, err)
		}
	}
}
