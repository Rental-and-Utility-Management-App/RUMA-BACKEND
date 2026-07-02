package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	// 1. Thêm 3 thư viện này của Swagger
	_ "rental-app/docs" // CỰC KỲ QUAN TRỌNG: Import thư mục docs do lệnh 'swag init' sinh ra

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"rental-app/internal/config"
	"rental-app/internal/handlers"
	"rental-app/internal/middleware"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

func SetupRoutes(r *gin.Engine, cfg *config.Config) {
	// 0. Bật CORS cho toàn bộ API (đặt trước mọi route khác)
	r.Use(middleware.CORS(cfg.AllowedOrigins))

	// Healthcheck - dùng cho Docker HEALTHCHECK, load balancer, uptime monitor...
	// Kiểm tra luôn kết nối MongoDB để phản ánh đúng tình trạng "sẵn sàng phục vụ".
	r.GET("/healthz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		if err := config.Client.Ping(ctx, nil); err != nil {
			utils.Error(c, http.StatusServiceUnavailable, "Không kết nối được cơ sở dữ liệu")
			return
		}

		utils.Success(c, http.StatusOK, "OK", gin.H{"status": "healthy"})
	})

	// 2. Bật giao diện Swagger Web (Route này để ở ngoài cùng, ai cũng truy cập được để xem docs)
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	authHandler := handlers.NewAuthHandler(cfg)
	userHandler := handlers.NewUserHandler()
	roomHandler := handlers.NewRoomHandler()
	invoiceHandler := handlers.NewInvoiceHandler(cfg)
	paymentHandler := handlers.NewPaymentHandler(cfg)
	contractHandler := handlers.NewContractHandler()

	// Rate limit riêng cho login: tối đa 5 lần thử/phút theo từng IP, chống brute-force.
	loginLimiter := middleware.NewRateLimiter(5, time.Minute)

	api := r.Group("/api")

	// ---- Public routes ----
	auth := api.Group("/auth")
	{
		auth.POST("/login", loginLimiter.Middleware(), authHandler.Login)
	}

	// Webhook nhận báo giao dịch từ SePay - không qua JWT (bên thứ 3 gọi vào),
	// tự xác thực bằng API Key riêng (xem SepayWebhook handler).
	webhooks := api.Group("/webhooks")
	{
		webhooks.POST("/sepay", paymentHandler.SepayWebhook)
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
			users.PUT("/:id/room", userHandler.AssignRoom)      // gán/đổi phòng cho tenant có sẵn
			users.DELETE("/:id/room", userHandler.UnassignRoom) // trả phòng cho 1 tenant cụ thể
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
				roomsManagerOnly.POST("/:id/checkout", roomHandler.CheckoutRoom)
			}
		}

		// Invoices - manager tạo, cả 2 role xem (tự lọc theo quyền)
		invoices := protected.Group("/invoices")
		{
			invoices.GET("", invoiceHandler.ListInvoices)
			invoices.GET("/:id", invoiceHandler.GetInvoice)
			invoices.GET("/:id/qr-code", invoiceHandler.GetInvoiceQRCode)

			invoicesManagerOnly := invoices.Group("")
			invoicesManagerOnly.Use(middleware.RequireRole(string(models.RoleManager)))
			{
				invoicesManagerOnly.POST("", invoiceHandler.CreateInvoice)
				invoicesManagerOnly.PUT("/:id", invoiceHandler.UpdateInvoice)
				invoicesManagerOnly.POST("/:id/cancel", invoiceHandler.CancelInvoice)
			}
		}

		// Contracts - manager tạo/quản lý, cả 2 role xem (tự lọc theo quyền)
		contracts := protected.Group("/contracts")
		{
			contracts.GET("", contractHandler.ListContracts)
			contracts.GET("/:id", contractHandler.GetContract)
			contracts.GET("/:id/deposit-transactions", contractHandler.ListDepositTransactions)

			contractsManagerOnly := contracts.Group("")
			contractsManagerOnly.Use(middleware.RequireRole(string(models.RoleManager)))
			{
				contractsManagerOnly.POST("", contractHandler.CreateContract)
				contractsManagerOnly.PUT("/:id", contractHandler.UpdateContract)
				contractsManagerOnly.POST("/:id/extend", contractHandler.ExtendContract)
				contractsManagerOnly.POST("/:id/collect-deposit", contractHandler.CollectDeposit)
				contractsManagerOnly.POST("/:id/checkout", contractHandler.CheckoutContract)
				contractsManagerOnly.POST("/:id/cancel", contractHandler.CancelContract)
				contractsManagerOnly.POST("/:id/tenants", contractHandler.AddTenantToContract)
				contractsManagerOnly.DELETE("/:id/tenants/:tenantId", contractHandler.RemoveTenantFromContract)
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
				paymentsManagerOnly.PUT("/:id", paymentHandler.UpdatePayment)
				paymentsManagerOnly.DELETE("/:id", paymentHandler.DeletePayment)
			}
		}
	}
}
