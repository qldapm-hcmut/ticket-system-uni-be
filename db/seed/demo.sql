-- Synthetic identities for a local database. Existing accounts keep their permissions.
BEGIN;

INSERT INTO units (code, name, type)
VALUES ('DEMO', 'Demo Event Unit', 'FACULTY')
ON CONFLICT (code) DO NOTHING;

INSERT INTO users (email, full_name, student_code, is_admin)
VALUES
  ('admin.demo@hcmut.edu.vn', 'Demo Admin', NULL, true),
  ('organizer.demo@hcmut.edu.vn', 'Demo Organizer', NULL, false),
  ('student.demo@hcmut.edu.vn', 'Demo Student', 'DEMO0001', false)
ON CONFLICT (email) DO NOTHING;

INSERT INTO unit_memberships (user_id, unit_id, member_role)
SELECT u.id, un.id, 'ORGANIZER'
FROM users u CROSS JOIN units un
WHERE u.email = 'organizer.demo@hcmut.edu.vn' AND un.code = 'DEMO'
ON CONFLICT DO NOTHING;

COMMIT;
