-- name: GetOwnedRegistrationView :one
WITH decision AS MATERIALIZED (
    SELECT
        clock_timestamp() AS at
)
SELECT
    r.id,
    r.event_id,
    CASE
        WHEN r.status = 'WAITLISTED'
            AND e.start_at <= decision.at
            THEN 'EXPIRED'
        ELSE r.status::text
    END::text AS status,
    r.checkin_code,
    r.checked_in_at,
    r.checkin_method,
    r.created_at,
    e.title AS event_title,
    e.location AS event_location,
    e.start_at AS event_start_at,
    e.end_at AS event_end_at,
    e.status AS event_status,
    -- No rank once the waitlist has effectively expired.
    COALESCE(
        CASE
            WHEN r.status = 'WAITLISTED'
                AND e.start_at > decision.at
                THEN (
                    SELECT
                        count(*)
                    FROM registrations w
                    WHERE w.event_id = r.event_id
                        AND w.status = 'WAITLISTED'
                        AND (w.queue_seq, w.id) <= (r.queue_seq, r.id)
                )
        END,
        0
    )::bigint AS waitlist_position
FROM registrations r
JOIN events e ON e.id = r.event_id
CROSS JOIN decision
WHERE r.id = sqlc.arg(registration_id)
    AND r.student_id = sqlc.arg(student_id);
