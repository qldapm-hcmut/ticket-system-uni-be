-- name: ListManagedEvents :many
SELECT e.* FROM events e
WHERE (sqlc.arg(is_admin)::boolean OR EXISTS(SELECT 1 FROM unit_memberships m JOIN units u ON u.id=m.unit_id AND u.is_active WHERE m.unit_id=e.unit_id AND m.user_id=sqlc.arg(actor_id)))
AND (sqlc.arg(unit_filter)::text='' OR e.unit_id::text=sqlc.arg(unit_filter))
AND (sqlc.arg(status_filter)::text='' OR e.status::text=ANY(string_to_array(sqlc.arg(status_filter),',')))
AND (sqlc.arg(search)::text='' OR strpos(lower(e.title),lower(sqlc.arg(search)::text))>0)
ORDER BY e.start_at,e.id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountManagedEvents :one
SELECT count(*) FROM events e
WHERE (sqlc.arg(is_admin)::boolean OR EXISTS(SELECT 1 FROM unit_memberships m JOIN units u ON u.id=m.unit_id AND u.is_active WHERE m.unit_id=e.unit_id AND m.user_id=sqlc.arg(actor_id)))
AND (sqlc.arg(unit_filter)::text='' OR e.unit_id::text=sqlc.arg(unit_filter))
AND (sqlc.arg(status_filter)::text='' OR e.status::text=ANY(string_to_array(sqlc.arg(status_filter),',')))
AND (sqlc.arg(search)::text='' OR strpos(lower(e.title),lower(sqlc.arg(search)::text))>0);

-- name: GetEventUnit :one
SELECT id,code,name FROM units WHERE id=$1;

-- name: GetEventCreator :one
SELECT id,full_name FROM users WHERE id=$1;

-- name: ListPublishedEvents :many
SELECT e.* FROM events e
WHERE e.status='PUBLISHED'
AND (sqlc.arg(unit_filter)::text='' OR e.unit_id::text=sqlc.arg(unit_filter))
AND (sqlc.arg(search)::text='' OR strpos(lower(e.title),lower(sqlc.arg(search)::text))>0)
AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.start_at>=sqlc.narg(from_at))
AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.start_at<=sqlc.narg(to_at))
AND (NOT sqlc.arg(available_only)::boolean OR (
 e.registration_opens_at<=sqlc.arg(now_at)::timestamptz AND sqlc.arg(now_at)<e.registration_closes_at
 AND (e.registered_count<e.capacity OR e.waitlist_limit IS NULL OR e.waitlist_count<e.waitlist_limit)))
ORDER BY e.start_at,e.id LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountPublishedEvents :one
SELECT count(*) FROM events e
WHERE e.status='PUBLISHED'
AND (sqlc.arg(unit_filter)::text='' OR e.unit_id::text=sqlc.arg(unit_filter))
AND (sqlc.arg(search)::text='' OR strpos(lower(e.title),lower(sqlc.arg(search)::text))>0)
AND (sqlc.narg(from_at)::timestamptz IS NULL OR e.start_at>=sqlc.narg(from_at))
AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.start_at<=sqlc.narg(to_at))
AND (NOT sqlc.arg(available_only)::boolean OR (
 e.registration_opens_at<=sqlc.arg(now_at)::timestamptz AND sqlc.arg(now_at)<e.registration_closes_at
 AND (e.registered_count<e.capacity OR e.waitlist_limit IS NULL OR e.waitlist_count<e.waitlist_limit)));


-- name: GetMyEventRegistration :one
-- Rank the full live waitlist before selecting this caller; cancelled history
-- and effectively expired WAITLISTED rows are not active registrations.
WITH active AS (
 SELECT r.id,r.student_id,r.status,
 CASE WHEN r.status='WAITLISTED' THEN row_number() OVER (PARTITION BY r.status ORDER BY r.queue_seq,r.id) END AS position
 FROM registrations r JOIN events e ON e.id=r.event_id
 WHERE r.event_id=sqlc.arg(event_id) AND (r.status='REGISTERED'
 OR (r.status='WAITLISTED' AND e.start_at>sqlc.arg(now_at)::timestamptz))
)
SELECT coalesce((SELECT jsonb_build_object('id',id,'status',status,'waitlistPosition',position)
 FROM active WHERE student_id=sqlc.arg(student_id)), 'null'::jsonb)::jsonb AS registration;
