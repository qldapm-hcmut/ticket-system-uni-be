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
# Cần psql; dùng email Admin thật, không commit vào repository:
make seed ADMIN_EMAIL='admin@hcmut.edu.vn'
# DB kết nối chỉ dùng để tạo database test riêng; role cần CREATEDB:
TEST_DATABASE_URL='postgres://ticket:ticket_local@localhost:5432/ticket?sslmode=disable' make test-int
```

Integration test tạo database tên ngẫu nhiên, chạy up/down/up và kiểm tra constraint, sau đó xóa database test; không chạy down lên database trong URL. Thiếu `TEST_DATABASE_URL` thì test báo lỗi, không âm thầm skip. Đã chạy thành công integration test trên PostgreSQL 16 qua Podman. Seed cũng đã chạy hai lần trên database tạm, xác nhận không tạo trùng user và audit bootstrap.

Seed không nhận Google sub, không seed MSSV demo vào DB thật. Nếu email đã tồn tại mà chưa là Admin, seed không nâng quyền; dùng quy trình nâng quyền có audit riêng. Query liên kết Google chỉ là primitive: adapter auth phải xác minh OIDC, email, active và thực hiện transaction đúng contract trước khi gọi.

## Import MSSV cần triển khai

- Bootstrap đơn vị và email Admin thật `@hcmut.edu.vn`, không seed mật khẩu.
- Import danh sách sinh viên gồm email trường, họ tên, MSSV; kiểm tra trùng email/MSSV, ghi audit và hỗ trợ bổ sung MSSV cho user đã đăng nhập.
- User bootstrap chưa có `google_sub` chỉ được liên kết sau khi xác minh đầy đủ Google ID token.
- User chưa có MSSV không có vai trò STUDENT; không tự cấp quyền khi đăng nhập Google.
- Admin được cấp/thu hồi bởi quy trình nội bộ có audit, không qua API công khai.

Import MSSV và email dispatcher chưa được triển khai. Test schema nằm trong `db/schema_integration_test.go`; test persistence/use case và concurrency hiện có nằm trong `internal/*/adapter/postgres/*_integration_test.go`. Sự hiện diện của test không thay thế việc chạy lại và đối chiếu độ bao phủ với contract.
