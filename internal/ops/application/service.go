// Package application implements internal, dev-operated account maintenance.
package application

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrValidation = errors.New("invalid operations input")
	ErrConflict   = errors.New("account identity conflict")
	ErrNotFound   = errors.New("account not found")
)

// RowConflict identifies a roster conflict without exposing identity values.
type RowConflict struct{ Row int }

func (e *RowConflict) Error() string { return fmt.Sprintf("row %d: account identity conflict", e.Row) }
func (e *RowConflict) Unwrap() error { return ErrConflict }

type Attribution struct{ Operator, Ticket string }
type Student struct {
	Email, FullName, StudentCode string
	Row                          int
}
type ImportRequest struct {
	Attribution
	Students []Student
	DryRun   bool
}
type ImportResult struct{ Created, Enriched, Unchanged int }
type AdminRequest struct {
	Attribution
	Action, UserID, Email, FullName string
}
type AdminResult struct{ Changed bool }

// Store owns mutations and their audit entries in one transaction.
type Store interface {
	Import(context.Context, ImportRequest) (ImportResult, error)
	Admin(context.Context, AdminRequest) (AdminResult, error)
}
type Service struct{ store Store }

func New(store Store) *Service { return &Service{store: store} }

func validText(s string, max int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func validEmail(s string) bool {
	address, err := mail.ParseAddress(s)
	local, domain, ok := strings.Cut(s, "@")
	return err == nil && address.Address == s && ok && local != "" && domain == "hcmut.edu.vn" && !strings.ContainsAny(s, "\r\n\x00")
}
func validateAttribution(a Attribution) error {
	if !validText(a.Operator, 200) || !validText(a.Ticket, 200) || strings.ContainsAny(a.Operator+a.Ticket, "\r\n") {
		return ErrValidation
	}
	return nil
}
func validateStudents(rows []Student) error {
	if len(rows) == 0 {
		return fmt.Errorf("%w: empty roster", ErrValidation)
	}
	emails, codes := map[string]bool{}, map[string]bool{}
	for i, row := range rows {
		number := row.Row
		if number == 0 {
			number = i + 2
		}
		category := ""
		switch {
		case !validEmail(row.Email):
			category = "invalid email"
		case !validText(row.FullName, 120):
			category = "invalid full name"
		case !validText(row.StudentCode, 10):
			category = "invalid student code"
		case emails[strings.ToLower(row.Email)]:
			category = "duplicate email"
		case codes[row.StudentCode]:
			category = "duplicate student code"
		}
		if category != "" {
			return fmt.Errorf("%w: row %d: %s", ErrValidation, number, category)
		}
		emails[strings.ToLower(row.Email)] = true
		codes[row.StudentCode] = true
	}
	return nil
}

// ReadStudents validates the complete CSV before the caller opens a database.
// Errors deliberately exclude CSV values, including encoding/csv parse errors.
func ReadStudents(r io.Reader) ([]Student, error) {
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: invalid CSV header", ErrValidation)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	if len(header) != 3 || header[0] != "email" || header[1] != "fullName" || header[2] != "studentCode" {
		return nil, fmt.Errorf("%w: expected email,fullName,studentCode header", ErrValidation)
	}
	var rows []Student
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: malformed CSV record %d", ErrValidation, len(rows)+2)
		}
		line, _ := reader.FieldPos(0)
		rows = append(rows, Student{Email: strings.ToLower(fields[0]), FullName: fields[1], StudentCode: fields[2], Row: line})
	}
	if err := validateStudents(rows); err != nil {
		return nil, err
	}
	return rows, nil
}
func (in ImportRequest) Validate() error {
	if err := validateAttribution(in.Attribution); err != nil {
		return err
	}
	return validateStudents(in.Students)
}
func (s *Service) Import(ctx context.Context, in ImportRequest) (ImportResult, error) {
	if err := in.Validate(); err != nil {
		return ImportResult{}, err
	}
	return s.store.Import(ctx, in)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (in AdminRequest) Validate() error {
	if err := validateAttribution(in.Attribution); err != nil {
		return err
	}
	switch in.Action {
	case "bootstrap":
		if in.UserID != "" || !validEmail(strings.ToLower(in.Email)) || !validText(in.FullName, 120) {
			return ErrValidation
		}
	case "grant", "revoke":
		if !uuidPattern.MatchString(in.UserID) || in.Email != "" || in.FullName != "" {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	return nil
}
func (s *Service) Admin(ctx context.Context, in AdminRequest) (AdminResult, error) {
	if err := in.Validate(); err != nil {
		return AdminResult{}, err
	}
	in.Email = strings.ToLower(in.Email)
	return s.store.Admin(ctx, in)
}
