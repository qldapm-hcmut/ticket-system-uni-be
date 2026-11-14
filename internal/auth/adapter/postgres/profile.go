package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
)

type ProfileReader struct{ queries *db.Queries }

func NewProfileReader(pool *pgxpool.Pool) *ProfileReader {
	return &ProfileReader{queries: db.New(pool)}
}

// ReadProfile loads the current active user, then their memberships. These reads
// are not an atomic snapshot and do not share the login/refresh transaction.
func (r *ProfileReader) ReadProfile(ctx context.Context, id string) (application.Profile, error) {
	uid, err := uuid(id)
	if err != nil {
		return application.Profile{}, application.ErrUnauthenticated
	}
	row, err := r.queries.GetUserByID(ctx, uid)
	if err != nil {
		return application.Profile{}, mapError(err)
	}
	if !row.IsActive {
		return application.Profile{}, application.ErrUnauthenticated
	}
	user, err := userFromRow(row, nil)
	if err != nil {
		return application.Profile{}, err
	}
	memberships, err := r.queries.ListUserMemberships(ctx, uid)
	if err != nil {
		return application.Profile{}, mapError(err)
	}
	profile := application.Profile{User: user, Memberships: []application.Membership{}}
	for _, membership := range memberships {
		profile.Memberships = append(profile.Memberships, application.Membership{
			UnitID:     membership.UnitID.String(),
			Code:       membership.Code,
			Name:       membership.Name,
			MemberRole: string(membership.MemberRole),
		})
	}
	return profile, nil
}
