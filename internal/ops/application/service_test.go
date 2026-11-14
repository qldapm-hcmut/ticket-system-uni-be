package application

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadStudentsPreservesMSSVAndAcceptsBOM(t *testing.T) {
	rows, err := ReadStudents(strings.NewReader("\ufeffemail,fullName,studentCode\nStudent@hcmut.edu.vn,Nguyễn An,0012345\n"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].Email != "student@hcmut.edu.vn" || rows[0].StudentCode != "0012345" || rows[0].FullName != "Nguyễn An" || rows[0].Row != 2 {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestReadStudentsRejectsInvalidRosterWithoutPersonalData(t *testing.T) {
	for name, csv := range map[string]string{
		"headers":         "fullName,email,studentCode\nName,a@hcmut.edu.vn,1\n",
		"empty":           "email,fullName,studentCode\n",
		"duplicate email": "email,fullName,studentCode\na@hcmut.edu.vn,Name,1\nA@hcmut.edu.vn,Name,2\n",
		"duplicate code":  "email,fullName,studentCode\na@hcmut.edu.vn,Name,1\nb@hcmut.edu.vn,Name,1\n",
		"domain":          "email,fullName,studentCode\na@evil.hcmut.edu.vn,Name,1\n",
		"display name":    "email,fullName,studentCode\nName <a@hcmut.edu.vn>,Name,1\n",
		"missing code":    "email,fullName,studentCode\na@hcmut.edu.vn,Name,\n",
		"long code":       "email,fullName,studentCode\na@hcmut.edu.vn,Name,12345678901\n",
		"blank name":      "email,fullName,studentCode\na@hcmut.edu.vn, ,1\n",
		"invalid utf8":    "email,fullName,studentCode\na@hcmut.edu.vn,\xff,1\n",
		"nul":             "email,fullName,studentCode\na@hcmut.edu.vn,Name,1\x00\n",
		"malformed":       "email,fullName,studentCode\na@hcmut.edu.vn,\"Name,1\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadStudents(strings.NewReader(csv))
			if err == nil {
				t.Fatal("accepted invalid roster")
			}
			for _, pii := range []string{"a@", "12345678901", "evil"} {
				if strings.Contains(err.Error(), pii) {
					t.Fatalf("unsafe error: %v", err)
				}
			}
		})
	}
}

func TestReadStudentsCharacterLimits(t *testing.T) {
	_, err := ReadStudents(strings.NewReader("email,fullName,studentCode\na@hcmut.edu.vn," + strings.Repeat("ế", 120) + ",001\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadStudents(strings.NewReader("email,fullName,studentCode\na@hcmut.edu.vn," + strings.Repeat("ế", 121) + ",001\n"))
	if err == nil {
		t.Fatal("accepted long name")
	}
}

type recordingStore struct{ calls int }

func (s *recordingStore) Import(context.Context, ImportRequest) (ImportResult, error) {
	s.calls++
	return ImportResult{Created: 1}, nil
}
func (s *recordingStore) Admin(context.Context, AdminRequest) (AdminResult, error) {
	s.calls++
	return AdminResult{Changed: true}, nil
}

func TestServiceRejectsInvalidInputBeforeStore(t *testing.T) {
	store := &recordingStore{}
	svc := New(store)
	for _, req := range []ImportRequest{
		{},
		{Attribution: Attribution{Operator: "dev", Ticket: "T-1"}},
		{Attribution: Attribution{Operator: "dev", Ticket: "T-1"}, Students: []Student{{Email: "a@hcmut.edu.vn", FullName: "A", StudentCode: "1"}, {Email: "A@hcmut.edu.vn", FullName: "B", StudentCode: "2"}}},
	} {
		if _, err := svc.Import(context.Background(), req); !errors.Is(err, ErrValidation) {
			t.Fatalf("err=%v", err)
		}
	}
	for _, req := range []AdminRequest{
		{},
		{Action: "grant", UserID: "invalid", Attribution: Attribution{Operator: "dev", Ticket: "T-1"}},
		{Action: "bootstrap", Email: "a@evil.test", FullName: "A", Attribution: Attribution{Operator: "dev", Ticket: "T-1"}},
		{Action: "bootstrap", Email: "a@hcmut.edu.vn", FullName: "A", UserID: "11111111-1111-1111-1111-111111111111", Attribution: Attribution{Operator: "dev", Ticket: "T-1"}},
	} {
		if _, err := svc.Admin(context.Background(), req); !errors.Is(err, ErrValidation) {
			t.Fatalf("err=%v", err)
		}
	}
	if store.calls != 0 {
		t.Fatal("invalid input reached store")
	}
}

func TestServiceAcceptsValidRequests(t *testing.T) {
	store := &recordingStore{}
	svc := New(store)
	attr := Attribution{Operator: "dev-id", Ticket: "T-1"}
	if _, err := svc.Import(context.Background(), ImportRequest{Attribution: attr, Students: []Student{{Email: "a@hcmut.edu.vn", FullName: "A", StudentCode: "01", Row: 2}}}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"grant", "revoke"} {
		if _, err := svc.Admin(context.Background(), AdminRequest{Action: action, UserID: "11111111-1111-1111-1111-111111111111", Attribution: attr}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Admin(context.Background(), AdminRequest{Action: "bootstrap", Email: "a@hcmut.edu.vn", FullName: "A", Attribution: attr}); err != nil {
		t.Fatal(err)
	}
	if store.calls != 4 {
		t.Fatal("valid operations not applied")
	}
}
