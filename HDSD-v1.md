# Hướng dẫn sử dụng MowfNet v1 — M1 Subnet Core

Tài liệu này mô tả đúng phạm vi hiện có sau M1: REST API quản lý Subnet. Phiên bản hiện tại chưa có frontend; khi mở URL bằng trình duyệt, bạn sẽ xem dữ liệu JSON do REST API trả về.

## 1. Yêu cầu

- Go 1.23.6 hoặc phiên bản tương thích mới hơn (theo `go.mod`).
- PostgreSQL đang chạy trên máy local.
- `psql` và `pg_isready` để khởi tạo, kiểm tra database.
- `curl` để gọi REST API từ terminal.

Kiểm tra các công cụ:

```bash
go version
psql --version
curl --version
```

Chạy các lệnh trong tài liệu từ root repository.

## 2. Database local mặc định

Nếu không đặt biến môi trường, server dùng cấu hình sau:

| Thuộc tính | Giá trị mặc định |
|---|---|
| Host | `127.0.0.1` |
| Port | `5432` |
| User | `postgres` |
| Password | `postgres` |
| Database | `mowf_net` |
| SSL mode | `disable` |

### Kiểm tra và khởi động PostgreSQL

Kiểm tra PostgreSQL có nhận kết nối hay không:

```bash
pg_isready -h 127.0.0.1 -p 5432
```

Trên Linux dùng systemd, kiểm tra service và khởi động nếu chưa chạy:

```bash
systemctl status postgresql
sudo systemctl start postgresql
```

Sau đó chạy lại `pg_isready`. Kết quả sẵn sàng thường chứa `accepting connections`.

### Khởi tạo database lần đầu

Project mặc định kết nối bằng user `postgres` với password `postgres`. Nếu PostgreSQL local chưa có cấu hình này, đặt password:

```bash
sudo -u postgres psql -c "ALTER USER postgres WITH PASSWORD 'postgres';"
```

Tạo database (bỏ qua lệnh này nếu `mowf_net` đã tồn tại):

```bash
sudo -u postgres createdb mowf_net
```

Áp dụng các migration theo thứ tự, chỉ trong lần khởi tạo database:

```bash
PGPASSWORD=postgres psql -h 127.0.0.1 -U postgres -d mowf_net -f migrations/000001_create_vlans_table.up.sql
PGPASSWORD=postgres psql -h 127.0.0.1 -U postgres -d mowf_net -f migrations/000002_create_subnets_table.up.sql
PGPASSWORD=postgres psql -h 127.0.0.1 -U postgres -d mowf_net -f migrations/000003_create_ip_allocations_table.up.sql
```

Server hiện không tự chạy migration khi khởi động. Không cần áp dụng lại các lệnh trên ở những lần chạy sau nếu schema đã được tạo.

## 3. Chạy server

```bash
go run ./cmd/server
```

Khi server đã chạy, API Subnet có tại:

[http://localhost:8080/api/v1/subnets](http://localhost:8080/api/v1/subnets)

Giữ terminal chạy server mở. Dùng một terminal khác cho các lệnh `curl` bên dưới.

## 4. Sử dụng Subnet API

Các ví dụ giả sử database chưa có Subnet trùng với `192.168.10.0/24`. ID được database cấp tự động; thay `1` trong các URL bên dưới bằng trường `data.id` nhận được từ lệnh Create.

### Create — tạo Subnet

```bash
curl -i -X POST http://localhost:8080/api/v1/subnets \
  -H 'Content-Type: application/json' \
  -d '{
    "cidr": "192.168.10.0/24",
    "vlan_ref_id": null,
    "description": "Lab LAN"
  }'
```

Kết quả thành công là `201 Created`. Response chứa Subnet cùng các giá trị được tính như network, broadcast, dải usable và số lượng IP.

### List — liệt kê Subnet

```bash
curl -i 'http://localhost:8080/api/v1/subnets'
```

Có thể giới hạn số phần tử hoặc tìm theo CIDR/description:

```bash
curl -i 'http://localhost:8080/api/v1/subnets?limit=10&search=192.168.10'
```

### Get — lấy một Subnet

```bash
curl -i http://localhost:8080/api/v1/subnets/1
```

### PATCH — cập nhật thông tin Subnet

Chỉ gửi những trường cần đổi. Ví dụ cập nhật description và gỡ liên kết VLAN:

```bash
curl -i -X PATCH http://localhost:8080/api/v1/subnets/1 \
  -H 'Content-Type: application/json' \
  -d '{
    "description": "Lab LAN - updated",
    "vlan_ref_id": null
  }'
```

Các trường PATCH hiện được hỗ trợ là `cidr`, `description` và `vlan_ref_id`.

### Resize — đổi kích thước Subnet

Resize được thực hiện bằng PATCH trường `cidr`; không có endpoint Resize riêng. CIDR mới phải là IPv4 canonical từ `/1` đến `/30`, không được overlap Subnet khác và phải chứa mọi IP allocation hiện có trong usable range mới.

Ví dụ thu nhỏ `/24` thành `/25`:

```bash
curl -i -X PATCH http://localhost:8080/api/v1/subnets/1 \
  -H 'Content-Type: application/json' \
  -d '{"cidr":"192.168.10.0/25"}'
```

### DELETE — xóa Subnet

```bash
curl -i -X DELETE http://localhost:8080/api/v1/subnets/1
```

Kết quả thành công là `204 No Content`. Subnet có IP allocation đã lưu sẽ không thể bị xóa.

## 5. Ví dụ lỗi

API trả lỗi theo envelope `error` gồm `code`, `message` và `details`.

### `SUBNET_OVERLAP`

Sau khi đã tạo `192.168.10.0/24`, thử tạo một Subnet nằm bên trong dải đó:

```bash
curl -i -X POST http://localhost:8080/api/v1/subnets \
  -H 'Content-Type: application/json' \
  -d '{
    "cidr": "192.168.10.128/25",
    "vlan_ref_id": null,
    "description": "Overlapping subnet"
  }'
```

Kết quả là `409 Conflict`:

```json
{
  "error": {
    "code": "SUBNET_OVERLAP",
    "message": "The subnet overlaps with an existing subnet.",
    "details": {}
  }
}
```

### `INVALID_CIDR`

Ví dụ dưới đây dùng CIDR không canonical vì phần host của địa chỉ không bằng 0:

```bash
curl -i -X POST http://localhost:8080/api/v1/subnets \
  -H 'Content-Type: application/json' \
  -d '{
    "cidr": "192.168.20.1/24",
    "vlan_ref_id": null,
    "description": "Invalid CIDR example"
  }'
```

Kết quả là `400 Bad Request`:

```json
{
  "error": {
    "code": "INVALID_CIDR",
    "message": "The provided CIDR is invalid or non-canonical.",
    "details": {}
  }
}
```

### `SUBNET_NOT_FOUND`

Gọi một ID không tồn tại:

```bash
curl -i http://localhost:8080/api/v1/subnets/999999999
```

Kết quả là `404 Not Found`:

```json
{
  "error": {
    "code": "SUBNET_NOT_FOUND",
    "message": "The requested subnet was not found.",
    "details": {}
  }
}
```

## 6. Dừng và chạy lại

Tại terminal đang chạy server, nhấn `Ctrl+C`. Server sẽ dừng graceful và in thông báo đã dừng thành công.

Ở lần sử dụng tiếp theo:

1. Kiểm tra PostgreSQL bằng `pg_isready -h 127.0.0.1 -p 5432` và khởi động service nếu cần.
2. Không chạy lại migration nếu database đã được khởi tạo.
3. Từ root repository, chạy lại:

```bash
go run ./cmd/server
```

Dữ liệu đã tạo vẫn nằm trong PostgreSQL local. Phiên bản M1 hiện chỉ có REST API Subnet; chưa có frontend, nên browser chỉ hiển thị REST JSON tại các URL GET.
