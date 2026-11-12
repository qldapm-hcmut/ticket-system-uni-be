-- name: RegisterStudent :one
WITH locked AS (
  SELECT e.* FROM events e WHERE e.id=$1 FOR UPDATE
), inserted AS (
  INSERT INTO registrations (event_id, student_id, status, checkin_code)
  SELECT $1, $2,
    CASE WHEN e.registered_count < e.capacity THEN 'REGISTERED'::registration_status ELSE 'WAITLISTED'::registration_status END,
    upper(substr(encode(gen_random_bytes(4),'hex'),1,8))
  FROM locked e
  WHERE e.status='PUBLISHED' AND now() >= e.registration_opens_at AND now() < e.registration_closes_at
    AND (e.registered_count < e.capacity OR e.waitlist_limit IS NULL OR e.waitlist_count < e.waitlist_limit)
  RETURNING *
), counted AS (
  UPDATE events e SET
    registered_count = e.registered_count + CASE WHEN i.status='REGISTERED' THEN 1 ELSE 0 END,
    waitlist_count = e.waitlist_count + CASE WHEN i.status='WAITLISTED' THEN 1 ELSE 0 END,
    updated_at=now()
  FROM inserted i WHERE e.id=i.event_id
)
SELECT inserted.* FROM inserted;

-- name: GetRegistration :one
SELECT * FROM registrations WHERE id=$1;

-- name: GetActiveRegistration :one
SELECT * FROM registrations WHERE event_id=$1 AND student_id=$2 AND status IN ('REGISTERED','WAITLISTED');

-- name: GetEventForRegistration :one
SELECT * FROM events WHERE id=$1 FOR UPDATE;

-- name: IncrementRegistered :exec
UPDATE events SET registered_count=registered_count+1, updated_at=now() WHERE id=$1;

-- name: IncrementWaitlist :exec
UPDATE events SET waitlist_count=waitlist_count+1, updated_at=now() WHERE id=$1;

-- name: CancelRegistration :one
UPDATE registrations r SET status='CANCELLED',cancelled_at=now(),cancelled_by=$2,cancelled_by_user=$3,
 cancellation_exception_until=NULL,updated_at=now()
FROM events e
WHERE r.id=$1 AND r.student_id=$3 AND e.id=r.event_id
 AND r.status IN ('REGISTERED','WAITLISTED') AND now()<e.start_at
 AND (r.status='WAITLISTED' OR now()<e.start_at-interval '24 hours' OR now()<r.cancellation_exception_until)
RETURNING r.*;

-- name: DecrementRegistrationCounter :exec
UPDATE events SET registered_count=registered_count-CASE WHEN sqlc.arg(was_registered)::boolean THEN 1 ELSE 0 END,
 waitlist_count=waitlist_count-CASE WHEN sqlc.arg(was_registered)::boolean THEN 0 ELSE 1 END,
 updated_at=now() WHERE id=sqlc.arg(event_id);

-- name: FindWaitlistHead :one
SELECT * FROM registrations WHERE event_id=$1 AND status='WAITLISTED' ORDER BY queue_seq LIMIT 1 FOR UPDATE;

-- name: PromoteRegistration :one
UPDATE registrations SET status='REGISTERED',promoted_at=now(),updated_at=now() WHERE id=$1 AND status='WAITLISTED' RETURNING *;

-- name: IncrementPromotedCounters :exec
UPDATE events SET registered_count=registered_count+1,waitlist_count=waitlist_count-1,updated_at=now() WHERE id=$1 AND registered_count < capacity;

-- name: ListMyRegistrations :many
SELECT sqlc.embed(r), e.title, e.location, e.start_at, e.end_at, e.status AS event_status,
       CASE WHEN r.status='WAITLISTED' THEN
         (SELECT count(*) FROM registrations w
          WHERE w.event_id=r.event_id AND w.status='WAITLISTED' AND w.queue_seq<=r.queue_seq)
       ELSE 0 END::bigint AS waitlist_position
FROM registrations r JOIN events e ON e.id=r.event_id
WHERE r.student_id=sqlc.arg(student_id)
  AND (sqlc.arg(status_filter)::text='' OR r.status::text=sqlc.arg(status_filter))
  AND (NOT sqlc.arg(upcoming)::boolean OR e.end_at>now())
ORDER BY r.created_at DESC, r.id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountMyRegistrations :one
SELECT count(*) FROM registrations r JOIN events e ON e.id=r.event_id
WHERE r.student_id=sqlc.arg(student_id)
  AND (sqlc.arg(status_filter)::text='' OR r.status::text=sqlc.arg(status_filter))
  AND (NOT sqlc.arg(upcoming)::boolean OR e.end_at>now());

-- name: IsRegistrationWindowOpen :one
SELECT now() >= registration_opens_at AND now() < registration_closes_at AS is_open FROM events WHERE id=$1;
