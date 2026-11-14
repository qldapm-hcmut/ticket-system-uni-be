-- Internal dev-operated identity enrichment and privilege operations only.
-- Every mutation and its audit must use the same transaction.

-- name: OpsInsertUser :one
INSERT INTO users (email, full_name, student_code, is_admin)
VALUES (sqlc.arg(email), sqlc.arg(full_name), sqlc.narg(student_code), sqlc.arg(is_admin))
ON CONFLICT (email) DO NOTHING
RETURNING *;

-- name: OpsLockUserByEmail :one
SELECT * FROM users WHERE email = $1 FOR UPDATE;

-- name: OpsLockUserByID :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: OpsSetStudentCode :exec
UPDATE users SET student_code = $2, updated_at = now() WHERE id = $1;

-- name: OpsSetAdmin :exec
UPDATE users SET is_admin = $2, updated_at = now() WHERE id = $1;

-- name: OpsInsertActivity :exec
INSERT INTO activity_logs (entity_type, entity_id, action, metadata)
VALUES ('USER', $1, $2, $3);
