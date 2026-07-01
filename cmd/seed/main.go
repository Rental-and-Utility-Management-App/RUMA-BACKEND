package main

// Seed script cho RUMA-BACKEND.
//
// Cách chạy (từ thư mục gốc project, nơi có go.mod và file .env):
//   go run ./cmd/seed
//
// Script sẽ tạo:
//   - 1 tài khoản "manager" (role admin/quản lý trong hệ thống này) — phone: 0900000001, password: Admin@123
//   - 1 tài khoản "tenant" (người thuê)                              — phone: 0900000002, password: Tenant@123
//   - 5 phòng mẫu (rooms), trong đó 1 phòng được gán cho tenant ở trên
//
// Lưu ý: model User trong hệ thống chỉ có 2 role là "manager" và "tenant"
// (xem internal/models/user.go), không có role riêng tên "admin" — nên
// "manager" chính là tài khoản quản trị/admin trong app này.
//
// Script an toàn để chạy nhiều lần: nếu phone/code đã tồn tại thì sẽ
// cập nhật (upsert) thay vì tạo trùng.
//
// Field giá điện/nước đã đổi tên thành "price_electricity" và "price_water"
// (khớp với internal/models/room.go). Status của phòng được tự suy ra:
// có TenantID -> "occupied", không có -> "available" (không cần set tay).

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

func main() {
	cfg := config.Load()
	config.ConnectMongo(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ---------- 1. Tài khoản admin/manager ----------
	adminID := upsertUser(ctx, models.User{
		FullName: "Quản trị viên",
		Phone:    "0900000001",
		Email:    "admin@ruma.local",
		Role:     models.RoleManager,
		IsActive: true,
	}, "Admin@123")
	log.Println("✅ Tạo/cập nhật tài khoản admin (manager):", adminID.Hex())

	// ---------- 2. Tài khoản người thuê ----------
	tenantID := upsertUser(ctx, models.User{
		FullName: "Nguyễn Văn Thuê",
		Phone:    "0900000002",
		Email:    "tenant@ruma.local",
		Role:     models.RoleTenant,
		IsActive: true,
	}, "Tenant@123")
	log.Println("✅ Tạo/cập nhật tài khoản tenant:", tenantID.Hex())

	// ---------- 3. Phòng mẫu ----------
	rooms := []models.Room{
		{
			Code: "P101", Name: "Phòng 101", Floor: 1,
			MonthlyRent: 2500000, ElectricPrice: 3500, WaterPrice: 15000,
			TenantID: &tenantID, // có tenant -> status sẽ tự động là "occupied"
			Note:     "Phòng đã có người thuê (dữ liệu mẫu)",
		},
		{
			Code: "P102", Name: "Phòng 102", Floor: 1,
			MonthlyRent: 2500000, ElectricPrice: 3500, WaterPrice: 15000,
			// không có TenantID -> status tự động là "available"
		},
		{
			Code: "P201", Name: "Phòng 201", Floor: 2,
			MonthlyRent: 3000000, ElectricPrice: 3500, WaterPrice: 15000,
		},
		{
			Code: "P202", Name: "Phòng 202", Floor: 2,
			MonthlyRent: 3000000, ElectricPrice: 3500, WaterPrice: 15000,
			Note: "Phòng góc, có ban công",
		},
		{
			Code: "P301", Name: "Phòng 301 (Deluxe)", Floor: 3,
			MonthlyRent: 4200000, ElectricPrice: 3800, WaterPrice: 18000,
			Note: "Phòng rộng, có gác lửng",
		},
	}

	for _, room := range rooms {
		id := upsertRoom(ctx, room)
		log.Printf("✅ Tạo/cập nhật phòng %s: %s\n", room.Code, id.Hex())
	}

	log.Println("🎉 Seed dữ liệu hoàn tất.")
}

// upsertUser tạo mới hoặc cập nhật user theo số điện thoại (unique key),
// trả về ObjectID của user.
func upsertUser(ctx context.Context, u models.User, plainPassword string) primitive.ObjectID {
	hash, err := utils.HashPassword(plainPassword)
	if err != nil {
		log.Fatalf("Lỗi hash password cho %s: %v", u.Phone, err)
	}
	u.PasswordHash = hash

	col := config.GetCollection("users")
	now := time.Now()

	filter := bson.M{"phone": u.Phone}
	update := bson.M{
		"$set": bson.M{
			"full_name":     u.FullName,
			"email":         u.Email,
			"password_hash": u.PasswordHash,
			"role":          u.Role,
			"room_id":       u.RoomID,
			"is_active":     u.IsActive,
			"updated_at":    now,
		},
		"$setOnInsert": bson.M{
			"phone":      u.Phone,
			"created_at": now,
		},
	}

	opts := options.Update().SetUpsert(true)
	res, err := col.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Fatalf("Lỗi upsert user %s: %v", u.Phone, err)
	}

	if res.UpsertedID != nil {
		return res.UpsertedID.(primitive.ObjectID)
	}

	// Đã tồn tại từ trước (không phải insert mới) -> lấy lại ID
	var existing models.User
	if err := col.FindOne(ctx, filter).Decode(&existing); err != nil {
		log.Fatalf("Lỗi lấy lại user %s sau upsert: %v", u.Phone, err)
	}
	return existing.ID
}

// upsertRoom tạo mới hoặc cập nhật phòng theo mã phòng (unique key),
// trả về ObjectID của phòng.
//
// Status được tự suy ra từ TenantID: có tenant -> "occupied",
// không có tenant -> "available". Không cần set Status khi khai báo room.
func upsertRoom(ctx context.Context, r models.Room) primitive.ObjectID {
	col := config.GetCollection("rooms")
	now := time.Now()

	status := models.RoomStatusAvailable
	if r.TenantID != nil {
		status = models.RoomStatusOccupied
	}

	filter := bson.M{"code": r.Code}
	update := bson.M{
		"$set": bson.M{
			"name":              r.Name,
			"floor":             r.Floor,
			"tenant_id":         r.TenantID,
			"monthly_rent":      r.MonthlyRent,
			"price_electricity": r.ElectricPrice,
			"price_water":       r.WaterPrice,
			"status":            status,
			"note":              r.Note,
			"updated_at":        now,
		},
		"$setOnInsert": bson.M{
			"code":       r.Code,
			"created_at": now,
		},
	}

	opts := options.Update().SetUpsert(true)
	res, err := col.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Fatalf("Lỗi upsert room %s: %v", r.Code, err)
	}

	if res.UpsertedID != nil {
		return res.UpsertedID.(primitive.ObjectID)
	}

	var existing models.Room
	if err := col.FindOne(ctx, filter).Decode(&existing); err != nil {
		log.Fatalf("Lỗi lấy lại room %s sau upsert: %v", r.Code, err)
	}
	return existing.ID
}
