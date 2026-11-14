-- name: SubmitEvent :one
UPDATE events
SET status = 'PENDING_APPROVAL',
    reject_reason = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = $1
  AND version = $2
  AND status IN ('DRAFT', 'REJECTED')
RETURNING *;

-- name: ApproveEvent :one
UPDATE events
SET status = 'PUBLISHED',
    approved_by = sqlc.arg(approved_by),
    approved_at = sqlc.arg(now_at),
    published_at = sqlc.arg(now_at),
    version = version + 1,
    updated_at = sqlc.arg(now_at)
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(version)
  AND status = 'PENDING_APPROVAL'
  AND created_by <> sqlc.arg(approved_by)
  AND start_at > sqlc.arg(now_at)::timestamptz
RETURNING *;

-- name: RejectEvent :one
UPDATE events
SET status = 'REJECTED',
    reject_reason = $2,
    approved_by = NULL,
    approved_at = NULL,
    version = version + 1,
    updated_at = now()
WHERE id = $1
  AND version = $3
  AND status = 'PENDING_APPROVAL'
RETURNING *;

-- name: ListPendingEvents :many
SELECT *
FROM events
WHERE status = 'PENDING_APPROVAL'
ORDER BY updated_at, id
LIMIT $1 OFFSET $2;

-- name: CountPendingEvents :one
SELECT count(*)
FROM events
WHERE status = 'PENDING_APPROVAL';

-- Creator is the sole Organizer recipient. This producer runs in the same
-- transaction as the decision and audit; delivery is handled after commit.
-- name: NotifyApprovalDecision :exec
INSERT INTO notifications (user_id, event_id, type, title, body)
SELECT e.created_by,
       e.id,
       sqlc.arg(notification_type)::notification_type,
       CASE WHEN sqlc.arg(notification_type)::notification_type = 'EVENT_APPROVED'
            THEN 'Sự kiện đã được duyệt'
            ELSE 'Sự kiện bị từ chối'
       END,
       sqlc.arg(body)::text
FROM events e
WHERE e.id = sqlc.arg(event_id);
