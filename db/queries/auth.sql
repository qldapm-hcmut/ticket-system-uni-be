-- name: GetUserByGoogleSub :one
SELECT * FROM users WHERE google_sub = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: CreateGoogleUser :one
INSERT INTO users (email, full_name, google_sub)
VALUES ($1, $2, $3)
RETURNING *;

-- name: LinkGoogleIdentity :one
UPDATE users SET google_sub = $2, updated_at = now()
WHERE id = $1 AND google_sub IS NULL AND is_active = true
RETURNING *;

-- name: GetUserRoles :one
SELECT (u.student_code IS NOT NULL)::boolean AS is_student, u.is_admin,
       EXISTS (SELECT 1 FROM unit_memberships m WHERE m.user_id = u.id) AS is_organizer
FROM users u WHERE u.id = $1 AND u.is_active = true;

-- name: ListUserMemberships :many
SELECT m.unit_id, m.member_role, u.code, u.name, u.is_active
FROM unit_memberships m JOIN units u ON u.id = m.unit_id
WHERE m.user_id = $1
ORDER BY u.code;
