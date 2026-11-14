-- One active email consumer. No row locks or transaction are held during SMTP.
-- name: ListPendingEmails :many
SELECT n.id, u.email::text AS recipient, n.title, n.body
FROM notifications AS n
JOIN users AS u ON u.id = n.user_id
WHERE n.email_status = 'PENDING' AND n.email_attempts < 5
ORDER BY n.created_at, n.id
LIMIT $1;

-- email_attempts counts failed sends, not successful sends.
-- name: RecordEmailDelivery :execrows
UPDATE notifications
SET email_status = CASE
      WHEN sqlc.arg(sent)::boolean THEN 'SENT'::delivery_status
      WHEN email_attempts + 1 >= 5 THEN 'FAILED'::delivery_status
      ELSE 'PENDING'::delivery_status
    END,
    email_attempts = email_attempts + CASE WHEN sqlc.arg(sent)::boolean THEN 0 ELSE 1 END,
    last_error = CASE WHEN sqlc.arg(sent)::boolean THEN NULL ELSE 'Email delivery failed' END
WHERE id = sqlc.arg(id) AND email_status = 'PENDING' AND email_attempts < 5;
