-- Legacy psql bootstrap. Prefer cmd/ops for validated internal operations.
-- Required variables: admin_email, operator (verified internal ID), ticket.
\set ON_ERROR_STOP on
\if :{?admin_email}
\else
  \echo 'admin_email is required'
  \quit 1
\endif
\if :{?operator}
\else
  \echo 'operator is required'
  \quit 1
\endif
\if :{?ticket}
\else
  \echo 'ticket is required'
  \quit 1
\endif
BEGIN;
-- Validate without exposing supplied identities through constraint errors.
SELECT set_config('ops.admin_email', :'admin_email', true),
       set_config('ops.operator', :'operator', true),
       set_config('ops.ticket', :'ticket', true) \gset
DO $$
BEGIN
  IF current_setting('ops.admin_email') !~ '^[A-Za-z0-9.!#$%&''*+/=?^_`{|}~-]+@hcmut[.]edu[.]vn$'
     OR btrim(current_setting('ops.operator')) = ''
     OR length(current_setting('ops.operator')) > 200
     OR current_setting('ops.operator') ~ E'[\r\n]'
     OR btrim(current_setting('ops.ticket')) = ''
     OR length(current_setting('ops.ticket')) > 200
     OR current_setting('ops.ticket') ~ E'[\r\n]' THEN
    RAISE EXCEPTION 'invalid bootstrap input';
  END IF;
END $$;

INSERT INTO units (code, name, type)
VALUES ('CSE', 'Khoa Khoa học và Kỹ thuật Máy tính', 'FACULTY')
ON CONFLICT (code) DO NOTHING;

-- Audit only the identity actually inserted by this execution.
WITH inserted AS (
  INSERT INTO users (email, full_name, is_admin)
  VALUES (:'admin_email', 'Bootstrap Admin', true)
  ON CONFLICT (email) DO NOTHING
  RETURNING id
)
INSERT INTO activity_logs (entity_type, entity_id, action, metadata)
SELECT 'USER', id, 'GRANT_ADMIN',
       jsonb_build_object('source', 'seed', 'operator', :'operator',
                          'ticket', :'ticket', 'before', NULL,
                          'after', jsonb_build_object('isAdmin', true))
FROM inserted;

-- A revoked/existing non-Admin must go through explicit audited grant.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users
                 WHERE email = current_setting('ops.admin_email')::citext
                   AND is_admin = true) THEN
    RAISE EXCEPTION 'account exists without Admin; use ops admin grant';
  END IF;
END $$;
COMMIT;
