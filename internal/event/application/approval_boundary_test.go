package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

func TestApprovalStartBoundaryAndComment(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		start   time.Time
		comment string
		want    error
	}{
		{"before", now.Add(time.Nanosecond), strings.Repeat("ế", 500), nil},
		{"at start", now, "", domain.ErrApprovalStart},
		{"past", now.Add(-time.Nanosecond), "", domain.ErrApprovalStart},
		{"comment too long", now.Add(time.Hour), strings.Repeat("ế", 501), domain.ErrValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &approvalFake{current: domain.Event{CreatedBy: "creator", Status: "PENDING_APPROVAL", Fields: domain.Fields{StartAt: tc.start}}}
			_, err := NewApprovalService(&approvalUOW{r: r}, editClock{now: now}).Approve(context.Background(), Actor{ID: "admin", IsAdmin: true}, "event", 0, tc.comment)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if tc.want == nil && (!r.mutated || r.auditText != tc.comment) {
				t.Fatal("successful comment not audited")
			}
			if tc.want != nil && (r.mutated || r.auditAction != "") {
				t.Fatal("invalid approval mutated event")
			}
		})
	}
}

func TestSelfApprovalAndRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		r := &approvalFake{current: domain.Event{CreatedBy: "admin", Status: "PENDING_APPROVAL", Fields: domain.Fields{StartAt: time.Now().Add(time.Hour)}}}
		s := NewApprovalService(&approvalUOW{r: r}, editClock{})
		actor := Actor{ID: "admin", IsAdmin: true}
		var err error
		if reject {
			_, err = s.Reject(context.Background(), actor, "event", 0, "valid")
		} else {
			_, err = s.Approve(context.Background(), actor, "event", 0, "")
		}
		if !errors.Is(err, domain.ErrSelfApproval) || r.mutated {
			t.Fatalf("self reject=%v err=%v mutated=%v", reject, err, r.mutated)
		}
	}
}
