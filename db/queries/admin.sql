-- name: AdminActor :one
SELECT is_admin FROM users WHERE id = $1 AND is_active = true;

-- name: AdminCreateUnit :one
INSERT INTO units (code, name, type) VALUES ($1, $2, $3) RETURNING *;

-- name: AdminLockUnit :one
SELECT * FROM units WHERE id = $1 FOR UPDATE;

-- name: AdminListUnitMembers :many
SELECT u.id, u.full_name, m.member_role
FROM unit_memberships m JOIN users u ON u.id = m.user_id
WHERE m.unit_id = $1 ORDER BY u.id, m.member_role;

-- name: AdminDeleteUnitMembers :exec
DELETE FROM unit_memberships WHERE unit_id = $1;

-- name: AdminInsertUnitMember :exec
INSERT INTO unit_memberships (unit_id, user_id, member_role) VALUES ($1, $2, $3);

-- name: AdminLockUser :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: AdminUpdateUserActive :one
UPDATE users SET is_active = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: AdminCountUsers :one
SELECT count(*) FROM users u
WHERE (sqlc.arg(search)::text = '' OR strpos(lower(u.full_name), lower(sqlc.arg(search))) > 0
       OR strpos(lower(u.email::text), lower(sqlc.arg(search))) > 0
       OR strpos(lower(coalesce(u.student_code, '')), lower(sqlc.arg(search))) > 0)
  AND (sqlc.arg(role_filter)::text = ''
       OR (sqlc.arg(role_filter) = 'STUDENT' AND u.student_code IS NOT NULL)
       OR (sqlc.arg(role_filter) = 'ADMIN' AND u.is_admin)
       OR (sqlc.arg(role_filter) = 'ORGANIZER' AND EXISTS (SELECT 1 FROM unit_memberships m WHERE m.user_id = u.id)));

-- name: AdminListUsers :many
SELECT u.* FROM users u
WHERE (sqlc.arg(search)::text = '' OR strpos(lower(u.full_name), lower(sqlc.arg(search))) > 0
       OR strpos(lower(u.email::text), lower(sqlc.arg(search))) > 0
       OR strpos(lower(coalesce(u.student_code, '')), lower(sqlc.arg(search))) > 0)
  AND (sqlc.arg(role_filter)::text = ''
       OR (sqlc.arg(role_filter) = 'STUDENT' AND u.student_code IS NOT NULL)
       OR (sqlc.arg(role_filter) = 'ADMIN' AND u.is_admin)
       OR (sqlc.arg(role_filter) = 'ORGANIZER' AND EXISTS (SELECT 1 FROM unit_memberships m WHERE m.user_id = u.id)))
ORDER BY u.full_name, u.id
LIMIT sqlc.arg(page_size)::int OFFSET sqlc.arg(page_offset)::bigint;

-- name: AdminInsertActivity :exec
INSERT INTO activity_logs (actor_id, entity_type, entity_id, action, metadata)
VALUES ($1, $2, $3, $4, $5);
