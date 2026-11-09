package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/application"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
)

type Store struct{ runner *platform.TransactionRunner }

func New(pool *pgxpool.Pool) *Store { return &Store{platform.NewTransactionRunner(pool)} }
func uuid(raw string) pgtype.UUID   { var id pgtype.UUID; _ = id.Scan(raw); return id }
func authorize(ctx context.Context, q *db.Queries, actor string) error {
	admin, err := q.AdminActor(ctx, uuid(actor))
	if errors.Is(err, pgx.ErrNoRows) {
		return a.ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if !admin {
		return a.ErrForbidden
	}
	return nil
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return a.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		if pgerr.Code == "23503" {
			return a.ErrNotFound
		}
		if pgerr.Code == "23505" && pgerr.ConstraintName == "units_code_key" {
			return a.ErrConflict
		}
	}
	return err
}
func audit(ctx context.Context, q *db.Queries, actor, entity, id, action string, before, after any) error {
	metadata, err := json.Marshal(map[string]any{"before": before, "after": after})
	if err != nil {
		return err
	}
	return q.AdminInsertActivity(ctx, db.AdminInsertActivityParams{ActorID: uuid(actor), EntityType: entity, EntityID: uuid(id), Action: action, Metadata: metadata})
}
func (s *Store) CreateUnit(ctx context.Context, actor string, in a.UnitCreate) (out a.Unit, err error) {
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := db.New(tx)
		if err := authorize(ctx, q, actor); err != nil {
			return err
		}
		row, err := q.AdminCreateUnit(ctx, db.AdminCreateUnitParams{Code: in.Code, Name: in.Name, Type: db.UnitType(in.Type)})
		if err != nil {
			return err
		}
		out = a.Unit{ID: row.ID.String(), Code: row.Code, Name: row.Name, Type: string(row.Type), IsActive: row.IsActive}
		return audit(ctx, q, actor, "UNIT", out.ID, "CREATE_UNIT", nil, map[string]any{"code": out.Code, "name": out.Name, "type": out.Type, "isActive": out.IsActive})
	})
	if err != nil {
		return a.Unit{}, mapError(err)
	}
	return out, nil
}
func members(ctx context.Context, q *db.Queries, id pgtype.UUID) ([]a.UnitMember, error) {
	rows, err := q.AdminListUnitMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]a.UnitMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.UnitMember{UserID: r.ID.String(), FullName: r.FullName, MemberRole: string(r.MemberRole)})
	}
	return out, nil
}
func memberSnapshot(in []a.UnitMember) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, m := range in {
		out = append(out, map[string]string{"userId": m.UserID, "memberRole": m.MemberRole})
	}
	return out
}
func (s *Store) SetMembers(ctx context.Context, actor, unit string, in []a.Member) (out []a.UnitMember, err error) {
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := db.New(tx)
		if err := authorize(ctx, q, actor); err != nil {
			return err
		}
		// Lock the parent even for an empty replacement to serialize concurrent PUTs.
		id := uuid(unit)
		if _, err := q.AdminLockUnit(ctx, id); err != nil {
			return err
		}
		before, err := members(ctx, q, id)
		if err != nil {
			return err
		}
		if err = q.AdminDeleteUnitMembers(ctx, id); err != nil {
			return err
		}
		for _, m := range in {
			// FK validation also protects against concurrent deletion of a referenced user.
			if err = q.AdminInsertUnitMember(ctx, db.AdminInsertUnitMemberParams{UnitID: id, UserID: uuid(m.UserID), MemberRole: db.MembershipRole(m.MemberRole)}); err != nil {
				return err
			}
		}
		out, err = members(ctx, q, id)
		if err != nil {
			return err
		}
		return audit(ctx, q, actor, "UNIT", unit, "SET_UNIT_MEMBERS", memberSnapshot(before), memberSnapshot(out))
	})
	if err != nil {
		return nil, mapError(err)
	}
	return out, nil
}
func profile(ctx context.Context, q *db.Queries, u db.User) (auth.Profile, error) {
	rows, err := q.ListUserMemberships(ctx, u.ID)
	if err != nil {
		return auth.Profile{}, err
	}
	p := auth.Profile{User: auth.User{ID: u.ID.String(), Email: u.Email, FullName: u.FullName, StudentCode: u.StudentCode.String, IsAdmin: u.IsAdmin, IsActive: u.IsActive}, Memberships: make([]auth.Membership, 0, len(rows))}
	for _, m := range rows {
		p.Memberships = append(p.Memberships, auth.Membership{UnitID: m.UnitID.String(), Code: m.Code, Name: m.Name, MemberRole: string(m.MemberRole)})
	}
	return p, nil
}
func (s *Store) ListUsers(ctx context.Context, actor string, f a.Filter) (out a.UserList, err error) {
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Count, page, memberships and authorization share one consistent snapshot.
		if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
			return err
		}
		q := db.New(tx)
		if err := authorize(ctx, q, actor); err != nil {
			return err
		}
		total, err := q.AdminCountUsers(ctx, db.AdminCountUsersParams{Search: f.Q, RoleFilter: f.Role})
		if err != nil {
			return err
		}
		rows, err := q.AdminListUsers(ctx, db.AdminListUsersParams{Search: f.Q, RoleFilter: f.Role, PageSize: int32(f.PageSize), PageOffset: (f.Page - 1) * f.PageSize})
		if err != nil {
			return err
		}
		out = a.UserList{Items: make([]auth.Profile, 0, len(rows)), TotalItems: total}
		for _, u := range rows {
			p, err := profile(ctx, q, u)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, p)
		}
		return nil
	})
	if err != nil {
		return a.UserList{}, mapError(err)
	}
	return out, nil
}
func (s *Store) UpdateUser(ctx context.Context, actor, user string, active bool) (out auth.Profile, err error) {
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := db.New(tx)
		if err := authorize(ctx, q, actor); err != nil {
			return err
		}
		before, err := q.AdminLockUser(ctx, uuid(user))
		if err != nil {
			return err
		}
		row, err := q.AdminUpdateUserActive(ctx, db.AdminUpdateUserActiveParams{ID: before.ID, IsActive: active})
		if err != nil {
			return err
		}
		out, err = profile(ctx, q, row)
		if err != nil {
			return err
		}
		return audit(ctx, q, actor, "USER", user, "UPDATE_USER", map[string]bool{"isActive": before.IsActive}, map[string]bool{"isActive": row.IsActive})
	})
	if err != nil {
		return auth.Profile{}, mapError(err)
	}
	return out, nil
}
