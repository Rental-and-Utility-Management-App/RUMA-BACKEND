package main

import (
	"context"
	"flag"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

func main() {
	phone := flag.String("phone", "0932000035", "Số điện thoại tài khoản manager mặc định")
	password := flag.String("password", "admin123", "Mật khẩu tài khoản manager mặc định")
	fullName := flag.String("name", "Manager", "Họ tên tài khoản manager mặc định")

	flag.Parse()

	cfg := config.Load()
	config.ConnectMongo(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")

	count, err := usersCol.CountDocuments(ctx, bson.M{"phone": *phone})
	if err != nil {
		log.Fatalf("Lỗi kiểm tra tài khoản đã tồn tại: %v", err)
	}
	if count > 0 {
		log.Printf("⚠️  Tài khoản manager với số điện thoại %s đã tồn tại, bỏ qua seed.", *phone)
		return
	}

	hash, err := utils.HashPassword(*password)
	if err != nil {
		log.Fatalf("Không thể mã hóa mật khẩu: %v", err)
	}

	now := time.Now()
	manager := models.User{
		ID:           primitive.NewObjectID(),
		FullName:     *fullName,
		Phone:        *phone,
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
	log.Printf("   Số điện thoại: %s", *phone)
	log.Printf("   Mật khẩu:      %s", *password)
	log.Println("   ⚠️  Hãy đổi mật khẩu ngay sau khi đăng nhập lần đầu (PUT /api/auth/change-password).")
}
