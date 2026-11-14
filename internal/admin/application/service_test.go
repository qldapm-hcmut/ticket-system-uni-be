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
	unit    UnitCreate
	members []Member
	filter  Filter
	active  bool
	err     error
}

func (f *fakeStore) CreateUnit(_ context.Context, _ string, in UnitCreate) (Unit, error) {
	f.calls++
	f.unit = in
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

// These cases catch byte-based length checks, accidental trimming and lost unit types.
func TestCreateUnitValidationBoundaries(t *testing.T) {
	for _, unitType := range []string{"FACULTY", "CLUB", "DEPARTMENT"} {
		t.Run(unitType, func(t *testing.T) {
			store := &fakeStore{}
			in := UnitCreate{Code: strings.Repeat("界", 30), Name: strings.Repeat("界", 200), Type: unitType}
			if _, err := New(store).CreateUnit(context.Background(), testID, in); err != nil || store.unit != in {
				t.Fatalf("unit at rune limits: forwarded=%+v, err=%v", store.unit, err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		in    UnitCreate
		valid bool
	}{
		{"preserve spaces", UnitCreate{Code: " C ", Name: " Club ", Type: "CLUB"}, true},
		{"blank code", UnitCreate{Code: " \t", Name: "Club", Type: "CLUB"}, false},
		{"blank name", UnitCreate{Code: "C", Name: "\n ", Type: "CLUB"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := New(store).CreateUnit(context.Background(), testID, tc.in)
			if tc.valid {
				if err != nil || store.unit != tc.in {
					t.Fatalf("input changed: %+v, err=%v", store.unit, err)
				}
			} else if !errors.Is(err, ErrValidation) || store.calls != 0 {
				t.Fatalf("invalid input reached store: calls=%d, err=%v", store.calls, err)
			}
		})
	}
}

// Guard ordering must reject zero/negative page sizes before dividing, and the
// largest representable offset must remain valid (strict >, not >=).
func TestListUsersPaginationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter Filter
		valid  bool
	}{
		{"zero page size", Filter{Page: 1, PageSize: 0}, false},
		{"negative page size", Filter{Page: 1, PageSize: -1}, false},
		{"minimum page", Filter{Page: math.MinInt64, PageSize: 20}, false},
		{"maximum page", Filter{Page: math.MaxInt64, PageSize: 1}, true},
		{"maximum offset", Filter{Page: math.MaxInt64/2 + 1, PageSize: 2}, true},
		{"offset overflow", Filter{Page: math.MaxInt64/2 + 2, PageSize: 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := New(store).ListUsers(context.Background(), testID, tc.filter)
			if tc.valid {
				if err != nil || store.calls != 1 || store.filter != tc.filter {
					t.Fatalf("valid filter not preserved: %+v, err=%v", store.filter, err)
				}
			} else if !errors.Is(err, ErrValidation) || store.calls != 0 {
				t.Fatalf("invalid filter reached store: calls=%d, err=%v", store.calls, err)
			}
		})
	}
}

func TestSetMembersRejectsCaseInsensitiveDuplicateIDs(t *testing.T) {
	store := &fakeStore{}
	memberID := "abcdefab-cdef-4abc-8def-abcdefabcdef"
	_, err := New(store).SetMembers(context.Background(), testID, testID, []Member{
		{UserID: memberID, MemberRole: "ORGANIZER"},
		{UserID: strings.ToUpper(memberID), MemberRole: "ORGANIZER"},
	})
	if !errors.Is(err, ErrValidation) || store.calls != 0 {
		t.Fatalf("case-insensitive duplicates accepted: calls=%d, err=%v", store.calls, err)
	}
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
