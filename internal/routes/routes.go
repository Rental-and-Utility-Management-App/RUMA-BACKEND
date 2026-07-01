package routes

import (
	"github.com/gin-gonic/gin"

	"rental-app/internal/config"
	"rental-app/internal/handlers"
	"rental-app/internal/middleware"
	"rental-app/internal/models"
)

func SetupRoutes(r *gin.Engine, cfg *config.Config) {
	authHandler := handlers.NewAuthHandler(cfg)
	userHandler := handlers.NewUserHandler()
	roomHandler := handlers.NewRoomHandler()
	invoiceHandler := handlers.NewInvoiceHandler()
	paymentHandler := handlers.NewPaymentHandler()

	api := r.Group("/api")

	// ---- Public routes ----
	auth := api.Group("/auth")
	{
		auth.POST("/login", authHandler.Login)
	}

	// ---- Protected routes (cần JWT) ----
	protected := api.Group("")
	protected.Use(middleware.AuthRequired(cfg.JWTSecret))
	{
		protected.GET("/auth/me", authHandler.Me)
		protected.PUT("/auth/change-password", authHandler.ChangePassword)

		// Users - chỉ manager quản lý tenant
		users := protected.Group("/users")
		users.Use(middleware.RequireRole(string(models.RoleManager)))
		{
			users.POST("", userHandler.CreateTenant)
			users.GET("", userHandler.ListTenants)
			users.GET("/:id", userHandler.GetTenant)
			users.PUT("/:id", userHandler.UpdateTenant)
		}

		// Rooms - manager full quyền, tenant chỉ xem (đọc, lọc theo mình trong handler)
		rooms := protected.Group("/rooms")
		{
			rooms.GET("", roomHandler.ListRooms) // cả 2 role, tenant tự lọc trong handler
			rooms.GET("/:id", roomHandler.GetRoom)

			roomsManagerOnly := rooms.Group("")
			roomsManagerOnly.Use(middleware.RequireRole(string(models.RoleManager)))
			{
				roomsManagerOnly.POST("", roomHandler.CreateRoom)
				roomsManagerOnly.PUT("/:id", roomHandler.UpdateRoom)
				roomsManagerOnly.DELETE("/:id", roomHandler.DeleteRoom)
			}
		}

		// Invoices - manager tạo, cả 2 role xem (tự lọc theo quyền)
		invoices := protected.Group("/invoices")
		{
			invoices.GET("", invoiceHandler.ListInvoices)
			invoices.GET("/:id", invoiceHandler.GetInvoice)

			invoicesManagerOnly := invoices.Group("")
			invoicesManagerOnly.Use(middleware.RequireRole(string(models.RoleManager)))
			{
				invoicesManagerOnly.POST("", invoiceHandler.CreateInvoice)
			}
		}

		// Payments - manager ghi nhận, cả 2 role xem
		payments := protected.Group("/payments")
		{
			payments.GET("", paymentHandler.ListPayments)

			paymentsManagerOnly := payments.Group("")
			paymentsManagerOnly.Use(middleware.RequireRole(string(models.RoleManager)))
			{
				paymentsManagerOnly.POST("", paymentHandler.CreatePayment)
			}
		}
	}
}