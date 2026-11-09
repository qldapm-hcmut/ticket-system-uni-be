package application

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

const testID = "11111111-1111-4111-8111-111111111111"

type fakeStore struct {
	calls   int
	members []Member
	filter  Filter
	active  bool
	err     error
}

func (f *fakeStore) CreateUnit(context.Context, string, UnitCreate) (Unit, error) {
	f.calls++
	return Unit{}, f.err
}
func (f *fakeStore) SetMembers(_ context.Context, _, _ string, m []Member) ([]UnitMember, error) {
	f.calls++
	f.members = m
	return []UnitMember{}, f.err
}
func (f *fakeStore) ListUsers(_ context.Context, _ string, v Filter) (UserList, error) {
	f.calls++
	f.filter = v
	return UserList{}, f.err
}
func (f *fakeStore) UpdateUser(_ context.Context, _, _ string, v bool) (auth.Profile, error) {
	f.calls++
	f.active = v
	return auth.Profile{}, f.err
}
func TestValidation(t *testing.T) {
	ctx := context.Background()
	f := &fakeStore{}
	s := New(f)
	for _, in := range []UnitCreate{{}, {Code: "C", Name: "N", Type: "ADMIN"}, {Code: strings.Repeat("界", 31), Name: "N", Type: "CLUB"}, {Code: "C", Name: strings.Repeat("界", 201), Type: "CLUB"}} {
		if _, err := s.CreateUnit(ctx, testID, in); !errors.Is(err, ErrValidation) {
			t.Fatalf("unit %+v: %v", in, err)
		}
	}
	for _, in := range [][]Member{nil, {{UserID: "invalid", MemberRole: "ORGANIZER"}}, {{UserID: testID, MemberRole: "ADMIN"}}, {{UserID: testID, MemberRole: "ORGANIZER"}, {UserID: strings.ToUpper(testID), MemberRole: "ORGANIZER"}}} {
		if _, err := s.SetMembers(ctx, testID, testID, in); !errors.Is(err, ErrValidation) {
			t.Fatalf("members: %v", err)
		}
	}
	for _, filter := range []Filter{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 101}, {Page: math.MaxInt64, PageSize: 20}, {Page: 1, PageSize: 20, Role: "OWNER"}} {
		if _, err := s.ListUsers(ctx, testID, filter); !errors.Is(err, ErrValidation) {
			t.Fatalf("filter: %v", err)
		}
	}
	if _, err := s.UpdateUser(ctx, testID, "invalid", true); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached store")
	}
	if _, err := s.SetMembers(ctx, testID, testID, []Member{}); err != nil || f.members == nil {
		t.Fatalf("empty replacement: %v", err)
	}
	for _, role := range []string{"", "STUDENT", "ORGANIZER", "ADMIN"} {
		filter := Filter{Q: "Name", Role: role, Page: 2, PageSize: 10}
		if _, err := s.ListUsers(ctx, testID, filter); err != nil || f.filter != filter {
			t.Fatalf("valid role %s: %v", role, err)
		}
	}
	if _, err := s.UpdateUser(ctx, testID, testID, false); err != nil || f.active {
		t.Fatal("false must be forwarded")
	}
	f.err = ErrForbidden
	if _, err := s.CreateUnit(ctx, testID, UnitCreate{Code: "C", Name: "N", Type: "CLUB"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("store error lost")
	}
}
