-- name: GetOwnedRegistrationView :one
SELECT r.id, r.event_id, r.status, r.checkin_code, r.checked_in_at, r.checkin_method, r.created_at,
       e.title AS event_title, e.location AS event_location, e.start_at AS event_start_at,
       e.end_at AS event_end_at, e.status AS event_status,
       -- 0 when the registration is not waitlisted; the caller decides by status.
       COALESCE(CASE WHEN r.status='WAITLISTED' THEN
          (SELECT count(*) FROM registrations w
           WHERE w.event_id=r.event_id AND w.status='WAITLISTED' AND w.queue_seq <= r.queue_seq)
        END, 0)::bigint AS waitlist_position
FROM registrations r JOIN events e ON e.id=r.event_id
WHERE r.id=sqlc.arg(registration_id) AND r.student_id=sqlc.arg(student_id);

-- name: GetMyActiveRegistrationRef :one
SELECT r.id, r.status,
       CASE WHEN r.status='WAITLISTED' THEN
         (SELECT count(*) FROM registrations w
          WHERE w.event_id=r.event_id AND w.status='WAITLISTED' AND w.queue_seq<=r.queue_seq)
       ELSE 0 END::bigint AS waitlist_position
FROM registrations r
WHERE r.event_id=sqlc.arg(event_id) AND r.student_id=sqlc.arg(student_id)
  AND r.status IN ('REGISTERED','WAITLISTED');
