package application

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

var (
	ErrValidation      = errors.New("invalid admin input")
	ErrUnauthenticated = errors.New("active account required")
	ErrForbidden       = errors.New("admin required")
	ErrNotFound        = errors.New("resource not found")
	ErrConflict        = errors.New("unit code already exists")
)

type Unit struct {
	ID, Code, Name, Type string
	IsActive             bool
}
type UnitCreate struct{ Code, Name, Type string }
type Member struct{ UserID, MemberRole string }
type UnitMember struct{ UserID, FullName, MemberRole string }
type Filter struct {
	Q, Role        string
	Page, PageSize int64
}
type UserList struct {
	Items      []auth.Profile
	TotalItems int64
}

// Store owns atomic mutations, including authorization against current database
// state and an activity log in the same transaction as each mutation.
type Store interface {
	CreateUnit(context.Context, string, UnitCreate) (Unit, error)
	SetMembers(context.Context, string, string, []Member) ([]UnitMember, error)
	ListUsers(context.Context, string, Filter) (UserList, error)
	UpdateUser(context.Context, string, string, bool) (auth.Profile, error)
}
type Service struct{ store Store }

func New(store Store) *Service { return &Service{store} }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool { return uuidPattern.MatchString(id) }
func (s *Service) CreateUnit(ctx context.Context, actor string, in UnitCreate) (Unit, error) {
	if !ValidID(actor) || strings.TrimSpace(in.Code) == "" || strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Code) > 30 || utf8.RuneCountInString(in.Name) > 200 || (in.Type != "FACULTY" && in.Type != "CLUB" && in.Type != "DEPARTMENT") {
		return Unit{}, ErrValidation
	}
	return s.store.CreateUnit(ctx, actor, in)
}
func (s *Service) SetMembers(ctx context.Context, actor, unit string, members []Member) ([]UnitMember, error) {
	if !ValidID(actor) || !ValidID(unit) || members == nil {
		return nil, ErrValidation
	}
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		key := strings.ToLower(m.UserID)
		if !ValidID(m.UserID) || m.MemberRole != "ORGANIZER" || seen[key] {
			return nil, ErrValidation
		}
		seen[key] = true
	}
	return s.store.SetMembers(ctx, actor, unit, members)
}
func (s *Service) ListUsers(ctx context.Context, actor string, f Filter) (UserList, error) {
	if !ValidID(actor) || f.Page < 1 || f.PageSize < 1 || f.PageSize > 100 || f.Page-1 > math.MaxInt64/f.PageSize || (f.Role != "" && f.Role != "STUDENT" && f.Role != "ORGANIZER" && f.Role != "ADMIN") {
		return UserList{}, ErrValidation
	}
	return s.store.ListUsers(ctx, actor, f)
}
func (s *Service) UpdateUser(ctx context.Context, actor, user string, active bool) (auth.Profile, error) {
	if !ValidID(actor) || !ValidID(user) {
		return auth.Profile{}, ErrValidation
	}
	return s.store.UpdateUser(ctx, actor, user, active)
}
