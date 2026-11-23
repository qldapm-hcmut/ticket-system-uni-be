-- name: NotifyRegistration :exec
INSERT INTO notifications(user_id,event_id,type,title,body)
SELECT student_id,event_id,sqlc.arg(kind)::notification_type,
       sqlc.arg(title)::text,sqlc.arg(body)::text
FROM registrations r WHERE r.id=sqlc.arg(registration_id);

-- Promotion and counters only. PROMOTED notifications are written separately
-- so a notification failure cannot undo the promotion (Q14).
-- name: PromoteWaitlistForCapacity :many
WITH head AS (
 SELECT r.id FROM registrations r JOIN events e ON e.id=r.event_id
 WHERE r.event_id=sqlc.arg(event_id) AND r.status='WAITLISTED'
 AND e.status='PUBLISHED' AND now()<e.start_at
 ORDER BY r.queue_seq LIMIT sqlc.arg(seats)::integer FOR UPDATE OF r
), promoted AS (
 UPDATE registrations r SET status='REGISTERED',promoted_at=now(),updated_at=now()
 FROM head WHERE r.id=head.id RETURNING r.*
), counted AS (
 UPDATE events SET registered_count=registered_count+(SELECT count(*) FROM promoted),
 waitlist_count=waitlist_count-(SELECT count(*) FROM promoted),updated_at=now()
 WHERE id=sqlc.arg(event_id)
)
SELECT id FROM promoted;

-- name: NotifyPromotedRegistrations :exec
INSERT INTO notifications(user_id,event_id,type,title,body)
SELECT student_id,event_id,'PROMOTED','Registration promoted','A seat is now available for your registration.'
FROM registrations WHERE id = ANY(sqlc.arg(registration_ids)::uuid[]);
