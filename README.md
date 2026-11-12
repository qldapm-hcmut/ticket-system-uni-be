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

```sh
cp .env.example .env
# Giữ AUTH_MODE=local, đặt JWT_SECRET và QR_HMAC_SECRET trong .env
make db-up
make migrate-up
make run
```

Tạo hai secret ngẫu nhiên độc lập, mỗi secret ít nhất 32 byte.
Nếu dùng Docker Compose, chạy `make db-up COMPOSE="docker compose"`.

Với `AUTH_MODE=local`, tạo tài khoản demo và lấy access token bằng một trong các lệnh sau.
Lệnh chỉ chạy với database ở localhost. Seed có thể chạy lại và giữ các tài khoản đã tồn tại.

```sh
make dev-token ROLE=student
make dev-token ROLE=organizer
make dev-token ROLE=admin
```

Dán token vào tab **Dán token trực tiếp** ở màn đăng nhập frontend, bỏ trống refresh token.
Token có hiệu lực 8 giờ. Chạy lại lệnh để lấy token mới. Organizer demo thuộc đơn vị
**Demo Event Unit**, chọn đơn vị này khi tạo sự kiện.

Khi dùng Google thật, đặt `AUTH_MODE=google` và điền cùng OAuth Client ID vào
`GOOGLE_CLIENT_ID` của backend và `VITE_GOOGLE_CLIENT_ID` của frontend.

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

## Chạy toàn bộ bằng Docker và mở trên điện thoại

Đặt hai repo cạnh nhau dưới tên `ticket-system-uni-be` và `ticket-system-uni-fe`.
Điền `DOCKER_DATABASE_URL`, `JWT_SECRET`, `QR_HMAC_SECRET` trong `.env`, rồi chạy từ repo backend.

```sh
docker compose -f compose.yaml -f compose.host.yaml up -d --build --wait
```

Compose khởi động PostgreSQL, áp dụng migration, chạy API, worker và frontend Nginx.
Web mặc định mở ở cổng `5174`. Điện thoại cùng Wi-Fi mở `http://<IP-LAN-của-PC>:5174`.
Cho phép TCP 5174 qua firewall trên mạng LAN. `WEB_PORT` đổi cổng và `WEB_BIND_ADDRESS`
chọn địa chỉ bind (mặc định `0.0.0.0`). Frontend chuyển tiếp `/api/v1` tới API trong Docker.

Trên Windows dùng WSL mirrored, mở TCP 5174 trong cả Windows Firewall và Hyper-V Firewall,
giới hạn nguồn theo subnet Wi-Fi. Nếu máy Windows không truy cập được web bằng IP LAN
của chính nó, đặt `hostAddressLoopback=true` dưới `[experimental]` trong `.wslconfig`,
rồi khởi động lại WSL và Docker.

Camera trên điện thoại cần HTTPS với chứng chỉ được tin cậy.
Nếu PC và điện thoại đã dùng Tailscale, có thể tạo địa chỉ HTTPS riêng cho web này.

```sh
tailscale serve --bg --https=8443 http://127.0.0.1:5174
```

Mở URL HTTPS do lệnh trả về trên điện thoại đã kết nối cùng tailnet.
Đăng nhập local bằng access token demo như hướng dẫn ở trên.

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
