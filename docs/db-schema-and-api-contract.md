# Event Registration System — DB Schema & API Contract

Oct 18, 2026 · baseline cập nhật 24/10/2026 · API 1.1.0 · @Minh Phan

## Nguồn chuẩn trong repository

- API máy đọc: [`../api/openapi.yaml`](../api/openapi.yaml).
- Schema thực thi: [`../db/migrations/0001_initial_schema.up.sql`](../db/migrations/0001_initial_schema.up.sql), kèm migration down. DDL bên dưới giải thích cùng schema; thay đổi phải đồng bộ.
- Hướng dẫn triển khai: [`development.md`](development.md); index cho agent: [`AGENTS.md`](AGENTS.md); trạng thái code thực tế: [`../README.md`](../README.md).
- Đây là baseline thiết kế, không phải xác nhận mọi tính năng đã được triển khai. Không giữ các bản sao có hậu tố tải xuống.


## 1. Tổng quan & giả định

Hệ thống dùng PostgreSQL và REST/JSON. Mọi bất biến nghiệp vụ đã chốt với thầy (không vượt chỗ, không đăng ký trùng, waitlist FIFO, không check-in trùng) được khóa ở tầng database bằng constraint và transaction, không chỉ dựa vào code ứng dụng.

| Quy tắc đã chốt (nguồn) | Hiện thực trong thiết kế |
| --- | --- |
| Giới hạn chỗ + waitlist, tự chuyển người đầu waitlist (Q3, Q14) | `events.registered_count <= capacity` (CHECK); promote trong cùng transaction với lệnh hủy, chọn dòng WAITLISTED có `queue_seq` nhỏ nhất |
| Gửi email lỗi không ảnh hưởng đăng ký (Q14) | Thay đổi trạng thái và INSERT notification/outbox commit trong cùng transaction; lỗi INSERT rollback toàn bộ. Worker gửi email riêng và retry sau commit |
| Mỗi sinh viên 1 đăng ký còn hiệu lực; được đăng ký lại sau khi hủy (Q15) | Partial unique index `(event_id, student_id) WHERE status IN ('REGISTERED','WAITLISTED')`; đăng ký lại = dòng mới, vào cuối waitlist |
| Tự hủy đến 24h trước giờ bắt đầu (Q13) | Hạn bình thường `start_at - 24h`; trong 24h cuối, nếu Organizer đổi thời gian/địa điểm thì các đăng ký REGISTERED hiện hữu được mở ngoại lệ tự hủy tới trước `start_at` mới; Organizer vẫn hủy hộ được |
| Duyệt trước khi công bố, Organizer không tự duyệt (Q4, Q12) | Máy trạng thái Event; `approve` kiểm tra `approver_id <> created_by`; user có `is_admin = true` đảm nhận duyệt |
| Sửa/hủy sau công bố (Q19) | Sửa không cần duyệt lại; đổi thời gian/địa điểm → ghi `activity_logs` (`UPDATE_EVENT`, `isSignificant`) + notify; hủy → hủy toàn bộ registration và waitlist + notify |
| QR + nhập mã + xác nhận tay, không check-out (Q2) | Mỗi registration có `checkin_code` 8 ký tự và QR token ký HMAC; check-in ghi vào cột `checked_in_at` của chính registration, chỉ ghi được khi cột còn NULL |
| Tài khoản trường qua Google OAuth 2.0 / OpenID Connect (phạm vi cập nhật) | Backend xác minh Google ID token, email_verified và domain/hd hcmut.edu.vn; liên kết bằng Google sub, cấp JWT nội bộ |
| Báo cáo + xuất Excel/CSV, không tích hợp ĐRL (Q6) | Endpoint summary + export CSV (Excel đọc được); XLSX để giai đoạn sau |
| Phân quyền theo đơn vị (Q20) | `unit_memberships` gắn Organizer với đơn vị; Admin là quản trị viên cấp trường và có quyền xem/duyệt toàn trường, nhưng KHÔNG check-in |

**Giả định của nhóm** (ghi rõ là assumption trong báo cáo, theo Q7):

- Thời gian lưu `timestamptz` (UTC), hiển thị theo Asia/Ho\_Chi\_Minh.
- Quy mô thiết kế: tối đa 50 sự kiện/tháng, 1.000 chỗ/sự kiện, 200 lượt check-in/phút lúc cao điểm.
- Mô hình check-in: thiết bị của BTC quét QR trên điện thoại sinh viên. Sinh viên không cần mạng tại hội trường vì QR đã có sẵn trong trang "Đăng ký của tôi".
- Cửa sổ check-in: từ 60 phút trước `start_at` đến `end_at`.
- Không được giảm `capacity` xuống dưới số đã đăng ký; tăng `capacity` → tự promote từ waitlist.
- Waitlist hết hiệu lực (EXPIRED) ngay khi `now >= start_at`, không phụ thuộc lịch worker. API trả trạng thái hiệu lực EXPIRED cho dòng WAITLISTED quá hạn, không promote và không tính vào waitlist còn hiệu lực (kể cả Event.waitlistCount và báo cáo). Mọi thao tác đổi chỗ chuẩn hóa các dòng quá hạn và bộ đếm trong transaction sau khi khóa events. Worker cũng khóa events, chuyển WAITLISTED → EXPIRED và đặt waitlist_count = 0 trong cùng transaction.
- Phạm vi Organizer (diễn giải "sự kiện do mình quản lý" ở Q20): mọi sự kiện thuộc đơn vị mà Organizer được gán vai trò ORGANIZER, không chỉ sự kiện do chính họ tạo. Lý do: ngày sự kiện cần nhiều người cùng check-in và có người làm thay khi người tạo vắng. Admin chỉ gán Organizer cho người thật sự cần để giới hạn phạm vi xem dữ liệu cá nhân.
- Sinh viên được promote từ waitlist trong 24h cuối vẫn chịu quy tắc hủy thông thường. WAITLISTED không được cấp hoặc giữ ngoại lệ; promote không tự sinh ngoại lệ. Nếu một thay đổi quan trọng xảy ra sau khi promote, họ được xét như các REGISTERED hiện hữu khác.
- Ngoại lệ Q13–Q19 (quyết định nhóm): nếu Organizer đổi `start_at`, `end_at` hoặc `location` trong 24h trước `start_at` hiện tại, thông báo người đang REGISTERED/WAITLISTED và mở quyền tự hủy cho những đăng ký REGISTERED hiện hữu đó tới trước giờ bắt đầu mới (`cancellation_exception_until = new start_at`); người đăng ký sau thay đổi không được hưởng. Đổi chỉ `end_at` cũng được coi là thay đổi thời gian quan trọng. Ngoại lệ được thu hồi khi hết hạn, hủy sự kiện hoặc registration không còn REGISTERED. Nếu dời ngày tới mốc còn trên 24h, quy tắc hủy thông thường theo `start_at` mới tự áp dụng.
- Từ 60 phút trước `start_at` đến hết sự kiện, khóa cả `start_at`, `end_at` và `location` của PUBLISHED; trước thời điểm này được sửa theo Q19. Hủy toàn bộ sự kiện vẫn theo E7.
- Hệ thống single-tenant. Admin là quản trị viên cấp trường (`is_admin = true`); vai trò Admin tự nó không cho quyền check-in. Cấp `is_admin` chỉ qua quy trình nội bộ do đội dev xác minh và ghi audit, không có API công khai tự nâng quyền. Admin đầu tiên được tạo bằng seed. Việc Admin xem toàn trường là quyết định nhóm mở rộng Q20 (thầy trước đó đồng ý phạm vi theo đơn vị).
- **Vai trò người dùng là chuyên biệt hóa chồng lấn** (EERD: USER ⊃ STUDENT, ORGANIZER, ADMIN, ký hiệu "o", tham gia một phần). Một người có thể giữ nhiều vai trò cùng lúc, điển hình là Organizer của CLB đồng thời là sinh viên. Vai trò không lưu bằng một cột mà suy ra từ dữ liệu: STUDENT ⇔ `student_code IS NOT NULL`; ORGANIZER ⇔ có dòng trong `unit_memberships` (nguồn sự thật duy nhất); ADMIN ⇔ `is_admin = true`. Quyền của một người là **hợp** quyền các vai trò họ đang có.
- MSSV (`student_code`) không lấy được từ Google. Danh sách sinh viên (email trường, họ tên, MSSV) được đội dev import thành tài khoản bootstrap chưa có `google_sub`; lần đăng nhập đầu liên kết theo email đã xác minh (cơ chế ở A1). Tài khoản đăng nhập mà không có trong danh sách thì không có MSSV, nên chưa phải STUDENT và không đăng ký sự kiện được (`403 FORBIDDEN`) cho tới khi được bổ sung.
- Organizer kiêm sinh viên (quyết định nhóm 24/10, cần TV1 xác nhận): được đăng ký mọi sự kiện, kể cả của đơn vị mình; khi tự hủy đăng ký của chính mình thì áp dụng luật hủy của sinh viên (Q13), không dùng quyền hủy hộ; **không được tự check-in cho mình** (`chk_no_self_checkin`, cùng tinh thần Q12 không tự duyệt).

Google OAuth 2.0 / OpenID Connect nằm trong phạm vi cập nhật, thay cho đăng nhập mô phỏng; stack backend Go 1.27+, Echo v4, PostgreSQL 16. Ngoài phạm vi: check-out, SSO khác, app native, tích hợp điểm rèn luyện, đồng bộ offline, multi-tenant (hệ thống chỉ phục vụ một trường).

## 2. Mô hình dữ liệu (PostgreSQL)

Schema gồm 8 bảng xoay quanh `events` và `registrations`. Một sinh viên có thể có nhiều dòng `registrations` cho cùng sự kiện (lịch sử hủy/đăng ký lại), nhưng tối đa một dòng còn hiệu lực nhờ partial unique index; check-in là các cột của chính dòng đó.

| Bảng | Vai trò |
| --- | --- |
| `users` | Tài khoản trường liên kết Google; MSSV (xác định vai trò STUDENT) và cờ `is_admin` |
| `units` | Đơn vị tổ chức: Khoa, CLB/Đội/Nhóm, phòng ban |
| `unit_memberships` | Gắn Organizer với đơn vị, và là nguồn sự thật duy nhất cho vai trò ORGANIZER. Admin cấp trường không cần membership để duyệt/xem; chỉ Organizer mới check-in |
| `events` | Sự kiện, sức chứa, bộ đếm chỗ, trạng thái duyệt, lý do từ chối |
| `registrations` | Đăng ký + waitlist (phân biệt bằng `status`) + thông tin check-in |
| `notifications` | Thông báo in-app + outbox gửi email |
| `auth_sessions` | Refresh token nội bộ (chỉ lưu hash), độc lập với Google |
| `activity_logs` | Lịch sử duyệt, lịch sử sửa sự kiện và thao tác nhạy cảm (export, check-in tay, hủy hộ) |

Thiết kế rút gọn từ 11 bảng ban đầu; không chức năng nào bị bỏ:

| Bảng ban đầu | Gộp vào | Lý do | Trade-off chấp nhận |
| --- | --- | --- | --- |
| `check_ins` | Cột `checked_in_*` của `registrations` | Quan hệ 1–0..1: mỗi đăng ký check-in tối đa một lần | Hoàn tác check-in chỉ xóa giá trị cột, không giữ dòng riêng (lịch sử hoàn tác nằm trong `activity_logs`) |
| `event_approval_logs` | `activity_logs` | Cùng cấu trúc: ai, làm gì, lúc nào, trên đối tượng nào | Lý do duyệt/từ chối nằm trong `metadata`; lý do từ chối gần nhất lưu thêm ở `events.reject_reason` |
| `event_change_logs` | `activity_logs` | Chỉ ghi và đọc lại để hiển thị | Danh sách thay đổi cũ/mới nằm trong `metadata` |
| `audit_logs` | `activity_logs` | Dùng chung một cơ chế truy vết | `entity_id` không có FK; bù lại bằng cột `event_id` có FK thật |

### 2.1 ERD

&#91;embedded content: ERD · 8 bảng, ký hiệu crow's foot\]

USERS và UNITS quan hệ nhiều-nhiều qua UNIT\_MEMBERSHIPS; USERS còn nối EVENTS hai lần (người tạo, người duyệt), hình chỉ vẽ quan hệ người tạo để đỡ rối.

Repository hiện không lưu file sơ đồ drawio; schema thực thi chuẩn nằm trong `db/migrations/`. Các sơ đồ ERD/EERD và mapping được quản lý ngoài repository, không phải tài liệu có sẵn trong repo. Điểm khác chính so với ERD bảng: USER được chuyên biệt hóa chồng lấn thành STUDENT, ORGANIZER, ADMIN; mỗi liên kết gắn vào đúng lớp con (Đăng ký ← STUDENT; Quản lý, Tạo, Điểm danh ← ORGANIZER; Duyệt ← ADMIN).

### 2.2 Mapping ERD → schema

Mỗi thực thể thành một bảng `snake_case` số nhiều với khóa `uuid`; quan hệ 1–N thành FK ở phía N, N–N thành bảng trung gian, 1–0..1 gộp thành cột nullable của bảng phía 1. Không xóa cứng dữ liệu nghiệp vụ (dùng `status`, `is_active`), nên mọi FK để mặc định `ON DELETE NO ACTION`.

| Quan hệ trong ERD | Bản số | Hiện thực trong schema |
| --- | --- | --- |
| USER ⊃ STUDENT, ORGANIZER, ADMIN | Chuyên biệt hóa chồng lấn, một phần | Phương án 8D: một bảng `users`, vai trò suy ra từ `student_code`, `is_admin` và `unit_memberships` (không có cột `role`); FK trỏ tới lớp con (vd `created_by`, `checked_in_by`, `student_id`) đều trỏ `users.id`, việc đúng lớp con kiểm tra ở tầng ứng dụng |
| USERS — UNITS qua UNIT\_MEMBERSHIPS | N–N | Bảng trung gian, PK `(user_id, unit_id, member_role)`; `member_role` chỉ gồm ORGANIZER; Admin không cần membership |
| UNITS — EVENTS | 1–N | `events.unit_id` NOT NULL → `units.id` |
| USERS — EVENTS (tạo) | 1–N | `events.created_by` NOT NULL → `users.id` |
| USERS — EVENTS (duyệt) | 1–0..N | `events.approved_by` NULL → `users.id`; `chk_no_self_approve` |
| EVENTS — REGISTRATIONS | 1–N | `registrations.event_id`; partial UNIQUE `(event_id, student_id)` khi còn hiệu lực |
| USERS — REGISTRATIONS (đăng ký) | 1–N | `registrations.student_id` NOT NULL → `users.id` |
| USERS — REGISTRATIONS (người check-in) | 1–0..N | `registrations.checked_in_by` NULL → `users.id`; `chk_no_self_checkin` |
| EVENTS — ACTIVITY\_LOGS | 1–0..N | `activity_logs.event_id` NULL → `events.id`; NULL với log không gắn sự kiện (khóa/mở user, gán đơn vị) |
| USERS — ACTIVITY\_LOGS | 1–0..N | `activity_logs.actor_id` NULL → `users.id` (job hệ thống không có actor) |
| Đối tượng bất kỳ — ACTIVITY\_LOGS | 1–0..N | `entity_type` + `entity_id`, không FK (quan hệ đa hình) |
| USERS — NOTIFICATIONS, EVENTS — NOTIFICATIONS | 1–N, 1–0..N | `user_id` NOT NULL, `event_id` NULL |
| USERS — AUTH\_SESSIONS | 1–N | `auth_sessions.user_id` NOT NULL |

| Thuộc tính đặc biệt | Cách map |
| --- | --- |
| Check-in (thực thể yếu 1–0..1 của đăng ký) | 4 cột nullable `checked_in_at`, `checkin_method`, `checked_in_by`, `checkin_note`; CHECK buộc 3 cột đầu cùng NULL hoặc cùng có giá trị |
| Thuộc tính dẫn xuất `registered_count`, `waitlist_count` | Lưu thành cột bộ đếm, cập nhật trong transaction, để CHECK chống vượt chỗ |
| `waitlistPosition` | Không lưu; tính khi đọc từ `queue_seq` |
| Chi tiết từng loại log (lý do, thay đổi cũ/mới, cờ quan trọng) | Một cột `metadata jsonb` thay cho các cột riêng của 3 bảng log cũ |
| Miền giá trị cố định (status, method, loại đơn vị…) | PostgreSQL `ENUM`; vai trò người dùng không dùng enum vì chồng lấn; `activity_logs.action` để `varchar` vì danh sách action còn mở rộng |

### 2.3 DDL PostgreSQL

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;

-- ========== ENUMS ==========
-- Vai trò người dùng KHÔNG lưu bằng enum: USER chuyên biệt hóa chồng lấn (EERD, phương án ánh xạ 8D)
--   STUDENT   ⇔ users.student_code IS NOT NULL
--   ORGANIZER ⇔ có dòng trong unit_memberships
--   ADMIN     ⇔ users.is_admin = true
CREATE TYPE unit_type           AS ENUM ('FACULTY','CLUB','DEPARTMENT');
CREATE TYPE membership_role     AS ENUM ('ORGANIZER');
CREATE TYPE event_status        AS ENUM ('DRAFT','PENDING_APPROVAL','REJECTED',
                                         'PUBLISHED','CANCELLED','COMPLETED');
CREATE TYPE registration_status AS ENUM ('REGISTERED','WAITLISTED','CANCELLED',
                                         'CANCELLED_BY_EVENT','EXPIRED');
CREATE TYPE cancel_actor        AS ENUM ('STUDENT','ORGANIZER','SYSTEM');
CREATE TYPE checkin_method      AS ENUM ('QR','CODE','MANUAL');
CREATE TYPE notification_type   AS ENUM ('REGISTERED','WAITLISTED','PROMOTED','REG_CANCELLED',
                                         'EVENT_CHANGED','EVENT_CANCELLED',
                                         'EVENT_APPROVED','EVENT_REJECTED','REMINDER');
CREATE TYPE delivery_status     AS ENUM ('PENDING','SENT','FAILED');

-- ========== 1. USERS ==========
CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         citext NOT NULL UNIQUE CHECK (email LIKE '%@hcmut.edu.vn'),
  full_name     varchar(120) NOT NULL,
  student_code  varchar(10) UNIQUE,              -- MSSV; NOT NULL ⇔ là STUDENT; NULL với cán bộ
  is_admin      boolean NOT NULL DEFAULT false,  -- chỉ đội dev cấp qua quy trình nội bộ
  google_sub    text UNIQUE,                     -- Google sub; NULL chỉ với tài khoản bootstrap chưa liên kết
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ========== 2. UNITS ==========
CREATE TABLE units (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code       varchar(30) NOT NULL UNIQUE,        -- vd: CSE, CLB-GDSC, P.CTSV
  name       varchar(200) NOT NULL,
  type       unit_type NOT NULL,
  is_active  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ========== 3. UNIT_MEMBERSHIPS ==========
CREATE TABLE unit_memberships (
  user_id     uuid NOT NULL REFERENCES users(id),
  unit_id     uuid NOT NULL REFERENCES units(id),
  member_role membership_role NOT NULL,          -- chỉ ORGANIZER; Admin cấp trường không cần membership
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, unit_id, member_role)
);
CREATE INDEX idx_membership_unit ON unit_memberships (unit_id, member_role);

-- ========== 4. EVENTS ==========
CREATE TABLE events (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  unit_id                uuid NOT NULL REFERENCES units(id),
  created_by             uuid NOT NULL REFERENCES users(id),
  title                  varchar(200) NOT NULL,
  description            text,
  location               varchar(255) NOT NULL,
  start_at               timestamptz NOT NULL,
  end_at                 timestamptz NOT NULL,
  registration_opens_at  timestamptz NOT NULL,
  registration_closes_at timestamptz NOT NULL,
  capacity               integer NOT NULL CHECK (capacity > 0),
  waitlist_limit         integer CHECK (waitlist_limit >= 0),  -- NULL = không giới hạn
  registered_count       integer NOT NULL DEFAULT 0,          -- bộ đếm, cập nhật trong transaction
  waitlist_count         integer NOT NULL DEFAULT 0,
  status                 event_status NOT NULL DEFAULT 'DRAFT',
  approved_by            uuid REFERENCES users(id),
  approved_at            timestamptz,
  reject_reason          text,                                -- lý do từ chối gần nhất, hiển thị cho Organizer
  published_at           timestamptz,
  cancelled_at           timestamptz,
  cancel_reason          text,
  version                integer NOT NULL DEFAULT 1,          -- optimistic lock cho PATCH
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT chk_event_time      CHECK (end_at > start_at),
  CONSTRAINT chk_reg_window      CHECK (registration_opens_at < registration_closes_at
                                        AND registration_closes_at <= start_at),
  CONSTRAINT chk_capacity        CHECK (registered_count BETWEEN 0 AND capacity),   -- chống overbooking
  CONSTRAINT chk_waitlist        CHECK (waitlist_count >= 0 AND
                                        (waitlist_limit IS NULL OR waitlist_count <= waitlist_limit)),
  CONSTRAINT chk_no_self_approve CHECK (approved_by IS NULL OR approved_by <> created_by)  -- Q12
);
CREATE INDEX idx_events_catalog     ON events (start_at) WHERE status = 'PUBLISHED';
CREATE INDEX idx_events_unit_status ON events (unit_id, status, start_at);

-- ========== 5. REGISTRATIONS (đăng ký + waitlist + check-in) ==========
CREATE TABLE registrations (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id          uuid NOT NULL REFERENCES events(id),
  student_id        uuid NOT NULL REFERENCES users(id),
  status            registration_status NOT NULL,
  queue_seq         bigint GENERATED ALWAYS AS IDENTITY,  -- thứ tự vào hàng, dùng cho FIFO
  promoted_at       timestamptz,                         -- != NULL nếu đi lên từ waitlist
  cancelled_at      timestamptz,
  cancellation_exception_until timestamptz,             -- ngoại lệ Q13–Q19, chỉ cấp cho đăng ký có trước thay đổi
  cancelled_by      cancel_actor,
  cancelled_by_user uuid REFERENCES users(id),
  cancel_reason     varchar(255),
  checkin_code      char(8) NOT NULL,                    -- mã chuẩn hóa 8 ký tự; API format XXXX-XXXX
  -- check-in (gộp từ bảng check_ins cũ)
  checked_in_at     timestamptz,
  checkin_method    checkin_method,
  checked_in_by     uuid REFERENCES users(id),
  checkin_note      varchar(255),                        -- bắt buộc khi MANUAL
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_checkin_code UNIQUE (event_id, checkin_code),
  CONSTRAINT chk_cancel_exception CHECK (cancellation_exception_until IS NULL OR status = 'REGISTERED'),
  CONSTRAINT chk_cancel_fields CHECK (
    (status IN ('CANCELLED','CANCELLED_BY_EVENT')) = (cancelled_at IS NOT NULL)),
  CONSTRAINT chk_checkin_fields CHECK (
    (checked_in_at IS NULL) = (checkin_method IS NULL) AND
    (checked_in_at IS NULL) = (checked_in_by  IS NULL)),
  CONSTRAINT chk_manual_note CHECK (
    checkin_method IS DISTINCT FROM 'MANUAL' OR checkin_note IS NOT NULL),
  CONSTRAINT chk_no_self_checkin CHECK (checked_in_by IS NULL OR checked_in_by <> student_id)  -- Organizer kiêm sinh viên
);
-- Q15: 1 đăng ký còn hiệu lực / sinh viên / sự kiện; dòng đã hủy không chặn đăng ký lại
CREATE UNIQUE INDEX uq_active_registration ON registrations (event_id, student_id)
  WHERE status IN ('REGISTERED','WAITLISTED');
-- Lấy người đầu waitlist: O(log n)
CREATE INDEX idx_waitlist_head  ON registrations (event_id, queue_seq) WHERE status = 'WAITLISTED';
CREATE INDEX idx_reg_event      ON registrations (event_id, status);
CREATE INDEX idx_reg_student    ON registrations (student_id, created_at DESC);
-- Báo cáo điểm danh
CREATE INDEX idx_reg_checked_in ON registrations (event_id, checked_in_at)
  WHERE checked_in_at IS NOT NULL;
-- Ngoại lệ hủy Q13–Q19
CREATE INDEX idx_reg_cancel_exception ON registrations (event_id, cancellation_exception_until)
  WHERE cancellation_exception_until IS NOT NULL;

-- ========== 6. NOTIFICATIONS (in-app + outbox email) ==========
CREATE TABLE notifications (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id        uuid NOT NULL REFERENCES users(id),
  event_id       uuid REFERENCES events(id),
  type           notification_type NOT NULL,
  title          varchar(200) NOT NULL,
  body           text NOT NULL,
  read_at        timestamptz,
  email_status   delivery_status NOT NULL DEFAULT 'PENDING',
  email_attempts smallint NOT NULL DEFAULT 0,
  last_error     text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_notif_user   ON notifications (user_id, created_at DESC);
CREATE INDEX idx_notif_outbox ON notifications (created_at) WHERE email_status = 'PENDING';

-- ========== 7. AUTH_SESSIONS ==========
CREATE TABLE auth_sessions (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            uuid NOT NULL REFERENCES users(id),
  refresh_token_hash text NOT NULL UNIQUE,
  expires_at         timestamptz NOT NULL,
  revoked_at         timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);

-- ========== 8. ACTIVITY_LOGS (gộp approval/change/audit logs) ==========
CREATE TABLE activity_logs (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_id    uuid REFERENCES users(id),           -- NULL khi job hệ thống
  event_id    uuid REFERENCES events(id),          -- FK thật cho log liên quan sự kiện
  entity_type varchar(30) NOT NULL,                -- EVENT, REGISTRATION, USER, UNIT
  entity_id   uuid,
  action      varchar(40) NOT NULL,                -- xem bảng action bên dưới
  metadata    jsonb,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_activity_event  ON activity_logs (event_id, created_at);
CREATE INDEX idx_activity_entity ON activity_logs (entity_type, entity_id);
```

Các action ghi vào `activity_logs`. Vai trò của một user được đọc bằng truy vấn: `SELECT student_code IS NOT NULL AS is_student, is_admin, EXISTS (SELECT 1 FROM unit_memberships m WHERE m.user_id = u.id) AS is_organizer FROM users u WHERE u.id = :id`. Lịch sử duyệt (P5) là truy vấn `WHERE event_id = :id AND action IN ('SUBMIT','APPROVE','REJECT')`.

| `action` | `entity_type` | Có `event_id` | `metadata` |
| --- | --- | --- | --- |
| `SUBMIT`, `APPROVE`, `REJECT` | EVENT | Có | `{ "comment" }` hoặc `{ "reason" }` |
| `UPDATE_EVENT` | EVENT | Có | `{ "changes": { "startAt": { "old", "new" } }, "isSignificant": true, "exceptionGrantedCount": 42 }` |
| `CANCEL_EVENT` | EVENT | Có | `{ "reason", "affectedCount" }` |
| `CANCEL_ON_BEHALF` | REGISTRATION | Có | `{ "reason", "studentId" }` |
| `MANUAL_CHECKIN`, `UNDO_CHECKIN` | REGISTRATION | Có | `{ "note", "previousCheckedInAt" }` |
| `EXPORT_DATA` | EVENT | Có | `{ "kind", "format", "rowCount" }` |
| `UPDATE_USER`, `SET_UNIT_MEMBERS` | USER, UNIT | Không | `{ "before", "after" }` |
| `GRANT_ADMIN`, `REVOKE_ADMIN` | USER | Không | `{ "ticket" }` (ghi bởi quy trình nội bộ của đội dev) |

Notification được INSERT trong cùng transaction với thay đổi trạng thái, nên bản in-app luôn tồn tại; chỉ việc gửi email là bất đồng bộ và có thể FAILED mà không ảnh hưởng đăng ký (Q14).

## 3. Máy trạng thái

&#91;embedded content: máy trạng thái · Event 6 trạng thái, Registration 5 trạng thái + check-in\]

Chỉ PUBLISHED xuất hiện ở danh mục E1 và nhận đăng ký; REGISTERED là trạng thái duy nhất được check-in. Check-in được ghi bằng các cột checked\_in\_\* chứ không phải một status, nên hoàn tác check-in (C3) không làm đổi trạng thái đăng ký. Mọi chuyển trạng thái không có mũi tên trên sơ đồ đều trả `409 INVALID_STATE_TRANSITION`.

## 4. Quy ước API

REST/JSON, base URL `/api/v1`, xác thực bằng JWT Bearer. Mọi lỗi nghiệp vụ trả về một mã `code` ổn định để frontend xử lý, không dựa vào câu chữ `message`.

- **Định dạng:** JSON, field `camelCase`; thời gian ISO 8601 có offset (`2026-11-05T08:00:00+07:00`), server lưu UTC.
- **Xác thực:** `Authorization: Bearer <accessToken>`; access token 15 phút, refresh token 7 ngày (lưu hash trong `auth_sessions`).
- **Phân trang:** `?page=1&pageSize=20` (tối đa 100); response có `meta: { page, pageSize, totalItems, totalPages }`.
- **Optimistic lock:** `PATCH /events/{id}` bắt buộc header `If-Match: <version>`; sai version → `409 VERSION_CONFLICT`.
- **Idempotency:** đăng ký và check-in tự idempotent nhờ unique constraint — gọi lại trả về bản ghi đã có kèm mã lỗi tương ứng, không tạo bản ghi mới.

Response thành công:

```json
{ "data": { ... }, "meta": { ... } }
```

Response lỗi:

```json
{
  "error": {
    "code": "CANCEL_DEADLINE_PASSED",
    "message": "Chỉ được hủy trước giờ bắt đầu 24 giờ.",
    "details": { "deadline": "2026-11-04T08:00:00+07:00" }
  }
}
```

| HTTP | `code` | Khi nào |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Body/query sai kiểu hoặc thiếu field; `details.fields[]` |
| 401 | `UNAUTHENTICATED` | Thiếu/hết hạn token |
| 403 | `FORBIDDEN` | Người gọi không có vai trò cần cho thao tác (vd đăng ký mà chưa có MSSV); tài nguyên ngoài đơn vị/phạm vi trả 404 |
| 403 | `SELF_APPROVAL_NOT_ALLOWED` | Người duyệt là người tạo sự kiện |
| 403 | `SELF_CHECKIN_NOT_ALLOWED` | Organizer check-in (hoặc hoàn tác check-in) cho chính đăng ký của mình |
| 404 | `NOT_FOUND` | Không tồn tại, hoặc tồn tại nhưng người gọi không được thấy (tránh lộ dữ liệu) |
| 409 | `ALREADY_REGISTERED` | Đã có đăng ký REGISTERED/WAITLISTED cho sự kiện này |
| 409 | `EVENT_FULL` | Hết chỗ và waitlist đã đạt `waitlistLimit` |
| 409 | `INVALID_STATE_TRANSITION` | Thao tác không hợp lệ với trạng thái hiện tại |
| 409 | `CAPACITY_BELOW_REGISTERED` | Giảm sức chứa xuống dưới số đã đăng ký |
| 409 | `VERSION_CONFLICT` | `If-Match` không khớp `version` |
| 409 | `ALREADY_CHECKED_IN` | Check-in trùng; `details.checkedInAt` |
| 422 | `REGISTRATION_CLOSED` | Ngoài cửa sổ đăng ký hoặc sự kiện không ở PUBLISHED |
| 422 | `CANCEL_DEADLINE_PASSED` | Sinh viên tự hủy khi còn < 24h |
| 422 | `CHECKIN_WINDOW_CLOSED` | Ngoài cửa sổ check-in |
| 422 | `NOT_ELIGIBLE_FOR_CHECKIN` | Registration không ở trạng thái REGISTERED |
| 422 | `INVALID_QR` | Chữ ký QR sai hoặc QR thuộc sự kiện khác |

## 5. API contract

33 endpoint chia 8 nhóm, bám theo workflow nghiệm thu: tạo/duyệt sự kiện → đăng ký/hủy → waitlist → check-in → báo cáo. Cột Role là vai trò cần có (Student = có MSSV, Organizer = được gán đơn vị, Admin = `is_admin`); một user có thể có nhiều vai trò và được hợp quyền. Quyền theo đơn vị xem mục 6.

| # | Method | Path | Role | Mô tả |
| --- | --- | --- | --- | --- |
| A1 | POST | `/auth/google` | Public | Xác minh Google ID token của tài khoản trường, cấp token nội bộ |
| A2 | POST | `/auth/refresh` | Public | Đổi refresh token lấy access token mới |
| A3 | POST | `/auth/logout` | Đã đăng nhập | Thu hồi refresh token |
| A4 | GET | `/me` | Đã đăng nhập | Thông tin user + danh sách đơn vị và vai trò |
| U1 | GET | `/units` | Đã đăng nhập | Danh sách đơn vị (lọc sự kiện) |
| U2 | POST | `/admin/units` | Admin | Tạo đơn vị |
| U3 | PUT | `/admin/units/{unitId}/members` | Admin | Gán Organizer cho đơn vị |
| U4 | GET | `/admin/users` | Admin | Tìm user (quản trị cấp trường) |
| U5 | PATCH | `/admin/users/{userId}` | Admin | Chỉ đổi `isActive`; cấp `is_admin` do đội dev thực hiện ngoài API |
| E1 | GET | `/events` | Đã đăng nhập | Danh mục sự kiện PUBLISHED (cho sinh viên) |
| E2 | GET | `/events/{eventId}` | Đã đăng nhập | Chi tiết; kèm `myRegistration` nếu là sinh viên |
| E3 | GET | `/manage/events` | Organizer, Admin | Organizer: đơn vị được giao; Admin: toàn trường |
| E4 | POST | `/events` | Organizer | Tạo sự kiện (DRAFT) |
| E5 | PATCH | `/events/{eventId}` | Organizer | Sửa; cần `If-Match` |
| E6 | DELETE | `/events/{eventId}` | Organizer | Xóa, chỉ khi DRAFT |
| E7 | POST | `/events/{eventId}/cancel` | Organizer | Hủy sự kiện đã công bố |
| P1 | POST | `/events/{eventId}/submit` | Organizer | Gửi duyệt (DRAFT/REJECTED → PENDING\_APPROVAL) |
| P2 | GET | `/approvals/pending` | Admin | Hàng chờ duyệt toàn trường |
| P3 | POST | `/events/{eventId}/approve` | Admin | Duyệt và công bố |
| P4 | POST | `/events/{eventId}/reject` | Admin | Từ chối, bắt buộc lý do |
| P5 | GET | `/events/{eventId}/approval-logs` | Organizer, Admin | Lịch sử duyệt |
| R1 | POST | `/events/{eventId}/registrations` | Student | Đăng ký; tự vào waitlist nếu hết chỗ |
| R2 | POST | `/registrations/{registrationId}/cancel` | Student, Organizer | Hủy; tự promote người đầu waitlist |
| R3 | GET | `/me/registrations` | Student | Đăng ký của tôi (lọc theo status, thời gian) |
| R4 | GET | `/registrations/{registrationId}` | Student (chủ sở hữu), Organizer | Chi tiết + QR token + mã nhập tay |
| R5 | GET | `/events/{eventId}/registrations` | Organizer, Admin | Danh sách đăng ký / waitlist theo thứ tự |
| C1 | POST | `/events/{eventId}/check-ins` | Organizer | Check-in bằng QR, mã, hoặc xác nhận tay |
| C2 | GET | `/events/{eventId}/check-ins` | Organizer, Admin | Danh sách đã check-in |
| C3 | DELETE | `/registrations/{registrationId}/check-in` | Organizer | Hoàn tác check-in nhầm (ghi audit) |
| B1 | GET | `/events/{eventId}/reports/summary` | Organizer, Admin | Thống kê số lượng |
| B2 | GET | `/events/{eventId}/exports/{kind}` | Organizer, Admin | Xuất `registrations` hoặc `attendance`, `?format=csv` (mặc định) |
| N1 | GET | `/me/notifications` | Đã đăng nhập | Thông báo in-app, `?unread=true` |
| N2 | POST | `/me/notifications/read` | Đã đăng nhập | Đánh dấu đã đọc: `{ "ids": [...] }` hoặc `{ "all": true }` |

### 5.1 Auth

**A1 `POST /auth/google`**

```json
// Request
{ "idToken": "<Google ID token từ Google Identity Services>" }

// 200
{
  "data": {
    "accessToken": "eyJhbGciOi...",
    "refreshToken": "rt_9f2c...",
    "expiresIn": 900,
    "user": {
      "id": "6c1e...", "email": "an.nguyen@hcmut.edu.vn", "fullName": "Nguyễn Văn An",
      "studentCode": "2210001", "isAdmin": false, "roles": ["STUDENT"], "units": []
    }
  }
}
```

Frontend lấy ID token qua Google Identity Services; gửi qua HTTPS tới backend. Backend dùng thư viện OIDC xác minh chữ ký với Google JWKS, issuer Google hợp lệ, `aud` khớp Google client ID cấu hình, thời hạn `exp`, `email_verified = true`, `hd = hcmut.edu.vn` và domain email chính xác `hcmut.edu.vn`. Không chỉ kiểm tra hậu tố chuỗi hoặc tin tham số `hd` của frontend. Không nhận email/role tùy ý từ client.

Định danh bằng `google_sub` (`sub` của Google). Lần đầu: nếu email có trong danh sách import thì liên kết vào tài khoản đó (có MSSV → là STUDENT); nếu không thì tạo user mới với `student_code = NULL`, `is_admin = false`, chưa có vai trò nào. User seed chưa có sub được liên kết theo email đã xác minh trong transaction. Không liên kết nếu email đã gắn sub khác; unique email/sub bảo vệ đăng nhập song song. Không tự cấp vai trò; không nhận MSSV hay `isAdmin` từ client. Admin đầu tiên seed email thật, không có mật khẩu. Không lưu token Google; refresh/logout dùng session nội bộ, không đăng xuất Google. Không log ID token, access token hay refresh token.

Lỗi: `401 UNAUTHENTICATED` (token sai/hết hạn, sai audience/domain/hd, email chưa xác minh, xung đột liên kết hoặc `isActive = false`). JWT payload: `sub`, `exp`. Vai trò (MSSV, `is_admin`, đơn vị được giao) không nhét vào token mà tra DB mỗi request để đổi quyền có hiệu lực ngay. Response trả `roles` là mảng suy ra, ví dụ `["STUDENT","ORGANIZER"]`, để frontend hiển thị menu.

### 5.2 Events

**Event object** (dùng chung E1–E7):

```json
{
  "id": "e7a1...",
  "unit": { "id": "u01...", "code": "CSE", "name": "Khoa KH&KT Máy tính" },
  "title": "Workshop: System Design 101",
  "description": "...",
  "location": "H6-112",
  "startAt": "2026-11-05T08:00:00+07:00",
  "endAt": "2026-11-05T11:00:00+07:00",
  "registrationOpensAt": "2026-10-25T00:00:00+07:00",
  "registrationClosesAt": "2026-11-04T23:59:00+07:00",
  "capacity": 120,
  "waitlistLimit": 50,
  "registeredCount": 120,
  "waitlistCount": 7,
  "availableSeats": 0,
  "isRegistrationOpen": true,
  "cancelDeadline": "2026-11-04T08:00:00+07:00",
  "status": "PUBLISHED",
  "version": 3,
  "createdBy": { "id": "...", "fullName": "..." },
  "myRegistration": { "id": "r55...", "status": "WAITLISTED", "waitlistPosition": 3 }
}
```

`myRegistration` chỉ trả khi người gọi có vai trò STUDENT (null nếu chưa đăng ký); `createdBy` và `version` chỉ trả cho Organizer của đơn vị hoặc Admin. Organizer kiêm sinh viên nhận cả hai nhóm field.

**E1 `GET /events`** — query: `q`, `unitId`, `from`, `to`, `availableOnly=true`, `page`, `pageSize`. Chỉ trả `status = PUBLISHED`, sắp theo `startAt` tăng dần.

**E3 `GET /manage/events`** — query thêm `status` (nhiều giá trị, phân cách dấu phẩy). Organizer thấy sự kiện thuộc các đơn vị mình là ORGANIZER; Admin thấy mọi sự kiện toàn trường.

**E4 `POST /events`**

```json
// Request
{
  "unitId": "u01...",
  "title": "Workshop: System Design 101",
  "description": "...",
  "location": "H6-112",
  "startAt": "2026-11-05T08:00:00+07:00",
  "endAt": "2026-11-05T11:00:00+07:00",
  "registrationOpensAt": "2026-10-25T00:00:00+07:00",
  "registrationClosesAt": "2026-11-04T23:59:00+07:00",
  "capacity": 120,
  "waitlistLimit": 50
}
// 201 → Event object, status = DRAFT
```

Validation: người gọi phải là ORGANIZER của `unitId`; `endAt > startAt`; `registrationOpensAt < registrationClosesAt <= startAt`; `startAt` và `registrationClosesAt` phải sau thời điểm hiện tại; `capacity` 1–5000. `registrationOpensAt` được phép ở quá khứ để mở đăng ký ngay. Lỗi ngày trả `400 VALIDATION_ERROR` kèm `details.fields` chỉ rõ field và lý do.

**E5 `PATCH /events/{eventId}`** — header `If-Match: 3`, body là tập con field của E4 (trừ `unitId`).

- DRAFT / REJECTED: sửa mọi field; lịch sau khi sửa phải đáp ứng validation E4, gồm hạn đóng đăng ký ở tương lai.
- PENDING\_APPROVAL: không cho sửa → `409 INVALID_STATE_TRANSITION` (muốn sửa thì Admin reject trước).
- PUBLISHED: sửa được, không cần duyệt lại (Q19). Từ 60 phút trước startAt cũ không sửa được `startAt`, `endAt` hoặc `location` → 409 INVALID\_STATE\_TRANSITION. Mọi thay đổi các field này đều ghi `activity_logs (UPDATE_EVENT, isSignificant: true)` và tạo notification `EVENT_CHANGED` cho REGISTERED + WAITLISTED. Chỉ nếu thay đổi trong 24h trước startAt cũ mới cấp thêm `cancellation_exception_until = new startAt` cho những dòng REGISTERED hiện hữu, trong cùng transaction. Người đăng ký sau thời điểm sửa không được cấp ngoại lệ. Tăng `capacity` → promote tự động; giảm dưới `registeredCount` → `409 CAPACITY_BELOW_REGISTERED`.
- CANCELLED / COMPLETED: không sửa được.

PUBLISHED được giữ nguyên hạn đóng đăng ký đã qua khi sửa thông tin khác; nếu đổi `registrationClosesAt`, giá trị mới phải sau thời điểm hiện tại và không sau `startAt`.

```json
// 200
{ "data": { ...Event object, "version": 4 },
  "meta": { "significantChange": true, "notifiedCount": 127, "promotedCount": 0 } }
```

**E7 `POST /events/{eventId}/cancel`** — `{ "reason": "Diễn giả bận đột xuất" }` (không được chỉ whitespace, tối đa 500 ký tự). Chỉ Organizer của đơn vị active được hủy; Admin không tự có quyền này. PUBLISHED → CANCELLED; mọi registration REGISTERED/WAITLISTED → `CANCELLED_BY_EVENT`, xóa ngoại lệ hủy, đếm về 0, notify `EVENT_CANCELLED`. Giữ lịch sử check-in; không promote khi hủy sự kiện. Khóa event trước registrations; event, registrations, notification và audit `CANCEL_EVENT` commit cùng transaction, lỗi bất kỳ rollback toàn bộ. `If-Match` tùy chọn; sai version trả `409 VERSION_CONFLICT`, không phải PUBLISHED trả `409 INVALID_STATE_TRANSITION`. Trả `meta.affectedCount` (số registration active bị hủy).

### 5.3 Duyệt sự kiện

**P1 `POST /events/{eventId}/submit`** — không body. Kiểm tra lại lịch theo E4 tại thời điểm gửi; nếu `startAt` hoặc `registrationClosesAt` không còn ở tương lai, trả `422 VALIDATION_ERROR` kèm `details.fields`. Hợp lệ: DRAFT/REJECTED → PENDING\_APPROVAL, ghi log `SUBMIT`.

**P3 `POST /events/{eventId}/approve`** — `{ "comment": "OK" }` (tùy chọn). Kiểm tra: user có `is_admin = true`, và `userId <> createdBy`. Không phải Admin → `403 FORBIDDEN`; người duyệt trùng người tạo → `403 SELF_APPROVAL_NOT_ALLOWED`. Kiểm tra lại lịch theo E4 tại thời điểm duyệt; nếu `startAt` hoặc `registrationClosesAt` không còn ở tương lai → `422 VALIDATION_ERROR` kèm `details.fields`. Thành công: status = PUBLISHED, set `approvedBy`, `approvedAt`, `publishedAt`, notify Organizer `EVENT_APPROVED`.

**P4 `POST /events/{eventId}/reject`** — `{ "reason": "Thiếu thông tin địa điểm" }` (bắt buộc). PENDING\_APPROVAL → REJECTED, lưu lý do vào events.reject\_reason và activity\_logs, notify Organizer `EVENT_REJECTED`.

### 5.4 Đăng ký & waitlist

**R1 `POST /events/{eventId}/registrations`** — không body; sinh viên lấy từ token. Người gọi phải có vai trò STUDENT (có MSSV), nếu không → `403 FORBIDDEN`. Organizer kiêm sinh viên đăng ký được như sinh viên thường.

```json
// 201 — còn chỗ
{ "data": { "id": "r55...", "eventId": "e7a1...", "status": "REGISTERED",
            "waitlistPosition": null, "checkinCode": "K7Q2-M9XD", "createdAt": "..." } }

// 201 — hết chỗ, vào waitlist
{ "data": { "id": "r56...", "eventId": "e7a1...", "status": "WAITLISTED",
            "waitlistPosition": 8, "checkinCode": null, "createdAt": "..." } }
```

Lỗi: `409 ALREADY_REGISTERED` (kèm `details.registrationId`), `409 EVENT_FULL`, `422 REGISTRATION_CLOSED`. `checkinCode` chỉ hiện khi REGISTERED.

**R2 `POST /registrations/{registrationId}/cancel`** — `{ "reason": "..." }` (tùy chọn với sinh viên, bắt buộc khi Organizer hủy hộ).

- Sinh viên chủ sở hữu: được hủy REGISTERED nếu `now < startAt − 24h` HOẶC `now < startAt AND now < cancellation_exception_until`; nếu không → `422 CANCEL_DEADLINE_PASSED` (Q13 + Q19). Chỉ các dòng hiện hữu trước thay đổi quan trọng trong 24h cuối mới có ngoại lệ. Rời waitlist không bị chặn bởi hạn 24h nhưng chỉ trước `startAt`; từ `startAt` có trạng thái hiệu lực EXPIRED.
- Organizer của đơn vị: hủy hộ bất kỳ lúc nào trước `startAt`, ghi `activity_logs`. Nếu registration là của chính Organizer đó thì xử lý như sinh viên tự hủy (áp hạn 24h, `cancelledBy = STUDENT`), không dùng quyền hủy hộ.

```json
// 200
{ "data": { "id": "r55...", "status": "CANCELLED", "cancelledAt": "...", "cancelledBy": "STUDENT" },
  "meta": { "promotedRegistrationId": "r40..." } }
```

**R3 `GET /me/registrations`** — query `status`, `upcoming=true`. Mỗi phần tử gồm registration + event rút gọn (`id`, `title`, `startAt`, `location`, `status`).

**R4 `GET /registrations/{registrationId}`** — trả thêm `qrToken` khi REGISTERED:

```json
{ "data": { "id": "r55...", "status": "REGISTERED", "checkinCode": "K7Q2-M9XD",
            "qrToken": "v1.r55....e7a1....3f9c0b2a1d",
            "checkIn": null, "event": { ... } } }
```

`qrToken = v1.<registrationId>.<eventId>.<HMAC-SHA256(secret, registrationId|eventId)>`, frontend tự vẽ QR từ chuỗi này. Token không chứa thông tin cá nhân và không tự hết hạn; hiệu lực được kiểm tra bằng trạng thái registration lúc quét.

**R5 `GET /events/{eventId}/registrations`** — query `status` (mọi RegistrationStatus), `q` (tên/MSSV, literal không phân biệt hoa thường, tối đa 200 bytes), `page`, `pageSize` (mặc định 1/20, tối đa 100). Sắp `queueSeq, id` tăng dần; `waitlistPosition` tính trong toàn bộ waitlist còn hiệu lực trước lọc/tìm kiếm/phân trang. WAITLISTED quá `startAt` trả EXPIRED ngay cả khi worker chưa chạy. Mỗi dòng: `student { id, fullName, studentCode, email }`, `status`, `createdAt`, `promotedAt`, `checkedInAt` và các field nullable theo OpenAPI.

**C2 `GET /events/{eventId}/check-ins`** — lọc `method=QR|CODE|MANUAL`, phân trang mặc định 1/20, tối đa 100; sắp `checkedInAt DESC, registrationId DESC`. Chỉ dòng có check-in còn lưu, undo loại khỏi danh sách; hủy event vẫn giữ lịch sử check-in.

**P5 `GET /events/{eventId}/approval-logs`** — danh sách không phân trang, sắp `createdAt, id` tăng dần, chỉ SUBMIT/APPROVE/REJECT. Mỗi chuyển trạng thái ghi audit cùng transaction, REJECT lưu `metadata.reason` trả thành `comment`; dữ liệu cũ chưa ghi audit không được dựng lại giả.

R5/C2/P5 chỉ Organizer thuộc đơn vị active hoặc Admin; user phải active, ngoài phạm vi/mất quyền trả 404. Đọc scope, count và rows trong cùng snapshot REPEATABLE READ. Trả mảng rỗng `[]`, response `Cache-Control: no-store`; không trả QR token/checkinCode trong danh sách quản lý.

### 5.5 Check-in

**C1 `POST /events/{eventId}/check-ins`** — một trong ba dạng:

```json
{ "method": "QR",     "qrToken": "v1.r55....e7a1....3f9c0b2a1d" }
{ "method": "CODE",   "checkinCode": "K7Q2M9XD" }
{ "method": "MANUAL", "registrationId": "r55...", "note": "SV quên điện thoại, đã đối chiếu thẻ SV" }
```

```json
// 201
{ "data": { "registrationId": "r55...", "method": "QR",
            "checkedInAt": "2026-11-05T07:52:10+07:00",
            "checkedInBy": { "id": "...", "fullName": "Trần Thị B" },
            "student": { "fullName": "Nguyễn Văn An", "studentCode": "2210001" } } }
```

**C3 `DELETE /registrations/{registrationId}/check-in`** — hoàn tác check-in nhầm: đưa 4 cột `checked_in_*` về NULL và ghi `activity_logs` (`UNDO_CHECKIN`, kèm thời điểm check-in cũ). Trả `204`; chưa check-in → `404 NOT_FOUND`.

Lỗi: `409 ALREADY_CHECKED_IN` (kèm `details.checkedInAt`, `details.checkedInBy` để BTC xử lý tại chỗ), `403 SELF_CHECKIN_NOT_ALLOWED` (Organizer quét đăng ký của chính mình; C3 cũng chặn tương tự), `422 INVALID_QR`, `422 NOT_ELIGIBLE_FOR_CHECKIN`, `422 CHECKIN_WINDOW_CLOSED`. Mã nhập tay không phân biệt hoa thường và bỏ qua dấu `-`. Check-in MANUAL bắt buộc `note` và ghi `activity_logs`.

### 5.6 Báo cáo & xuất file

**B1 `GET /events/{eventId}/reports/summary`**

```json
{ "data": {
    "capacity": 120, "registered": 118, "waitlisted": 0, "cancelled": 14,
    "promotedFromWaitlist": 9, "checkedIn": 101, "attendanceRate": 0.856,
    "checkInByMethod": { "QR": 92, "CODE": 6, "MANUAL": 3 },
    "generatedAt": "2026-11-05T11:05:00+07:00" } }
```

`attendanceRate = registered == 0 ? 0 : checkedIn / registered`. `waitlisted` chỉ đếm WAITLISTED còn hiệu lực (`now < start_at`), trả 0 từ start_at dù worker chưa chạy.

**B2 `GET /events/{eventId}/exports/{kind}?format=csv`** — `kind = registrations | attendance`. MVP hỗ trợ CSV (mặc định); XLSX để giai đoạn sau, format khác trả `400 VALIDATION_ERROR`. Trả file trực tiếp (`Content-Disposition: attachment`), CSV có BOM UTF-8 để Excel đọc đúng tiếng Việt và vô hiệu hóa ô có thể chạy công thức. Cả hai loại dùng cột: STT, MSSV, Họ tên, Email, Trạng thái đăng ký, Thời gian check-in, Phương thức. `registrations` gồm mọi trạng thái; `attendance` chỉ gồm đăng ký đã check-in. Mỗi lần xuất ghi `activity_logs` (`EXPORT_DATA`, metadata kind/format/rowCount, không chứa dữ liệu cá nhân) và chỉ trả file sau khi audit commit thành công. Báo cáo và export đọc trên snapshot nhất quán; Organizer chỉ đơn vị active được gán, Admin toàn trường.

### 5.7 Notifications

**N1 `GET /me/notifications`**

```json
{ "data": [ { "id": "n01...", "type": "PROMOTED", "eventId": "e7a1...",
              "title": "Bạn đã có chỗ tại Workshop: System Design 101",
              "body": "Bạn được chuyển từ danh sách chờ sang danh sách đăng ký.",
              "readAt": null, "createdAt": "..." } ],
  "meta": { "unreadCount": 2, "page": 1, "pageSize": 20, "totalItems": 5, "totalPages": 1 } }
```

N1 chỉ trả thông báo của user đang đăng nhập, sắp xếp `created_at DESC, id DESC`; `page` mặc định 1, `pageSize` mặc định 20, tối đa 100. `unread=true` lọc chưa đọc; `totalItems` theo bộ lọc, `unreadCount` luôn là tổng chưa đọc của user. Danh sách rỗng trả `data: []`. Frontend polling mỗi 10 giây khi đã đăng nhập và tab hiển thị; quay lại tab fetch ngay. Backend không chạy timer polling; response có `Cache-Control: no-store`.

**N2 `POST /me/notifications/read`** — nhận đúng một trong `{ "ids": ["uuid", ...] }` (1–100 UUID) hoặc `{ "all": true }`. Trả `{ "data": { "updatedCount": 1, "unreadCount": 0 } }`. Chỉ cập nhật thông báo chưa đọc thuộc user hiện tại; ID không tồn tại/thuộc người khác bị bỏ qua, không làm lộ dữ liệu. Gọi lại idempotent, giữ nguyên `readAt`. Body/filter sai trả `400 VALIDATION_ERROR`. Đánh dấu đọc không thay đổi trạng thái email/outbox. Không có API cho Admin đọc hộ người khác.

Nhắc lịch (`REMINDER`) do job chạy 24h trước `startAt` — chức năng bổ trợ, làm sau cùng (Q1).

## 6. Phân quyền & xử lý đồng thời

Quyền được xét theo vai trò suy ra (mục 1), và một user được **hợp quyền** các vai trò mình có: Student chỉ dữ liệu mình; Organizer giới hạn đơn vị được gán qua `unit_memberships`; Admin cấp trường xem, duyệt và quản lý toàn trường nhưng vai trò Admin không cho quyền check-in. Mọi thao tác làm thay đổi số chỗ của một sự kiện đều khóa dòng `events` đó (`SELECT … FOR UPDATE`), nên các request cùng sự kiện chạy tuần tự còn các sự kiện khác nhau vẫn song song.

### 6.1 Ma trận phân quyền

| Thao tác | Student (có MSSV) | Organizer (đơn vị mình) | Admin (cấp trường) |
| --- | --- | --- | --- |
| Xem danh mục sự kiện PUBLISHED | Có | Có | Có |
| Tạo / sửa / xóa nháp / hủy sự kiện | — | Có | — |
| Gửi duyệt | — | Có | — |
| Duyệt / từ chối | — | — | Có, trừ sự kiện do chính mình tạo |
| Đăng ký, tự hủy, rời waitlist | Có (của mình) | — | — |
| Hủy đăng ký hộ sinh viên | — | Có | — |
| Xem danh sách đăng ký, waitlist, điểm danh | Chỉ đăng ký của mình | Có | Có (chỉ đọc) |
| Check-in, hoàn tác check-in | — | Có, trừ đăng ký của chính mình | Không |
| Xem báo cáo, xuất Excel/CSV | — | Có | Có |
| Quản lý user (khóa/mở), đơn vị, gán Organizer | — | — | Có; cấp `is_admin` chỉ qua đội dev |

Các cột cộng dồn: một Organizer kiêm sinh viên có cả quyền cột Student (với đăng ký của mình) lẫn cột Organizer (với đơn vị được gán). Một Admin đồng thời được gán Organizer của đơn vị thì check-in được sự kiện của đơn vị đó với tư cách Organizer.

Truy cập tài nguyên ngoài phạm vi trả `404 NOT_FOUND` thay vì `403`, để không lộ việc sự kiện hay đăng ký đó tồn tại. Dữ liệu cá nhân (email, MSSV) chỉ xuất hiện ở R5, C1, C2, B2 và chỉ cho Organizer/Admin trong phạm vi.

### 6.2 Đăng ký (R1)

```sql
BEGIN;
-- 1. Khóa sự kiện: mọi thao tác đổi chỗ của cùng sự kiện xếp hàng tuần tự
SELECT status, capacity, registered_count, waitlist_limit, waitlist_count,
       registration_opens_at, registration_closes_at
FROM events WHERE id = :event_id FOR UPDATE;
-- 2. App kiểm tra: status = 'PUBLISHED' và now() trong cửa sổ đăng ký, nếu không → 422

-- 3a. Còn chỗ (registered_count < capacity)
INSERT INTO registrations (event_id, student_id, status, checkin_code)
VALUES (:event_id, :student_id, 'REGISTERED', :code);
UPDATE events SET registered_count = registered_count + 1 WHERE id = :event_id;

-- 3b. Hết chỗ, waitlist còn chỗ → INSERT status 'WAITLISTED'
--     UPDATE events SET waitlist_count = waitlist_count + 1 ...
-- 3c. Waitlist đầy → ROLLBACK, 409 EVENT_FULL

INSERT INTO notifications (user_id, event_id, type, title, body) VALUES (...);
COMMIT;
-- unique_violation trên uq_active_registration → 409 ALREADY_REGISTERED
```

`checkin_code` lưu 8 ký tự không dấu gạch, API hiển thị dạng `XXXX-XXXX`. Mã sinh sẵn cho cả WAITLISTED nhưng API chỉ hiển thị khi REGISTERED; nếu trùng `uq_checkin_code` thì sinh lại và thử lại (xác suất rất thấp với 32^8 tổ hợp).

### 6.3 Hủy + tự promote (R2, E5 tăng capacity)

```sql
BEGIN;
SELECT start_at, capacity, registered_count FROM events WHERE id = :event_id FOR UPDATE;
SELECT status AS old_status FROM registrations WHERE id = :reg_id FOR UPDATE;

UPDATE registrations
SET status = 'CANCELLED', cancelled_at = now(), cancelled_by = :actor,
    cancelled_by_user = :actor_id, cancel_reason = :reason,
    cancellation_exception_until = NULL, updated_at = now()
WHERE id = :reg_id AND status IN ('REGISTERED','WAITLISTED')
RETURNING id;
-- old_status = REGISTERED → registered_count - 1; WAITLISTED → waitlist_count - 1

-- Promote người đầu waitlist (chỉ khi now() < start_at và còn chỗ)
WITH head AS (
  SELECT id FROM registrations
  WHERE event_id = :event_id AND status = 'WAITLISTED'
  ORDER BY queue_seq
  LIMIT 1
)
UPDATE registrations r
SET status = 'REGISTERED', promoted_at = now(), updated_at = now()
FROM head WHERE r.id = head.id
RETURNING r.id, r.student_id;
-- Có dòng → registered_count + 1, waitlist_count - 1,
--           INSERT notification 'PROMOTED' (INSERT lỗi thì rollback; gửi email bất đồng bộ sau commit lỗi không rollback — Q14)
COMMIT;
```

Với E5 tăng `capacity` thêm k chỗ, lặp bước promote tối đa k lần trong cùng transaction. `chk_capacity` là chốt chặn cuối: dù code có bug, DB vẫn từ chối mọi trạng thái vượt chỗ.

**Trade-off đã cân nhắc:** khóa dòng `events` đơn giản, dễ chứng minh đúng và đủ nhanh ở quy mô giả định (1.000 chỗ/sự kiện). Phương án thay thế là isolation `SERIALIZABLE` + retry khi xung đột, tránh khóa nhưng phức tạp hơn khi viết và kiểm thử. Bộ đếm `registered_count` thay cho `COUNT(*)` để CHECK constraint chống overbooking được và đọc danh mục không phải đếm lại.

### 6.4 Ngoại lệ tự hủy Q13–Q19

- Trong transaction E5, khóa `events` (`FOR UPDATE`) trước, lấy mốc `start_at` cũ. Nếu sự kiện PUBLISHED, còn trước cửa sổ 60 phút, và đổi `start_at`, `end_at` hoặc `location` trong vòng 24h trước mốc bắt đầu cũ, cập nhật `cancellation_exception_until = :new_start_at` cho **các registration REGISTERED hiện hữu**. Chỉ cấp cho sinh viên đã đăng ký trước khi thay đổi, không cấp cho đăng ký đến sau; có thể ghi `exceptionGrantedCount` vào `activity_logs`. Ghi notification trong cùng transaction.
- Nếu đổi giờ bắt đầu làm sự kiện còn hơn 24h nữa, ai cũng có quyền hủy theo deadline mới; cột ngoại lệ không làm thay đổi quyền bình thường. Nếu cập nhật nhiều lần, chỉ cập nhật người đang REGISTERED tại lần thay đổi đó, giữ ngoại lệ đã cấp trước (nếu còn hiệu lực) theo giờ bắt đầu mới.
- R2 kiểm tra điều kiện hủy **bên trong transaction sau khi khóa event**: `status = REGISTERED AND (now() < start_at - interval '24 hours' OR (now() < start_at AND now() < cancellation_exception_until))`; WAITLISTED có thể rời hàng chờ trước `start_at` không cần ngoại lệ. Hủy xong `cancellation_exception_until` về NULL. Khi event bắt đầu, ngoại lệ tự hết hiệu lực theo điều kiện `now() < start_at`.
- Đây là assumption do nhóm chốt, không phải quy tắc đã được client phê duyệt riêng.

**SQL tham khảo cho E5** (chạy sau khi đã khóa `events`, xác minh Organizer, validation và trước khi ghi notification/commit):

```sql
UPDATE registrations
SET cancellation_exception_until = :new_start_at, updated_at = now()
WHERE event_id = :event_id AND status = 'REGISTERED';
-- Chỉ thực hiện khi thay đổi thời gian/địa điểm trong 24h cuối trước start_at cũ;
-- số dòng ảnh hưởng được ghi vào activity_logs.
```

### 6.5 Check-in (C1)

```sql
UPDATE registrations
SET checked_in_at = now(), checkin_method = :method,
    checked_in_by = :staff_id, checkin_note = :note, updated_at = now()
WHERE id = :reg_id AND event_id = :event_id
  AND status = 'REGISTERED'
  AND checked_in_at IS NULL          -- chặn check-in trùng
RETURNING id, checked_in_at;
-- Không có dòng trả về → đọc lại để phân biệt ALREADY_CHECKED_IN và NOT_ELIGIBLE_FOR_CHECKIN
-- method = MANUAL → INSERT activity_logs (action 'MANUAL_CHECKIN') trong cùng transaction
-- App kiểm tra student_id <> :staff_id trước → 403 SELF_CHECKIN_NOT_ALLOWED; chk_no_self_checkin là chốt chặn cuối
```

Hai máy quét cùng quét một QR thì PostgreSQL khóa dòng registration đó: câu UPDATE thứ hai chờ, đọc lại điều kiện `checked_in_at IS NULL`, thấy đã có giá trị nên không cập nhật — không cần UNIQUE riêng.

Check-in không đụng bộ đếm nên không khóa dòng `events`: nhiều máy quét cùng lúc không chặn nhau. Mỗi lượt là một lần verify HMAC + một câu UPDATE theo khóa chính, phục vụ mục tiêu check-in trung vị ≤ 5 giây (Q11).

### 6.6 Job nền

| Job | Lịch chạy | Việc làm |
| --- | --- | --- |
| Email outbox | Mỗi 30 giây | Gửi notification `email_status = PENDING`; lỗi thì tăng `email_attempts`, tối đa 5 lần rồi đánh dấu FAILED |
| Đóng waitlist | Mỗi 5 phút | Khóa events; sự kiện đã tới `start_at`: WAITLISTED → EXPIRED, waitlist_count = 0, cùng transaction. Đây là đồng bộ dữ liệu lưu, không quyết định thời điểm hết hiệu lực nghiệp vụ |
| Hoàn tất sự kiện | Mỗi 15 phút | PUBLISHED và quá `end_at` → COMPLETED |

Hai job vòng đời chạy ngay khi worker khởi động, sau đó theo lịch trên. Xử lý batch tối đa 100 event/transaction, khóa event với `FOR UPDATE SKIP LOCKED` trước khi cập nhật registrations để phối hợp nhiều worker và không đảo thứ tự khóa với request. Job hoàn tất cũng đóng waitlist trong cùng transaction, không phụ thuộc job 5 phút chạy trước. Chạy lại không cập nhật/notify trùng; không tạo notification hay email cho EXPIRED/COMPLETED. Hoàn tất tăng event version một lần. Mỗi lượt job có timeout 2 phút; lỗi retry ở tick kế tiếp. Fx shutdown hủy context và đợi job dừng trước khi đóng DB pool.
| Nhắc lịch | Mỗi giờ | Tạo notification REMINDER cho sự kiện bắt đầu trong 24h tới (bổ trợ, Q1) |

## 7. Truy vết yêu cầu & tiêu chí nghiệm thu

Mỗi mục tiêu thành công thầy đã đồng ý ở Q11 có một chốt chặn trong DB và một endpoint để kiểm thử, nên nhóm có thể viết test nghiệm thu trực tiếp từ bảng dưới.

| Mục tiêu (Q11) | Chốt chặn trong DB | Endpoint | Test nghiệm thu đề xuất |
| --- | --- | --- | --- |
| Không đăng ký trùng | `uq_active_registration` | R1 | Gửi R1 hai lần song song cùng sinh viên → 1 thành công, 1 `409 ALREADY_REGISTERED` |
| Không vượt số chỗ | `chk_capacity` + khóa dòng `events` | R1, E5 | 200 request R1 đồng thời vào sự kiện 50 chỗ → đúng 50 REGISTERED, 150 WAITLISTED |
| Waitlist FIFO | `queue_seq` + `idx_waitlist_head` | R2, R5 | Hủy 3 đăng ký → 3 người đầu waitlist được promote đúng thứ tự `queue_seq` |
| Check-in ≤ 5 giây (trung vị), chặn trùng | Điều kiện `checked_in_at IS NULL` trong câu UPDATE check-in | C1 | Đo 100 lượt quét; quét lại cùng QR → `409 ALREADY_CHECKED_IN` |
| Có phương án thủ công khi QR lỗi | `checkin_method` CODE / MANUAL | C1 | Check-in bằng mã 8 ký tự và bằng xác nhận tay có `note` |
| 500 lượt đăng ký: tổng hợp + xuất ≤ 30 giây, khớp nguồn | `idx_reg_event`, `idx_reg_checked_in` | B1, B2 | Seed 500 đăng ký; đo thời gian B2; so số dòng file với B1 |
| Sự kiện chưa duyệt không công bố được | `event_status` + `chk_no_self_approve` | E1, P3 | Sự kiện PENDING không xuất hiện ở E1; user vừa là Admin vừa là người tạo tự duyệt → `403 SELF_APPROVAL_NOT_ALLOWED` |
| Đạt mọi kiểm thử phân quyền | `unit_memberships` | Tất cả | Organizer đơn vị A gọi R5 của đơn vị B → `404`; Admin (không được gán đơn vị) xem được R5 toàn trường nhưng C1/C3 → `403`; sinh viên gọi R4 của người khác → `404`; user chưa có MSSV gọi R1 → `403`; Organizer kiêm sinh viên đăng ký được (R1 → `201`) nhưng tự check-in → `403 SELF_CHECKIN_NOT_ALLOWED` |

| Quy tắc khác (nguồn) | Nơi hiện thực |
| --- | --- |
| Hạn hủy 24h + ngoại lệ Q19 | R2, `cancelDeadline` và `cancellation_exception_until` trong registration |
| Gửi email lỗi vẫn giữ đăng ký (Q14) | Outbox `notifications.email_status`, mục 6.3; INSERT outbox lỗi vẫn rollback transaction |
| Đăng ký lại sau hủy, vào cuối waitlist (Q15) | Partial index + `queue_seq` mới cho dòng mới |
| Sửa/hủy sau công bố + notify (Q19) | E5, E7, `activity_logs` (`UPDATE_EVENT`, `CANCEL_EVENT`) |
| Phân quyền xem dữ liệu (Q20) | Mục 6.1 |
| Không check-out, không SSO ngoài Google, không app native | Không có bảng/endpoint tương ứng; Google OAuth 2.0 / OIDC thay login mô phỏng |

**Quyết định nhóm cập nhật 24/10:** vai trò người dùng là chuyên biệt hóa chồng lấn, bỏ cột `users.role`, thêm `users.is_admin` và `chk_no_self_checkin`; Organizer kiêm sinh viên được đăng ký nhưng không tự check-in; MSSV lấy từ danh sách import; Organizer theo đơn vị; Admin cấp trường không check-in, nâng Admin chỉ bởi đội dev qua quy trình nội bộ; thay đổi thời gian/địa điểm trong 24h cuối mở ngoại lệ tự hủy cho đăng ký có trước thay đổi; khóa sửa thời gian/địa điểm 60 phút cuối. Google OAuth được nhóm tự chọn dù Q02/Q10 không bắt buộc tích hợp SSO thật; đây là thay đổi lựa chọn triển khai, cần TV1 xác nhận cập nhật baseline.
