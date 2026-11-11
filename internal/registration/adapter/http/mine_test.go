package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

type ownedStore struct {
	a.RegistrationRepository
	filter     a.MineFilter
	owner      string
	rows       []a.MyRegistration
	err        error
	active     a.Registration
	registered a.Registration
}

func (s *ownedStore) Do(ctx context.Context, fn func(context.Context, a.RegistrationRepository) error) error {
	return fn(ctx, s)
}
func (s *ownedStore) Mine(_ context.Context, owner string, f a.MineFilter) ([]a.MyRegistration, int64, error) {
	s.filter = f
	s.owner = owner
	return s.rows, int64(len(s.rows)), s.err
}

func (s *ownedStore) Active(context.Context, string, string) (a.Registration, error) {
	return s.active, nil
}

func (s *ownedStore) Register(context.Context, string, string) (a.Registration, error) {
	return s.registered, nil
}

func TestMineHTTPRejectsInvalidQuery(t *testing.T) {
	for _, q := range []string{"upcoming=yes", "upcoming=1", "upcoming=TRUE", "upcoming=", "status=BOGUS", "page=0", "pageSize=101", "page=2147483647&pageSize=100", "page=", "pageSize=", "status=", "upcoming=true&upcoming=false", "status=WAITLISTED&status=EXPIRED", "page=1&page=2"} {
		t.Run(q, func(t *testing.T) {
			e := echo.New()
			New(a.New(&ownedStore{})).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.profile", auth.Profile{ID: "owner", StudentCode: "student"})
					return next(c)
				}
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me/registrations?"+q, nil))
			if rec.Code != 400 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRegistrationDTOCodeVisibility(t *testing.T) {
	for _, status := range []string{"REGISTERED", "WAITLISTED", "CANCELLED", "CANCELLED_BY_EVENT", "EXPIRED"} {
		t.Run(status, func(t *testing.T) {
			raw, err := json.Marshal(dto(a.Registration{Status: status, CheckinCode: "K7Q2M9XD"}))
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			var want any
			if status == "REGISTERED" {
				want = "K7Q2-M9XD"
			}
			if out["checkinCode"] != want || out["waitlistPosition"] != nil {
				t.Fatalf("%s", raw)
			}
		})
	}
}

func TestRegistrationDTORejectsNonpositiveRank(t *testing.T) {
	for _, rank := range []int64{-1, 0, 1} {
		out := dto(a.Registration{Status: "WAITLISTED", WaitlistPosition: &rank})
		position, _ := out["waitlistPosition"].(*int64)
		if rank < 1 && position != nil || rank == 1 && (position == nil || *position != 1) {
			t.Fatalf("rank=%d out=%+v", rank, out)
		}
	}
}

func TestMineHTTPCancellationFields(t *testing.T) {
	at := time.Date(2026, 10, 26, 1, 2, 3, 0, time.UTC)
	by := "STUDENT"
	s := &ownedStore{rows: []a.MyRegistration{
		{Registration: a.Registration{ID: "cancelled", Status: "CANCELLED", CancelledAt: &at, CancelledBy: &by}},
		{Registration: a.Registration{ID: "registered", Status: "REGISTERED"}},
	}}
	e := echo.New()
	New(a.New(s)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("auth.profile", auth.Profile{ID: "owner", StudentCode: "student"})
			return next(c)
		}
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me/registrations", nil))
	var out struct{ Data []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(out.Data) != 2 || out.Data[0]["cancelledAt"] != "2026-10-26T01:02:03Z" || out.Data[0]["cancelledBy"] != "STUDENT" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, field := range []string{"cancelledAt", "cancelledBy"} {
		if _, exists := out.Data[1][field]; exists {
			t.Fatalf("optional field %s should be absent: %s", field, rec.Body)
		}
	}
}

func TestDuplicateConflictWithoutDetails(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	if err := mapErr(e.NewContext(httptest.NewRequest("POST", "/", nil), rec), a.ErrAlreadyActive); err != nil {
		t.Fatal(err)
	}
	var out struct{ Error map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 409 || out.Error["code"] != "ALREADY_REGISTERED" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, exists := out.Error["details"]; exists {
		t.Fatalf("unexpected details: %s", rec.Body)
	}
}

func TestMineHTTPResponseAndScope(t *testing.T) {
	position := int64(7)
	for _, tc := range []struct {
		name, query, student string
		status               int
	}{
		{"default", "", "student", 200}, {"upcoming", "?upcoming=true&status=WAITLISTED&page=2&pageSize=1", "student", 200},
		{"false", "?upcoming=false", "student", 200}, {"missing student role", "", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &ownedStore{rows: []a.MyRegistration{{Registration: a.Registration{ID: "reg", Status: "WAITLISTED", WaitlistPosition: &position, CheckinCode: "K7Q2M9XD"}, Event: a.EventSummary{ID: "event", Title: "Test", Location: "Campus", Status: "PUBLISHED"}}}}
			e := echo.New()
			New(a.New(s)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.profile", auth.Profile{ID: "owner", StudentCode: tc.student})
					return next(c)
				}
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me/registrations"+tc.query, nil))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
			}
			if tc.status != 200 {
				return
			}
			var out struct {
				Data []struct {
					ID               string
					WaitlistPosition *int64
					CheckinCode      *string
					Event            a.EventSummary
				}
				Meta struct{ Page, PageSize, TotalItems, TotalPages int }
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if s.owner != "owner" || len(out.Data) != 1 || out.Data[0].WaitlistPosition == nil || *out.Data[0].WaitlistPosition != 7 || out.Data[0].CheckinCode != nil || out.Data[0].Event.ID != "event" || out.Meta.TotalItems != 1 {
				t.Fatalf("%s", rec.Body)
			}
			if tc.name == "upcoming" {
				if !s.filter.Upcoming || s.filter.Status != "WAITLISTED" || s.filter.Page != 2 || s.filter.PageSize != 1 || out.Meta.TotalPages != 1 {
					t.Fatalf("filter=%+v body=%s", s.filter, rec.Body)
				}
			} else if s.filter.Upcoming || out.Meta.Page != 1 || out.Meta.PageSize != 20 {
				t.Fatalf("filter=%+v body=%s", s.filter, rec.Body)
			}
		})
	}
}

func TestRegisterHTTPDuplicateID(t *testing.T) {
	s := &ownedStore{active: a.Registration{ID: "existing"}}
	e := echo.New()
	New(a.New(s)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("auth.profile", auth.Profile{ID: "owner", StudentCode: "student"})
			return next(c)
		}
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/events/event/registrations", nil))
	var out struct {
		Error struct {
			Code    string
			Details struct{ RegistrationID string }
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 409 || out.Error.Code != "ALREADY_REGISTERED" || out.Error.Details.RegistrationID != "existing" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRegisterHTTPWaitingPositionAndCode(t *testing.T) {
	rank := int64(3)
	for _, status := range []string{"REGISTERED", "WAITLISTED"} {
		t.Run(status, func(t *testing.T) {
			s := &ownedStore{registered: a.Registration{ID: "new", Status: status, CheckinCode: "K7Q2M9XD", WaitlistPosition: &rank}}
			e := echo.New()
			New(a.New(s)).Register(e, func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.profile", auth.Profile{ID: "owner", StudentCode: "student"})
					return next(c)
				}
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/events/event/registrations", nil))
			var out struct {
				Data struct {
					WaitlistPosition *int64
					CheckinCode      *string
				}
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 201 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if status == "REGISTERED" {
				if out.Data.WaitlistPosition != nil || out.Data.CheckinCode == nil || *out.Data.CheckinCode != "K7Q2-M9XD" {
					t.Fatal(rec.Body)
				}
			} else if out.Data.WaitlistPosition == nil || *out.Data.WaitlistPosition != 3 || out.Data.CheckinCode != nil {
				t.Fatal(rec.Body)
			}
		})
	}
}
