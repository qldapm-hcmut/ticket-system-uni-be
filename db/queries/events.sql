-- name: CreateDraftEvent :one
INSERT INTO events (unit_id,created_by,title,description,location,start_at,end_at,registration_opens_at,registration_closes_at,capacity,waitlist_limit)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;

-- name: GetEventForUpdate :one
SELECT * FROM events WHERE id=$1 FOR UPDATE;

-- name: GetEvent :one
SELECT * FROM events WHERE id=$1;

-- name: UpdateDraftEvent :one
UPDATE events SET title=$2,description=$3,location=$4,start_at=$5,end_at=$6,registration_opens_at=$7,registration_closes_at=$8,capacity=$9,waitlist_limit=$10,version=version+1,updated_at=now()
WHERE id=$1 AND version=$11 AND status IN ('DRAFT','REJECTED','PUBLISHED') RETURNING *;

-- name: DeleteDraftEvent :execrows
DELETE FROM events WHERE id=$1 AND status='DRAFT';

-- name: IsUnitOrganizer :one
SELECT EXISTS(SELECT 1 FROM unit_memberships m JOIN units u ON u.id=m.unit_id WHERE m.user_id=$1 AND m.unit_id=$2 AND u.is_active=true) AS allowed;

-- name: ListActiveUnits :many
SELECT * FROM units WHERE is_active=true AND (sqlc.arg(kind)::text='' OR type::text=sqlc.arg(kind)::text) ORDER BY code;
