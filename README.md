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

## Hướng dẫn chạy backend và kiểm thử API local

Hướng dẫn này dành cho người kiểm thử (TV2) và developer: chạy backend, tạo ba tài khoản demo và gọi `/api/v1/me` mà **không cần frontend hoặc tài khoản Google thật**. Chạy các lệnh từ thư mục gốc backend, trên Linux/macOS hoặc Bash trong WSL. Windows nên dùng WSL2; không chạy nguyên các lệnh Bash trong PowerShell.

### 1. Chuẩn bị công cụ và lấy code

| Công cụ | Mục đích |
| --- | --- |
| Git | Lấy code |
| Go theo `go.mod` | Chạy API, worker và công cụ tạo token; cho phép `GOTOOLCHAIN=auto` tải toolchain nếu cần |
| GNU Make, Bash | Chạy các lệnh `make` |
| Podman + Compose provider **hoặc** Docker Engine/Desktop + Compose | Chạy PostgreSQL 16 |
| CLI `migrate` (golang-migrate), có driver PostgreSQL | Áp dụng migration; cài CLI, không phải một package Python cùng tên |
| OpenSSL, curl | Sinh khóa và kiểm tra HTTP |
| `jq` (tùy chọn) | Đọc JSON dễ hơn |

`psql` chỉ cần khi dùng `make seed`; `sqlc` chỉ cần khi sửa/sinh lại query. Không bắt buộc hai công cụ này cho luồng demo dưới đây. Cách thiết lập DB chi tiết ở [`db/README.md`](db/README.md).

```sh
git clone git@github.com:qldapm-hcmut/ticket-system-uni-be.git
cd ticket-system-uni-be
git switch main
git pull --ff-only

go version
make --version
migrate -version
openssl version
curl --version
# Chọn một bộ công cụ đang dùng:
podman compose version
# hoặc: docker compose version
```

Nếu đã có repo, không clone lại. Kiểm tra `git status` và lưu công việc đang làm trước khi chuyển nhánh; không reset/xóa thay đổi để chạy hướng dẫn. Tên thư mục clone cũ như `uni-ticket-sys-be` vẫn dùng được cho backend; tên thư mục frontend trong phần Docker full-stack phải khớp build context.

### 2. Tạo cấu hình local

```sh
# Chỉ chạy khi chưa có .env; không ghi đè cấu hình hiện có.
cp -n .env.example .env
openssl rand -hex 32
openssl rand -hex 32
```

Dán **hai kết quả khác nhau** vào `JWT_SECRET` và `QR_HMAC_SECRET` trong `.env`. Cấu hình tối thiểu (các chỗ `<...>` phải được thay bằng giá trị thật trên máy bạn):

```dotenv
HTTP_ADDR=:3000
AUTH_MODE=local
DATABASE_URL=postgres://ticket:ticket_local@localhost:5432/ticket?sslmode=disable
POSTGRES_PASSWORD=ticket_local
POSTGRES_PORT=5432
GOOGLE_CLIENT_ID=
JWT_SECRET=<ket-qua-openssl-thu-nhat>
QR_HMAC_SECRET=<ket-qua-openssl-thu-hai>
```

- `AUTH_MODE=local` bỏ bước Google discovery; **không** bỏ kiểm tra chữ ký JWT, tài khoản hoạt động hay phân quyền.
- Mật khẩu `ticket_local` chỉ dành cho DB local. Nếu đổi, sửa cả `POSTGRES_PASSWORD` và mật khẩu trong `DATABASE_URL`.
- Nếu cổng 5432 đã dùng, chọn ví dụ `POSTGRES_PORT=55432` và đổi port trong `DATABASE_URL` thành `55432`.
- Không thêm dấu nháy hoặc khoảng trắng quanh giá trị secret. Make tự nạp `.env`; sửa file xong phải khởi động lại API nếu đổi cấu hình.
- Không commit `.env`, không dán token/secret lên PR, chat công khai hoặc ảnh chụp kết quả test.

### 3. Chạy database và migration

**Podman** (mặc định của Makefile):

```sh
make db-up
make migrate-up
```

Nếu dùng **Docker**, thay lệnh khởi động DB bằng:

```sh
make db-up COMPOSE="docker compose"
make migrate-up
```

Chỉ dùng **một** container runtime cho cùng lần chạy. `make migrate-up` đọc `DATABASE_URL` từ `.env`; database phải sẵn sàng trước khi chạy. Kết quả `no change` nghĩa là database đã có các migration hiện tại.

**Không cần `make seed ADMIN_EMAIL=...` để tạo ba tài khoản demo.** Lệnh `make dev-token` ở bước 5 tự nạp `db/seed/demo.sql`. `make seed` là luồng bootstrap Admin riêng, không phải dữ liệu demo.

### 4. Chạy API và worker

Mở hai terminal, cả hai ở thư mục gốc backend:

```sh
# Terminal A: giữ chạy trong lúc test
make run
```

```sh
# Terminal B: giữ chạy để cập nhật vòng đời sự kiện
make worker
```

Ở terminal C:

```sh
curl -i http://localhost:3000/health/live
curl -i http://localhost:3000/health/ready
```

Cả hai cần trả HTTP `200`. `/health/live` kiểm tra HTTP process; `/health/ready` kiểm tra kết nối PostgreSQL. Worker không mở cổng HTTP và có thể không có log khi chưa có dữ liệu cần xử lý. Không chạy thêm API trên cùng port 3000.

### 5. Tạo access token cho ba vai trò

Vẫn tại terminal C, chạy bằng Bash:

```sh
STUDENT_TOKEN="$(make --no-print-directory -s dev-token ROLE=student)" || exit 1
ORGANIZER_TOKEN="$(make --no-print-directory -s dev-token ROLE=organizer)" || exit 1
ADMIN_TOKEN="$(make --no-print-directory -s dev-token ROLE=admin)" || exit 1
```

Các lệnh lưu token vào biến của **terminal hiện tại**, không in token ra hướng dẫn/kết quả test. Khi chuyển terminal phải tạo/nạp lại biến. Công cụ chỉ cho phép `AUTH_MODE=local` và database host là `localhost`, `127.0.0.1` hoặc `::1`; chạy từ thư mục gốc backend để đọc được file seed.

| ROLE | Email giả lập | Kết quả mong đợi |
| --- | --- | --- |
| `student` | `student.demo@hcmut.edu.vn` | `roles` chứa `STUDENT`, mã sinh viên `DEMO0001` |
| `organizer` | `organizer.demo@hcmut.edu.vn` | `roles` chứa `ORGANIZER`, thuộc đơn vị có code `DEMO` / tên **Demo Event Unit** |
| `admin` | `admin.demo@hcmut.edu.vn` | `roles` chứa `ADMIN`, `isAdmin=true` |

Token demo có hạn **8 giờ**, chỉ là access token, không có refresh token. Hết hạn thì chạy lại lệnh. Có thể chạy seed lại: dữ liệu đã tồn tại không bị tự đổi thông tin/quyền; vì vậy nếu tài khoản demo từng bị sửa quyền hoặc vô hiệu hóa, cần kiểm tra dữ liệu thay vì giả định chạy lại sẽ khôi phục.

### 6. Xác nhận `/me` bằng cả ba vai trò

```sh
BASE_URL=http://localhost:3000

curl -i "$BASE_URL/api/v1/me" \
  -H "Authorization: Bearer $STUDENT_TOKEN"

curl -i "$BASE_URL/api/v1/me" \
  -H "Authorization: Bearer $ORGANIZER_TOKEN"

curl -i "$BASE_URL/api/v1/me" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

Cả ba phải trả HTTP `200`. Thông tin user nằm trong `data`: đối chiếu `email`, `roles`, `isActive=true`; Organizer có đơn vị `DEMO` trong `units`, Admin có `isAdmin=true`. ID là UUID phát sinh trên từng database, không copy cố định từ máy khác.

Kiểm tra âm tính: gọi không có token phải trả HTTP `401`:

```sh
curl -i "$BASE_URL/api/v1/me"
```

Để gọi endpoint khác trong Postman/Insomnia, chọn **Bearer Token**, dán token của vai trò tương ứng (không thêm chữ `Bearer` vào ô token). Lấy token bằng `make dev-token ROLE=student` hoặc vai trò cần dùng; tránh chia sẻ token trong ảnh chụp. Khi tạo sự kiện, dùng Organizer và chọn đơn vị `DEMO`; Admin không tự có quyền Organizer.

**Môi trường sẵn sàng khi:** hai health endpoint trả `200`, `/me` trả `200` đúng ba vai trò, và request không có token trả `401`. Điều này chỉ xác nhận môi trường/auth, chưa xác nhận toàn bộ luồng đăng ký, check-in, báo cáo hay frontend.

### 7. Lỗi thường gặp

| Hiện tượng | Kiểm tra / cách xử lý |
| --- | --- |
| `make: command not found`, không chạy được Bash | Cài GNU Make/Bash; Windows dùng terminal WSL |
| Không tìm thấy `migrate` | Cài golang-migrate CLI có PostgreSQL driver, thêm thư mục executable vào `PATH` |
| Go báo toolchain không phù hợp | Kiểm tra `go.mod`, bật `GOTOOLCHAIN=auto`, cho phép mạng để tải toolchain; xem mục Go toolchain bên dưới |
| Podman báo lỗi socket/provider | Cài Compose provider; trên Linux nếu cần chạy `systemctl --user start podman.socket`; Docker dùng `COMPOSE="docker compose"` |
| Port 5432 hoặc 3000 đã dùng | Đổi cổng DB đồng bộ như bước 2; với API đổi `HTTP_ADDR` và URL curl tương ứng |
| DB báo authentication failed | Đối chiếu URL/password/port. Đổi `.env` không đổi mật khẩu đã lưu trong volume cũ; không xóa volume có dữ liệu cần giữ |
| API báo secret không đủ dài | Sinh hai khóa bằng OpenSSL, điền `.env`, khởi động lại API; không dùng placeholder `<...>` |
| `dev-token requires AUTH_MODE=local` | Đặt đúng `AUTH_MODE=local`; kiểm tra đang chạy từ đúng repo và `.env` |
| `dev-token requires a loopback database host` | Chạy công cụ trên máy host và dùng `DATABASE_URL=...@localhost:<port>/...`; không dùng hostname `postgres` của mạng container |
| Seed báo relation/table không tồn tại | Chạy `make migrate-up` trên đúng database trước khi tạo token |
| `/me` trả `401` | Kiểm tra biến token không rỗng, chưa hết hạn, API và công cụ tạo token dùng cùng `JWT_SECRET` và DB; tài khoản phải còn active |
| Gọi API nghiệp vụ trả `403` hoặc `404` | Kiểm tra vai trò và phạm vi đơn vị; token hợp lệ không có nghĩa được thao tác mọi sự kiện |
| Local demo gọi `/auth/google` không đăng nhập được | Đây không phải đường đăng nhập demo; dùng access token. Muốn test Google thật phải cấu hình chế độ Google bên dưới |

### 8. Dừng dịch vụ và gửi kết quả kiểm thử

- Dừng API/worker bằng `Ctrl+C` tại terminal tương ứng.
- Dừng DB bằng `make db-down`; Docker dùng `make db-down COMPOSE="docker compose"`. Volume được giữ lại. Không dùng `down -v`, `migrate-down` hoặc xóa database để xử lý lỗi khi chưa xác nhận dữ liệu có thể bỏ.
- Xóa token khỏi terminal khi xong: `unset STUDENT_TOKEN ORGANIZER_TOKEN ADMIN_TOKEN`.
- Khi gửi kết quả, ghi commit đang test (`git rev-parse --short HEAD`), phiên bản công cụ, lệnh đã chạy, HTTP status, vai trò và lỗi (nếu có). Lưu bằng chứng theo hướng dẫn của repo `pm-docs` tại `docs/quality/results/`; không lưu token/secret trong bằng chứng. Không ghi Pass cho bước chưa chạy.

## Tham chiếu cấu hình local và Google OIDC

Cần Go theo `go.mod`, Make và Podman với Compose provider (`podman compose`). Các lệnh database/code generation cần thêm CLI `migrate`, `sqlc` và `psql`; hướng dẫn tại [`db/README.md`](db/README.md).

| Biến | API | Worker | Quy tắc |
| --- | --- | --- | --- |
| `DATABASE_URL` | Bắt buộc | Bắt buộc | Kết nối PostgreSQL; không có mặc định ở ứng dụng |
| `HTTP_ADDR` | Tùy chọn | Được kiểm tra bởi config dùng chung | Mặc định `:3000`, định dạng `host:port`; worker không mở HTTP |
| `AUTH_MODE` | Tùy chọn | Không dùng | `google` mặc định; `local` chỉ dành cho demo |
| `GOOGLE_CLIENT_ID` | Bắt buộc khi `AUTH_MODE=google` | Không dùng | Google OAuth client ID, không có khoảng trắng đầu/cuối |
| `JWT_SECRET` | Bắt buộc | Không dùng | Khóa ký token, ít nhất 32 byte không tính khoảng trắng đầu/cuối |
| `QR_HMAC_SECRET` | Bắt buộc | Không dùng | Khóa ký QR độc lập, ít nhất 32 byte không tính khoảng trắng đầu/cuối |

`POSTGRES_PASSWORD` và `POSTGRES_PORT` dành cho Compose local; giữ chúng đồng bộ với `DATABASE_URL`. Tạo hai khóa độc lập bằng cách chạy `openssl rand -hex 32` hai lần rồi điền từng kết quả vào `.env`. Ứng dụng dùng chuỗi khóa nguyên văn, không giải mã hex/base64. Dùng giá trị không có khoảng trắng, không bọc dấu nháy để phù hợp cách Make nạp file. Không dùng các khóa mẫu chung.

Với Google, cấu hình OAuth consent screen/audience và OAuth client cho frontend (thường là Web application), khai báo authorized JavaScript origins thực tế. Điền client ID vào `GOOGLE_CLIENT_ID`, không phải client secret. Frontend gửi Google **ID token** đến `POST /api/v1/auth/google`; tài khoản phải có email xác minh thuộc `hcmut.edu.vn` và claim `hd=hcmut.edu.vn`. Luồng này không yêu cầu Google client secret hay callback URL ở backend. API cần kết nối HTTPS ra Google cho OIDC discovery lúc khởi động và lấy signing keys khi xác minh.

```sh
cp .env.example .env
# Giữ AUTH_MODE=local cho demo; điền JWT_SECRET, QR_HMAC_SECRET và cấu hình DB local.
make db-up
make migrate-up
make run
```

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

Phần này cần frontend có `Dockerfile`, `nginx.conf` và chức năng demo login (được cung cấp trong PR frontend #6). Kiểm tra các file/chức năng này đã có trên nhánh frontend đang dùng; không giả định backend đã merge nghĩa là frontend cũng đã sẵn sàng. Chỉ test API thì dùng hướng dẫn phía trên, không cần chạy full-stack.

Đặt hai repo cạnh nhau dưới tên `ticket-system-uni-be` và `ticket-system-uni-fe`:

```text
qldapm/
├── ticket-system-uni-be/
└── ticket-system-uni-fe/
```

Clone frontend nếu chưa có: `git clone git@github.com:qldapm-hcmut/ticket-system-uni-fe.git` từ thư mục cha. Build context trong `compose.host.yaml` trỏ đến `../ticket-system-uni-fe`; tên thư mục backend không bắt buộc nhưng đường dẫn frontend phải khớp.

Dừng API/worker chạy trực tiếp nếu chuyển sang mô hình container để tránh dùng nhầm instance. Không chạy song song hai Compose project trên cùng cổng DB.

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
Đăng nhập local bằng access token demo như hướng dẫn ở trên. Chạy `make dev-token` **trên máy host** với `DATABASE_URL` trỏ đến cổng PostgreSQL publish ở loopback, cùng database và `JWT_SECRET` với API container. Không dùng `DOCKER_DATABASE_URL` (host `postgres`) cho công cụ này.

API container không publish cổng 3000 ra host trong cấu hình này. Kiểm tra API qua Nginx:

```sh
curl -i http://localhost:5174/api/v1/me \
  -H "Authorization: Bearer $STUDENT_TOKEN"
docker compose -f compose.yaml -f compose.host.yaml ps
docker compose -f compose.yaml -f compose.host.yaml logs --tail=100 api worker web
# Dừng full-stack, giữ volume:
docker compose -f compose.yaml -f compose.host.yaml down
```

Thay `5174` nếu đổi `WEB_PORT`. Tạo token vẫn cần Go/Make trên host; image runtime không chứa công cụ `dev-token`. Không coi URL HTTP LAN là đủ để mở camera: quét QR bằng camera điện thoại cần secure context như HTTPS tin cậy nêu trên.

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
