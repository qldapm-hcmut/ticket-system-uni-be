# University Event Registration Backend

Backend hệ thống đăng ký sự kiện trường đại học — CO3011 · L01 · Nhóm 09.

## Tổng quan

Dự án được thiết kế để phục vụ luồng **tạo và duyệt sự kiện → đăng ký hoặc vào danh sách chờ → check-in → báo cáo** trong một trường đại học.

Phạm vi thiết kế gồm:

- Xác thực tài khoản trường bằng Google OpenID Connect, sử dụng JWT và refresh session nội bộ.
- Quản lý đơn vị tổ chức và phân quyền Student, Organizer, Admin; một tài khoản có thể có nhiều vai trò.
- Quản lý vòng đời sự kiện và quy trình duyệt trước khi công bố.
- Đăng ký theo sức chứa, danh sách chờ FIFO và tự động chuyển người khi có chỗ.
- Check-in bằng QR, mã nhập tay hoặc xác nhận thủ công.
- Thông báo in-app, email bất đồng bộ, nhật ký thao tác và xuất báo cáo.

Đây là phạm vi thiết kế. Quy tắc chi tiết và giới hạn phạm vi nằm trong tài liệu nghiệp vụ.

## Công nghệ và kiến trúc

- **Go**, **Echo v4**: HTTP API.
- **Uber Fx**: dependency injection và lifecycle.
- **PostgreSQL 16**, **pgx/v5**: lưu trữ và transaction.
- **sqlc**: sinh lớp truy vấn Go từ SQL viết tay.
- **golang-migrate**: quản lý schema có phiên bản.
- **Podman + Compose provider**: PostgreSQL local.

Phiên bản Go và dependency chính xác được khai báo trong [`go.mod`](go.mod).

Kiến trúc chia theo module, áp dụng Clean Architecture có chọn lọc với hướng phụ thuộc `adapter → app → domain`. Domain/app không phụ thuộc framework HTTP hoặc driver database. Các lớp chỉ được tạo khi có nhu cầu triển khai thực tế.

| Đường dẫn | Vai trò |
| --- | --- |
| `cmd/api/`, `cmd/worker/` | Composition roots cho HTTP API và worker |
| `internal/<module>/` | Use case và adapter của từng module; domain khi cần |
| `internal/platform/` | Hạ tầng, config, server và lifecycle |
| `internal/shared/kernel/` | Kiểu và abstraction dùng chung |
| `db/migrations/` | Schema có phiên bản, migration up/down |
| `db/queries/` | SQL nguồn cho sqlc |
| `internal/db/` | Code sqlc sinh tự động, không sửa tay |
| `db/seed/` | Dữ liệu bootstrap |
| `api/` | API contract |
| `docs/` | Thiết kế và hướng dẫn phát triển |

## Tài liệu chuẩn

| Nội dung | Nguồn |
| --- | --- |
| Nghiệp vụ, phân quyền, mô hình dữ liệu, tiêu chí nghiệm thu | [`docs/db-schema-and-api-contract.md`](docs/db-schema-and-api-contract.md) |
| Path, request/response và mã lỗi API | [`api/openapi.yaml`](api/openapi.yaml) |
| Kiến trúc, transaction và quy tắc phát triển/test | [`docs/development.md`](docs/development.md) |
| Thiết lập database, seed và integration test | [`db/README.md`](db/README.md) |
| Index tra cứu theo công việc cho agent | [`docs/AGENTS.md`](docs/AGENTS.md) |

## Thiết lập local

Cần Go theo `go.mod`, Make và Podman với Compose provider (`podman compose`). Các lệnh database/code generation cần thêm CLI `migrate`, `sqlc` và `psql`; hướng dẫn tại [`db/README.md`](db/README.md).

| Biến | API | Worker | Quy tắc |
| --- | --- | --- | --- |
| `DATABASE_URL` | Bắt buộc | Bắt buộc | Kết nối PostgreSQL; không có mặc định ở ứng dụng |
| `HTTP_ADDR` | Tùy chọn | Được kiểm tra bởi config dùng chung | Mặc định `:3000`, định dạng `host:port`; worker không mở HTTP |
| `GOOGLE_CLIENT_ID` | Bắt buộc | Không dùng | Google OAuth client ID, không có khoảng trắng đầu/cuối |
| `JWT_SECRET` | Bắt buộc | Không dùng | Khóa ký token, ít nhất 32 byte không tính khoảng trắng đầu/cuối |
| `QR_HMAC_SECRET` | Bắt buộc | Không dùng | Khóa ký QR độc lập, ít nhất 32 byte không tính khoảng trắng đầu/cuối |

`POSTGRES_PASSWORD` và `POSTGRES_PORT` dành cho Compose local; giữ chúng đồng bộ với `DATABASE_URL`. Tạo hai khóa độc lập bằng cách chạy `openssl rand -hex 32` hai lần rồi điền từng kết quả vào `.env`. Ứng dụng dùng chuỗi khóa nguyên văn, không giải mã hex/base64. Dùng giá trị không có khoảng trắng, không bọc dấu nháy để phù hợp cách Make nạp file. Không dùng các khóa mẫu chung.

Với Google, cấu hình OAuth consent screen/audience và OAuth client cho frontend (thường là Web application), khai báo authorized JavaScript origins thực tế. Điền client ID vào `GOOGLE_CLIENT_ID`, không phải client secret. Frontend gửi Google **ID token** đến `POST /api/v1/auth/google`; tài khoản phải có email xác minh thuộc `hcmut.edu.vn` và claim `hd=hcmut.edu.vn`. Luồng này không yêu cầu Google client secret hay callback URL ở backend. API cần kết nối HTTPS ra Google cho OIDC discovery lúc khởi động và lấy signing keys khi xác minh.

```sh
cp .env.example .env
# Điền GOOGLE_CLIENT_ID, JWT_SECRET, QR_HMAC_SECRET và cấu hình DB local trước khi chạy API.
make db-up
make migrate-up
make run
```

Chạy worker trong terminal khác:

```sh
make worker
```

Make tự nạp `.env` và mặc định `GOTOOLCHAIN=auto`. Khi chạy Go trực tiếp, cần tự export cấu hình môi trường. Không commit `.env`, secret hoặc dữ liệu sinh viên thật. Mật khẩu database mẫu chỉ dành cho local; PostgreSQL trong Compose chỉ bind loopback.

Nếu Compose provider yêu cầu Podman socket:

```sh
systemctl --user start podman.socket
```

`podman compose` có thể dùng plugin docker-compose làm provider nhưng container vẫn chạy bằng Podman, không cần Docker daemon.

Kiểm tra service (port theo cấu hình local):

```sh
curl http://localhost:3000/health/live
curl http://localhost:3000/health/ready
```

- `/health/live`: process đang phục vụ HTTP.
- `/health/ready`: kiểm tra kết nối PostgreSQL, không lộ chi tiết kết nối.
- API và worker dừng qua SIGINT/SIGTERM, có quản lý shutdown qua lifecycle.

## Lệnh phát triển

```sh
make fmt        # định dạng Go
make sqlc       # sinh lại code sau khi sửa SQL/schema liên quan
make lint       # kiểm tra định dạng và static analysis
make test       # unit tests với race detector, không cần database
make test-int   # integration tests với PostgreSQL thật
make build      # build API và worker
make db-down    # dừng PostgreSQL local, giữ volume dữ liệu
```

`make test-int` yêu cầu `TEST_DATABASE_URL` và role có quyền tạo database test. Test database cô lập và seed bootstrap được hướng dẫn tại [`db/README.md`](db/README.md). Chỉ chạy migration down trên database có thể xóa hoặc sau khi đã có kế hoạch bảo vệ dữ liệu.

## Go toolchain và editor

Nếu editor/gopls không tải được module do Go hệ thống cũ hoặc `GOTOOLCHAIN=local`, cho phép chọn toolchain theo `go.mod`:

```sh
go env -w GOTOOLCHAIN=auto
go version
go list ./...
```

Bỏ cấu hình `GOTOOLCHAIN=local` trong shell/editor nếu có và khởi động lại LSP. Lần đầu tải toolchain cần mạng; không hạ phiên bản trong `go.mod` chỉ để né lỗi editor.
