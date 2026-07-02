package main

import (
	"context"
	"flag"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

func main() {
	phone := flag.String("phone", "0984739739", "Số điện thoại tài khoản manager mặc định")
	password := flag.String("password", "admin123", "Mật khẩu tài khoản manager mặc định")
	fullName := flag.String("name", "Manager", "Họ tên tài khoản manager mặc định")

	flag.Parse()

	cfg := config.Load()
	config.ConnectMongo(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	roomsCol := config.GetCollection("rooms")

	seedManager(ctx, usersCol, *phone, *password, *fullName)

	roomIDs := seedRooms(ctx, roomsCol)
	seedTenants(ctx, usersCol, roomsCol, roomIDs)
}

// seedManager tạo tài khoản manager mặc định nếu chưa tồn tại.
func seedManager(ctx context.Context, usersCol *mongo.Collection, phone, password, fullName string) {
	count, err := usersCol.CountDocuments(ctx, bson.M{"phone": phone})
	if err != nil {
		log.Fatalf("Lỗi kiểm tra tài khoản đã tồn tại: %v", err)
	}
	if count > 0 {
		log.Printf("⚠️  Tài khoản manager với số điện thoại %s đã tồn tại, bỏ qua seed.", phone)
		return
	}

	hash, err := utils.HashPassword(password)
	if err != nil {
		log.Fatalf("Không thể mã hóa mật khẩu: %v", err)
	}

	now := time.Now()
	manager := models.User{
		ID:           primitive.NewObjectID(),
		FullName:     fullName,
		Phone:        phone,
		PasswordHash: hash,
		Role:         models.RoleManager,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := usersCol.InsertOne(ctx, manager); err != nil {
		log.Fatalf("Không thể tạo tài khoản manager: %v", err)
	}

	log.Println("✅ Seed thành công tài khoản manager:")
	log.Printf("   Số điện thoại: %s", phone)
	log.Printf("   Mật khẩu:      %s", password)
	log.Println("   ⚠️  Hãy đổi mật khẩu ngay sau khi đăng nhập lần đầu (PUT /api/auth/change-password).")
}

type roomSeed struct {
	code  string
	floor int
}

func seedRooms(ctx context.Context, roomsCol *mongo.Collection) map[string]primitive.ObjectID {
	rooms := []roomSeed{
		{code: "101", floor: 1},
		{code: "102", floor: 1},
		{code: "201", floor: 2},
		{code: "202", floor: 2},
		{code: "301", floor: 3},
		{code: "302", floor: 3},
	}

	roomIDs := make(map[string]primitive.ObjectID)
	now := time.Now()

	for _, r := range rooms {
		existing := roomsCol.FindOne(ctx, bson.M{"code": r.code})
		var found models.Room
		if err := existing.Decode(&found); err == nil {
			log.Printf("⚠️  Phòng %s đã tồn tại, bỏ qua seed.", r.code)
			roomIDs[r.code] = found.ID
			continue
		}

		room := models.Room{
			ID:                     primitive.NewObjectID(),
			Code:                   r.code,
			Name:                   "Phòng " + r.code,
			Floor:                  r.floor,
			Capacity:               2,
			MonthlyRent:            2500000,
			ElectricPrice:          3800,
			WaterPrice:             25000,
			Occupants:              0,
			ManagementFeePerPerson: 50000,
			Status:                 models.RoomStatusAvailable,
			Note:                   "",
			CreatedAt:              now,
			UpdatedAt:              now,
		}

		if _, err := roomsCol.InsertOne(ctx, room); err != nil {
			log.Fatalf("Không thể tạo phòng %s: %v", r.code, err)
		}

		roomIDs[r.code] = room.ID
		log.Printf("✅ Seed thành công phòng %s", r.code)
	}

	return roomIDs
}

type tenantSeed struct {
	fullName string
	phone    string
	password string
	roomCode string
	isHead   bool
}

func seedTenants(ctx context.Context, usersCol, roomsCol *mongo.Collection, roomIDs map[string]primitive.ObjectID) {
	tenants := []tenantSeed{
		{fullName: "Vũ Thảo Minh", phone: "0963797589", password: "minh123", roomCode: "101", isHead: false},
		{fullName: "Tăng Thị Bình", phone: "0983123425", password: "binh123", roomCode: "202", isHead: true},
		{fullName: "Nguyễn Tăng Tài Phát", phone: "0932000035", password: "phat123", roomCode: "101", isHead: true},
	}

	now := time.Now()

	for _, t := range tenants {
		count, err := usersCol.CountDocuments(ctx, bson.M{"phone": t.phone})
		if err != nil {
			log.Fatalf("Lỗi kiểm tra tài khoản đã tồn tại (%s): %v", t.phone, err)
		}
		if count > 0 {
			log.Printf("⚠️  Tài khoản tenant với số điện thoại %s đã tồn tại, bỏ qua seed.", t.phone)
			continue
		}

		roomID, ok := roomIDs[t.roomCode]
		if !ok {
			log.Fatalf("Không tìm thấy phòng %s để gán cho tenant %s", t.roomCode, t.fullName)
		}

		hash, err := utils.HashPassword(t.password)
		if err != nil {
			log.Fatalf("Không thể mã hóa mật khẩu cho %s: %v", t.fullName, err)
		}

		user := models.User{
			ID:           primitive.NewObjectID(),
			FullName:     t.fullName,
			Phone:        t.phone,
			PasswordHash: hash,
			Role:         models.RoleTenant,
			RoomID:       &roomID,
			IsActive:     true,
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		if _, err := usersCol.InsertOne(ctx, user); err != nil {
			log.Fatalf("Không thể tạo tài khoản tenant %s: %v", t.fullName, err)
		}

		var update bson.M
		if t.isHead {
			update = bson.M{
				"$push": bson.M{
					"tenant_ids": bson.M{"$each": bson.A{user.ID}, "$position": 0},
				},
				"$inc": bson.M{"occupants": 1},
				"$set": bson.M{"status": models.RoomStatusOccupied, "updated_at": now},
			}
		} else {
			update = bson.M{
				"$addToSet": bson.M{"tenant_ids": user.ID},
				"$inc":      bson.M{"occupants": 1},
				"$set":      bson.M{"status": models.RoomStatusOccupied, "updated_at": now},
			}
		}

		if _, err := roomsCol.UpdateOne(ctx, bson.M{"_id": roomID}, update); err != nil {
			log.Fatalf("Không thể cập nhật phòng %s cho tenant %s: %v", t.roomCode, t.fullName, err)
		}

		headNote := ""
		if t.isHead {
			headNote = " (chủ hộ)"
		}
		log.Printf("✅ Seed thành công tenant: %s - %s - phòng %s%s", t.fullName, t.phone, t.roomCode, headNote)
	}
}
