package main

import (
	"log"

	"github.com/gin-gonic/gin"

	"rental-app/internal/config"
	"rental-app/internal/routes"
	"rental-app/internal/scheduler"
)

// @title RUMA Backend API
// @version 1.0
// @description API quản lý nhà trọ: phòng, người thuê, hóa đơn, thanh toán.
// @termsOfService http://swagger.io/terms/

// @contact.name RUMA Support
// @contact.email support@ruma.local

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8080
// @BasePath /
// @schemes http

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Dán trực tiếp giá trị token vào đây (không cần gõ "Bearer " phía trước, server tự nhận diện cả 2 dạng)
func main() {
	cfg := config.Load()

	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	config.ConnectMongo(cfg)

	// Chạy các cron job nền: tạo hóa đơn nháp đầu tháng, quét hóa đơn quá hạn,
	// quét hợp đồng sắp hết hạn (xem internal/scheduler).
	scheduler.Start()

	r := gin.Default()

	routes.SetupRoutes(r, cfg)

	log.Printf("🚀 Server đang chạy tại port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Không thể khởi động server: %v", err)
	}
}
