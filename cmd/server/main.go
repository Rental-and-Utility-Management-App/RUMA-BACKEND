package main

import (
	"log"

	"github.com/gin-gonic/gin"

	"rental-app/internal/config"
	"rental-app/internal/routes"
)

func main() {
	cfg := config.Load()

	config.ConnectMongo(cfg)

	r := gin.Default()

	routes.SetupRoutes(r, cfg)

	log.Printf("🚀 Server đang chạy tại port %s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Không thể khởi động server: %v", err)
	}
}
