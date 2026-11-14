package httpdto

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

// Wire values must not drift across token, /me and admin profile responses.
func TestProfileWireShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile application.Profile
		want    string
	}{
		{"absent student and memberships", application.Profile{}, `{"id":"","email":"","fullName":"","studentCode":null,"isAdmin":false,"isActive":false,"roles":[],"units":[]}`},
		{"empty memberships", application.Profile{Memberships: []application.Membership{}}, `{"id":"","email":"","fullName":"","studentCode":null,"isAdmin":false,"isActive":false,"roles":[],"units":[]}`},
		{"all roles and ordered memberships", application.Profile{
			User: application.User{ID: "user", Email: "test@hcmut.edu.vn", FullName: "Test User", StudentCode: "123", IsAdmin: true, IsActive: true},
			Memberships: []application.Membership{
				{UnitID: "second", Code: "CLUB", Name: "Club", MemberRole: "ORGANIZER"},
				{UnitID: "first", Code: "CSE", Name: "Computer Science", MemberRole: "ORGANIZER"},
			},
		}, `{"id":"user","email":"test@hcmut.edu.vn","fullName":"Test User","studentCode":"123","isAdmin":true,"isActive":true,"roles":["STUDENT","ORGANIZER","ADMIN"],"units":[{"unit":{"id":"second","code":"CLUB","name":"Club"},"memberRole":"ORGANIZER"},{"unit":{"id":"first","code":"CSE","name":"Computer Science"},"memberRole":"ORGANIZER"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(Profile(tc.profile))
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("profile=%s, want %s", encoded, tc.want)
			}
		})
	}
}
