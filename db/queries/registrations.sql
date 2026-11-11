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

-- name: GetCancellationDecisionTime :one
SELECT clock_timestamp()::timestamptz AS decision_at;

-- name: CancelRegistration :one
UPDATE registrations r SET status='CANCELLED',cancelled_at=sqlc.arg(decision_at)::timestamptz,
 cancelled_by=sqlc.arg(cancelled_by),cancelled_by_user=sqlc.arg(actor_id),
 cancel_reason=sqlc.narg(reason),cancellation_exception_until=NULL,updated_at=sqlc.arg(decision_at)
FROM events e
WHERE r.id=sqlc.arg(id) AND e.id=r.event_id
 AND r.status IN ('REGISTERED','WAITLISTED') AND sqlc.arg(decision_at)<e.start_at
 AND ((sqlc.arg(cancelled_by)::cancel_actor='ORGANIZER' AND r.student_id<>sqlc.arg(actor_id))
   OR (r.student_id=sqlc.arg(actor_id) AND
     (r.status='WAITLISTED' OR sqlc.arg(decision_at)<e.start_at-interval '24 hours' OR sqlc.arg(decision_at)<r.cancellation_exception_until)))
RETURNING r.*;

-- name: AuditRegistrationCancellation :execrows
INSERT INTO activity_logs(actor_id,event_id,entity_type,entity_id,action,metadata)
SELECT r.cancelled_by_user,r.event_id,'REGISTRATION',r.id,'CANCEL_ON_BEHALF',
 jsonb_build_object('reason',r.cancel_reason,'studentId',r.student_id)
FROM registrations r WHERE r.id=$1;

-- name: DecrementRegistrationCounter :exec
UPDATE events SET registered_count=registered_count-CASE WHEN sqlc.arg(was_registered)::boolean THEN 1 ELSE 0 END,
 waitlist_count=waitlist_count-CASE WHEN sqlc.arg(was_registered)::boolean THEN 0 ELSE 1 END,
 updated_at=now() WHERE id=sqlc.arg(event_id);

-- name: FindWaitlistHead :one
SELECT * FROM registrations WHERE event_id=$1 AND status='WAITLISTED' ORDER BY queue_seq LIMIT 1 FOR UPDATE;

-- name: PromoteRegistration :one
UPDATE registrations r SET status='REGISTERED',promoted_at=sqlc.arg(decision_at)::timestamptz,updated_at=sqlc.arg(decision_at)
FROM events e WHERE r.id=sqlc.arg(id) AND r.status='WAITLISTED' AND e.id=r.event_id
 AND sqlc.arg(decision_at)<e.start_at RETURNING r.*;

-- name: IncrementPromotedCounters :execrows
UPDATE events SET registered_count=registered_count+1,waitlist_count=waitlist_count-1,updated_at=now() WHERE id=$1 AND registered_count < capacity;

-- name: ListMyRegistrations :many
SELECT sqlc.embed(r),
 CASE WHEN r.status='WAITLISTED' AND e.start_at<=sqlc.arg(decision_at)::timestamptz
   THEN 'EXPIRED' ELSE r.status::text END::text AS effective_status,
 e.title AS event_title, e.location AS event_location, e.start_at AS event_start_at,
 e.end_at AS event_end_at, e.status AS event_status,
 -- Rank the entire live event queue, not just this owner or result page.
 COALESCE(CASE WHEN r.status='WAITLISTED' AND e.start_at>sqlc.arg(decision_at) THEN
   (SELECT count(*) FROM registrations w WHERE w.event_id=r.event_id AND w.status='WAITLISTED'
    AND (w.queue_seq,w.id)<=(r.queue_seq,r.id)) END,0)::bigint AS waitlist_position
FROM registrations r JOIN events e ON e.id=r.event_id
WHERE r.student_id=sqlc.arg(student_id)
 AND (NOT sqlc.arg(upcoming)::boolean OR e.end_at>sqlc.arg(decision_at))
 AND (sqlc.arg(status_filter)::text='' OR
  CASE WHEN r.status='WAITLISTED' AND e.start_at<=sqlc.arg(decision_at) THEN 'EXPIRED' ELSE r.status::text END=sqlc.arg(status_filter))
ORDER BY r.created_at DESC,r.id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountMyRegistrations :one
SELECT count(*) FROM registrations r JOIN events e ON e.id=r.event_id
WHERE r.student_id=sqlc.arg(student_id)
 AND (NOT sqlc.arg(upcoming)::boolean OR e.end_at>sqlc.arg(decision_at)::timestamptz)
 AND (sqlc.arg(status_filter)::text='' OR
  CASE WHEN r.status='WAITLISTED' AND e.start_at<=sqlc.arg(decision_at) THEN 'EXPIRED' ELSE r.status::text END=sqlc.arg(status_filter));

-- name: GetWaitlistPosition :one
SELECT count(*)::bigint FROM registrations w JOIN registrations r ON r.id=sqlc.arg(registration_id)
JOIN events e ON e.id=r.event_id
WHERE w.event_id=r.event_id AND w.status='WAITLISTED' AND r.status='WAITLISTED'
 AND now()<e.start_at AND (w.queue_seq,w.id)<=(r.queue_seq,r.id);

-- name: IsRegistrationWindowOpen :one
SELECT now() >= registration_opens_at AND now() < registration_closes_at AS is_open FROM events WHERE id=$1;
