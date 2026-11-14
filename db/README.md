# Database workspace

Schema thực thi chuẩn nằm trong `db/migrations/`, theo baseline 24/10/2026 và API 1.1.0.

- `migrations/0001_initial_schema.up.sql`: initial schema 8 bảng, dùng cho database mới.
- `migrations/0001_initial_schema.down.sql`: rollback initial schema, phá hủy dữ liệu nghiệp vụ; chỉ dùng trên DB thử nghiệm hoặc sau khi đã có kế hoạch sao lưu.
- `queries/auth.sql`, `queries/sessions.sql`: SQL nguồn cho user, liên kết Google identity, quyền và session; use case xác thực nằm trong `internal/auth/`.
- `../sqlc.yaml`, `../internal/db/`: cấu hình và code sinh bằng sqlc v1.30.0, pgx/v5.
- `seed/seed.sql`: bootstrap đơn vị CSE và email Admin do người vận hành cung cấp; chạy lại không tự nâng quyền user đã tồn tại.

Thiết kế và DDL giải thích: [`../docs/db-schema-and-api-contract.md`](../docs/db-schema-and-api-contract.md). API chuẩn: [`../api/openapi.yaml`](../api/openapi.yaml). Khi đổi schema phải đồng bộ phần DDL trong tài liệu; không sửa migration đã merge, hãy thêm migration up/down mới.

## Chạy migration

Cần Podman + Compose provider, PostgreSQL 16, CLI `golang-migrate` (`migrate`) và `DATABASE_URL` trong `.env`. Nếu provider cần socket, chạy `systemctl --user start podman.socket`. Không cần Docker daemon:

```sh
make db-up
make migrate-up
# Chỉ trên DB thử nghiệm: lùi một migration, xóa các bảng và dữ liệu initial schema
make migrate-down
```

Initial migration đã được kiểm chứng up/down/up bằng integration test trên PostgreSQL 16 chạy qua Podman. Không áp dụng initial schema lên database đã có các bảng; cần kiểm tra lịch sử migration và viết ALTER migration phù hợp.

## sqlc, seed và integration test

```sh
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
make sqlc
# Khuyến nghị bootstrap Admin bằng CLI nội bộ ở mục dưới.
# Legacy seed cần psql và đủ 3 biến, không dùng make seed cũ:
psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 \
  -v admin_email='<verified-school-email>' -v operator='<internal-id>' \
  -v ticket='<verification-reference>' -f db/seed/seed.sql
# DB kết nối chỉ dùng để tạo database test riêng; role cần CREATEDB:
TEST_DATABASE_URL='postgres://ticket:ticket_local@localhost:5432/ticket?sslmode=disable' make test-int
```

Integration test tạo database tên ngẫu nhiên, chạy up/down/up và kiểm tra constraint, sau đó xóa database test; không chạy down lên database trong URL. Thiếu `TEST_DATABASE_URL` thì test báo lỗi, không âm thầm skip. Đã chạy thành công integration test trên PostgreSQL 16 qua Podman. Seed cũng đã chạy hai lần trên database tạm, xác nhận không tạo trùng user và audit bootstrap.

Seed không nhận Google sub, không seed MSSV demo vào DB thật. Chỉ user thật sự được INSERT mới nhận audit `GRANT_ADMIN`; user đã là Admin là no-op. Email đã tồn tại nhưng chưa là Admin làm seed thất bại và rollback, không âm thầm nâng quyền hoặc khôi phục quyền đã thu hồi. Seed giữ bootstrap đơn vị CSE; CLI bootstrap chỉ tạo tài khoản. Query liên kết Google chỉ là primitive: adapter auth phải xác minh OIDC, email, active và thực hiện transaction đúng contract trước khi gọi.

## CLI nội bộ: import MSSV và Admin

`cmd/ops` dành cho người vận hành nội bộ đã được đội dev xác minh, **không phải API Admin**. Giới hạn quyền chạy binary và quyền truy cập DB ở hạ tầng. `--operator` là ID truy vết, không phải cơ chế xác thực; ticket phải dẫn tới quy trình xác minh thực tế. Không dùng email/MSSV làm operator/ticket. Không commit roster, dữ liệu thật hoặc thông tin kết nối.

Build/chạy trực tiếp bằng Go; CLI chỉ đọc `DATABASE_URL` từ môi trường tiến trình, không tự nạp `.env` và không cần Google/JWT/QR secrets:

```sh
go build -o /tmp/ticket-ops ./cmd/ops
# DATABASE_URL phải được cấp sẵn qua môi trường an toàn.
/tmp/ticket-ops students import --file /secure/roster.csv \
  --operator '<internal-id>' --ticket '<verification-reference>' --dry-run
/tmp/ticket-ops students import --file /secure/roster.csv \
  --operator '<internal-id>' --ticket '<verification-reference>'
/tmp/ticket-ops admin bootstrap --email '<verified-school-email>' \
  --full-name '<verified-name>' --operator '<internal-id>' --ticket '<reference>'
/tmp/ticket-ops admin grant --user-id '<existing-user-uuid>' \
  --operator '<internal-id>' --ticket '<reference>'
/tmp/ticket-ops admin revoke --user-id '<existing-user-uuid>' \
  --operator '<internal-id>' --ticket '<reference>'
```

### Roster CSV

UTF-8 (chấp nhận BOM), header đúng thứ tự `email,fullName,studentCode`. Ví dụ chỉ dùng dữ liệu giả:

```csv
email,fullName,studentCode
example.student@hcmut.edu.vn,Example Student,0012345
```

- Email là địa chỉ thuần, domain chính xác `hcmut.edu.vn`, so sánh không phân biệt hoa thường.
- Họ tên không rỗng, tối đa 120 ký tự; MSSV không rỗng, tối đa 10 ký tự. MSSV là chuỗi, giữ số 0 đầu; không tự giả định chỉ gồm số.
- File rỗng/sai CSV, trùng email hoặc MSSV bị từ chối trước khi kết nối DB. Không in giá trị dữ liệu; lỗi validation chỉ nêu loại lỗi và dòng/bản ghi.
- Email mới tạo user active, không Admin, chưa có Google sub. Email hiện hữu chưa có MSSV được bổ sung trên đúng UUID đó, kể cả đã đăng nhập Google trước.
- Cùng email/cùng MSSV là unchanged. Email đã mang MSSV khác, hoặc MSSV thuộc UUID khác là conflict: rollback **toàn bộ file**, không merge/chuyển MSSV.
- Giữ nguyên tên hiện hữu, UUID, email, Google sub, quyền Admin, active, memberships, sessions và dữ liệu nghiệp vụ. Tên roster chỉ dùng khi tạo user mới. Không xóa user vắng trong file.
- Import và audit `UPDATE_USER` commit cùng transaction; audit không chứa tên/email/MSSV, dùng before/after trạng thái Student, ticket/operator và batchId. Audit lỗi thì không ghi nhận thay đổi nào.
- `--dry-run` chạy cùng kiểm tra và mutation DB nhưng rollback cả user/audit. Các số đếm là dự kiến, không giữ chỗ trước lần chạy thật. PostgreSQL sequence có thể vẫn tăng dù rollback.
- Thành công in created/enriched/unchanged và committed hoặc dry-run. Rerun cùng dữ liệu không tạo audit mutation giả. Không tự retry khi commit có kết quả không chắc chắn: kiểm tra trạng thái trước rồi mới chạy lại.

User bootstrap chỉ liên kết Google sau khi xác minh đầy đủ ID token. User chưa có MSSV chưa có vai trò STUDENT; import không suy diễn MSSV từ email hoặc Google.

### Bootstrap/grant/revoke Admin

Bootstrap chỉ tạo user mới với `google_sub = NULL`, không mật khẩu. Email đã là Admin là unchanged; email hiện hữu chưa là Admin phải dùng lệnh grant theo UUID sau xác minh. Bootstrap không tự khôi phục quyền đã revoke.

Grant/revoke khóa user và chỉ đổi `is_admin`/`updated_at`, audit `GRANT_ADMIN`/`REVOKE_ADMIN` cùng transaction. Audit có ticket, operator, source và before/after; `actor_id = NULL` vì operator CLI là danh tính hạ tầng, không được giả làm user đích. Trạng thái đã đúng là unchanged, không tạo audit chuyển quyền giả. Không tự mở khóa inactive, không đổi MSSV/Google sub và không gán Organizer. Quyền được phản ánh ở request xác thực tiếp theo; Admin đơn thuần vẫn không được check-in.

Không có rule cấm thu hồi Admin cuối cùng: quy trình nội bộ vẫn có thể grant lại sau xác minh. Không cấp quyền chạy CLI chỉ vì một user có JWT Admin.

### Test operations

Unit/CLI tests không cần DB: `go test -race ./cmd/ops ./internal/ops/...`.

Integration tests có build tag `integration`, tạo database tên riêng rồi xóa, yêu cầu PostgreSQL 16 và role CREATEDB. Cấp `TEST_DATABASE_URL` cho DB test riêng, không dùng DB thật:

```sh
go test -race -tags=integration ./internal/ops/adapter/postgres
```

Seed tests cần `psql`; nếu chỉ có client trong container Podman test, đặt `TEST_PSQL_CONTAINER` bằng tên/ID container test đó. Container phải chứa đúng PostgreSQL instance của `TEST_DATABASE_URL` và cho phép `psql -U postgres` local. Không dùng biến này với container production.

Tests bao phủ bảo toàn identity/quyền, import sau login, conflict/rollback/dry-run, audit failure, concurrent import/login-style insert, Admin transitions và seed rerun. Test schema ở `db/schema_integration_test.go`; sự hiện diện của test không thay thế chạy kiểm chứng.
