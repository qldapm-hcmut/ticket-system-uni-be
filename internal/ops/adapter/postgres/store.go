// Package postgres persists internal operations and their audit atomically.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/ops/application"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
)

type Store struct{ runner *platform.TransactionRunner }

func New(pool *pgxpool.Pool) *Store { return &Store{runner: platform.NewTransactionRunner(pool)} }

var errDryRun = errors.New("operations dry run rollback")

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return a.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return a.ErrConflict
	}
	return err
}
func audit(ctx context.Context, q *db.Queries, id pgtype.UUID, action string, attr a.Attribution, batch string, before, after any) error {
	metadata := map[string]any{"source": "ops-cli", "operator": attr.Operator, "ticket": attr.Ticket, "before": before, "after": after}
	if batch != "" {
		metadata["batchId"] = batch
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return q.OpsInsertActivity(ctx, db.OpsInsertActivityParams{EntityID: id, Action: action, Metadata: raw})
}
func (s *Store) Import(ctx context.Context, in a.ImportRequest) (a.ImportResult, error) {
	var batch [16]byte
	if _, err := rand.Read(batch[:]); err != nil {
		return a.ImportResult{}, err
	}
	batchID := hex.EncodeToString(batch[:])
	rows := append([]a.Student(nil), in.Students...)
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].Email) < strings.ToLower(rows[j].Email) })
	var out a.ImportResult
	err := s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := db.New(tx)
		for _, row := range rows {
			if err := importStudent(ctx, q, row, in.Attribution, batchID, &out); err != nil {
				if errors.Is(mapError(err), a.ErrConflict) {
					return &a.RowConflict{Row: row.Row}
				}
				return err
			}
		}
		if in.DryRun {
			return errDryRun
		}
		return nil
	})
	// Dry-run is successful only when the runner also completed rollback.
	if err == errDryRun {
		return out, nil
	}
	if err != nil {
		return a.ImportResult{}, mapError(err)
	}
	return out, nil
}
func importStudent(ctx context.Context, q *db.Queries, row a.Student, attr a.Attribution, batch string, out *a.ImportResult) error {
	email := strings.ToLower(row.Email)
	user, err := q.OpsLockUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		user, err = q.OpsInsertUser(ctx, db.OpsInsertUserParams{Email: email, FullName: row.FullName, StudentCode: pgtype.Text{String: row.StudentCode, Valid: true}, IsAdmin: false})
		if err == nil {
			out.Created++
			return audit(ctx, q, user.ID, "UPDATE_USER", attr, batch, nil, map[string]bool{"exists": true, "isStudent": true})
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// A concurrent login/import won the email. ON CONFLICT did not abort
		// this transaction, so reread its committed account and preserve its sub.
		user, err = q.OpsLockUserByEmail(ctx, email)
	}
	if err != nil {
		return err
	}
	if user.StudentCode.Valid {
		if user.StudentCode.String != row.StudentCode {
			return a.ErrConflict
		}
		out.Unchanged++
		return nil
	}
	if err := q.OpsSetStudentCode(ctx, db.OpsSetStudentCodeParams{ID: user.ID, StudentCode: pgtype.Text{String: row.StudentCode, Valid: true}}); err != nil {
		return err
	}
	out.Enriched++
	return audit(ctx, q, user.ID, "UPDATE_USER", attr, batch, map[string]bool{"isStudent": false}, map[string]bool{"isStudent": true})
}
func (s *Store) Admin(ctx context.Context, in a.AdminRequest) (a.AdminResult, error) {
	var out a.AdminResult
	err := s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := db.New(tx)
		var user db.User
		var err error
		if in.Action == "bootstrap" {
			user, err = q.OpsInsertUser(ctx, db.OpsInsertUserParams{Email: strings.ToLower(in.Email), FullName: in.FullName, IsAdmin: true})
			if err == nil {
				out.Changed = true
				return audit(ctx, q, user.ID, "GRANT_ADMIN", in.Attribution, "", nil, map[string]bool{"isAdmin": true})
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			user, err = q.OpsLockUserByEmail(ctx, strings.ToLower(in.Email))
			if err != nil {
				return err
			}
			if !user.IsAdmin {
				return a.ErrConflict
			}
			return nil
		}
		var id pgtype.UUID
		if err = id.Scan(in.UserID); err != nil {
			return a.ErrValidation
		}
		user, err = q.OpsLockUserByID(ctx, id)
		if err != nil {
			return err
		}
		desired := in.Action == "grant"
		if user.IsAdmin == desired {
			return nil
		}
		if err = q.OpsSetAdmin(ctx, db.OpsSetAdminParams{ID: user.ID, IsAdmin: desired}); err != nil {
			return err
		}
		action := "REVOKE_ADMIN"
		if desired {
			action = "GRANT_ADMIN"
		}
		out.Changed = true
		return audit(ctx, q, user.ID, action, in.Attribution, "", map[string]bool{"isAdmin": user.IsAdmin}, map[string]bool{"isAdmin": desired})
	})
	if err != nil {
		return a.AdminResult{}, mapError(err)
	}
	return out, nil
}
