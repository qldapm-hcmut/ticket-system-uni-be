package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
)

type repository struct{ queries *db.Queries }

var _ application.Repository = (*repository)(nil)

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	// Only user email/sub uniqueness conflicts are retryable login races.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "users_email_key" || pgErr.ConstraintName == "users_google_sub_key") {
		return application.ErrIdentityConflict
	}
	if err != nil {
		return fmt.Errorf("auth persistence: %w", err)
	}
	return nil
}

func userFromRow(row db.User, err error) (application.User, error) {
	if err != nil {
		return application.User{}, mapError(err)
	}
	return application.User{
		ID:          row.ID.String(),
		Email:       row.Email,
		FullName:    row.FullName,
		GoogleSub:   row.GoogleSub.String,
		StudentCode: row.StudentCode.String,
		IsAdmin:     row.IsAdmin,
		IsActive:    row.IsActive,
	}, nil
}

func uuid(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return id, fmt.Errorf("invalid auth user ID: %w", err)
	}
	return id, nil
}

func (r *repository) FindUserByID(ctx context.Context, id string) (application.User, error) {
	uid, err := uuid(id)
	if err != nil {
		return application.User{}, err
	}
	return userFromRow(r.queries.GetUserByID(ctx, uid))
}

func (r *repository) FindUserBySub(ctx context.Context, sub string) (application.User, error) {
	return userFromRow(r.queries.GetUserByGoogleSub(ctx, pgtype.Text{String: sub, Valid: true}))
}

func (r *repository) FindUserByEmail(ctx context.Context, email string) (application.User, error) {
	return userFromRow(r.queries.GetUserByEmail(ctx, email))
}

func (r *repository) CreateUser(ctx context.Context, email, name, sub string) (application.User, error) {
	return userFromRow(r.queries.CreateGoogleUser(ctx, db.CreateGoogleUserParams{
		Email:     email,
		FullName:  name,
		GoogleSub: pgtype.Text{String: sub, Valid: true},
	}))
}

func (r *repository) LinkIdentity(ctx context.Context, id, sub string) (application.User, error) {
	uid, err := uuid(id)
	if err != nil {
		return application.User{}, err
	}
	return userFromRow(r.queries.LinkGoogleIdentity(ctx, db.LinkGoogleIdentityParams{
		ID:        uid,
		GoogleSub: pgtype.Text{String: sub, Valid: true},
	}))
}

func sessionFromRow(row db.AuthSession, err error) (application.Session, error) {
	if err != nil {
		return application.Session{}, mapError(err)
	}
	return application.Session{
		ID:        row.ID.String(),
		UserID:    row.UserID.String(),
		ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

func (r *repository) CreateSession(ctx context.Context, id, hash string, expires time.Time) (application.Session, error) {
	uid, err := uuid(id)
	if err != nil {
		return application.Session{}, err
	}
	return sessionFromRow(r.queries.CreateAuthSession(ctx, db.CreateAuthSessionParams{
		UserID:           uid,
		RefreshTokenHash: hash,
		ExpiresAt:        pgtype.Timestamptz{Time: expires, Valid: true},
	}))
}

func (r *repository) ConsumeSession(ctx context.Context, hash string, now time.Time) (application.Session, error) {
	return sessionFromRow(r.queries.ConsumeAuthSession(ctx, db.ConsumeAuthSessionParams{
		TokenHash: hash,
		NowAt:     pgtype.Timestamptz{Time: now, Valid: true},
	}))
}

func (r *repository) RevokeSession(ctx context.Context, id, hash string, now time.Time) error {
	uid, err := uuid(id)
	if err != nil {
		return err
	}
	return mapError(r.queries.RevokeAuthSession(ctx, db.RevokeAuthSessionParams{
		UserID:    uid,
		TokenHash: hash,
		NowAt:     pgtype.Timestamptz{Time: now, Valid: true},
	}))
}
