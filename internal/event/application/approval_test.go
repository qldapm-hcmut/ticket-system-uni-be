package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type approvalFake struct {
	ApprovalRepository
	current                domain.Event
	auditAction, auditText string
	mutated                bool
}

func (r *approvalFake) Get(context.Context, string, bool) (domain.Event, error) {
	return r.current, nil
}
func (r *approvalFake) Approve(_ context.Context, _ string, _ int32, _ string, _ time.Time) (domain.Event, error) {
	r.mutated = true
	return r.current, nil
}
func (r *approvalFake) Reject(_ context.Context, _ string, _ int32, _ string) (domain.Event, error) {
	r.mutated = true
	return r.current, nil
}
func (r *approvalFake) NotifyDecision(context.Context, string, bool, string) error { return nil }
func (r *approvalFake) Audit(_ context.Context, _, _, action, text string) error {
	r.auditAction, r.auditText = action, text
	return nil
}

type approvalUOW struct {
	r     *approvalFake
	calls int
}

func (u *approvalUOW) DoApproval(_ context.Context, fn func(ApprovalRepository) error) error {
	u.calls++
	return fn(u.r)
}

func TestRejectAuthorizationAndCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		admin        bool
		want         error
	}{
		{"nonadmin", "valid", false, domain.ErrForbidden},
		{"blank", " \t\n", true, domain.ErrValidation},
		{"500 Unicode characters", strings.Repeat("ế", 500), true, nil},
		{"501 characters", strings.Repeat("a", 501), true, domain.ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &approvalFake{current: domain.Event{Status: "PENDING_APPROVAL"}}
			u := &approvalUOW{r: r}
			_, err := NewApprovalService(u, kernel.SystemClock{}).Reject(context.Background(), Actor{ID: "admin", IsAdmin: tc.admin}, "event", 0, tc.reason)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if tc.want != nil && (r.mutated || u.calls != 0) {
				t.Fatal("rejected request reached transaction")
			}
			if tc.want == nil && (!r.mutated || r.auditText != tc.reason) {
				t.Fatal("reason not preserved")
			}
		})
	}
}

// The pending event has already started; publishing must not mutate or audit it.
func TestApproveRejectsPastStart(t *testing.T) {
	r := &approvalFake{current: domain.Event{CreatedBy: "creator", Status: "PENDING_APPROVAL", Fields: domain.Fields{StartAt: time.Now().Add(-time.Hour)}}}
	_, err := NewApprovalService(&approvalUOW{r: r}, kernel.SystemClock{}).Approve(context.Background(), Actor{ID: "admin", IsAdmin: true}, "event", 0, "")
	if err == nil || r.mutated || r.auditAction != "" {
		t.Fatalf("past event published: err=%v mutated=%v audit=%s", err, r.mutated, r.auditAction)
	}
}
