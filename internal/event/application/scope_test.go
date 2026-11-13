package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

// Mutation operations must lock, then check unit scope, before exposing state
// or version conflicts or invoking the caller's merge. Admin is not a bypass.
func TestMutationScopePrecedence(t *testing.T) {
	failure := errors.New("scope database unavailable")
	for _, operation := range []string{"patch", "cancel", "delete"} {
		for _, tc := range []struct {
			name             string
			getErr, scopeErr error
			want             error
			calls            []string
		}{
			{"missing", domain.ErrNotFound, nil, domain.ErrNotFound, []string{"lock"}},
			{"lookup failure", failure, nil, failure, []string{"lock"}},
			{"scope failure", nil, failure, failure, []string{"lock", "scope"}},
			{"foreign scope hides stale version and state", nil, nil, domain.ErrNotFound, []string{"lock", "scope"}},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				r := &deleteRepo{event: domain.Event{ID: "event", UnitID: "unit", Status: "COMPLETED", Version: 3}, getErr: tc.getErr, scopeErr: tc.scopeErr}
				s := New(&deleteUOW{repo: r}, nil)
				actor := Actor{ID: "actor", IsOrganizer: true, IsAdmin: true}
				var err error
				switch operation {
				case "patch":
					_, err = s.Patch(context.Background(), actor, "event", 1, func(domain.Fields) (domain.Fields, error) {
						t.Fatal("unauthorized patch invoked merge")
						return domain.Fields{}, nil
					})
				case "cancel":
					_, err = s.Cancel(context.Background(), actor, "event", 1, "valid reason")
				case "delete":
					err = s.DeleteDraft(context.Background(), actor, "event")
				}
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(r.calls, tc.calls) {
					t.Fatalf("error=%v want=%v calls=%v want=%v", err, tc.want, r.calls, tc.calls)
				}
			})
		}
	}
}
