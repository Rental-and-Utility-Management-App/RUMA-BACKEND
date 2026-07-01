package main

import (
	"log"

	"github.com/gin-gonic/gin"

	"rental-app/internal/config"
	"rental-app/internal/routes"
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
// @description Nhập theo định dạng: Bearer {token}
func main() {
	cfg := config.Load()

	if cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	config.ConnectMongo(cfg)

	r := gin.Default()

	routes.SetupRoutes(r, cfg)

	log.Printf("🚀 Server đang chạy tại port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Không thể khởi động server: %v", err)
	}
}
