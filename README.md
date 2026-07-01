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
