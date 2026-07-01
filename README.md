# Kiến trúc

Go + gin (router), mongo-go-driver

JWT cho auth, phân quyền theo role

Cấu trúc thư mục layer: models, handlers, services, middleware, routes

# Domain MVP:

User: role = manager | tenant, do manager tạo tài khoản cấp cho user

Room: mã phòng, giá thuê, trạng thái, người thuê hiện tại

Invoice: hóa đơn theo tháng — tiền nhà + điện + nước, trạng thái (chưa/đã thanh toán)

Payment: lịch sử thanh toán gắn với invoice

# Run

go run cmd/server/main.go
http://localhost:8080/swagger/index.html

# Run bằng Docker (khuyến nghị - tự kèm MongoDB)

docker compose up --build
http://localhost:8080/swagger/index.html
http://localhost:8080/healthz

Sửa biến môi trường (JWT_SECRET, MONGO_URI...) trong `docker-compose.yml` hoặc file `.env`
(xem mẫu ở `.env.example`) trước khi deploy thật. Khi deploy production, set `APP_ENV=production`
và `JWT_SECRET` là chuỗi ngẫu nhiên ≥32 ký tự — app sẽ từ chối khởi động nếu thiếu.
