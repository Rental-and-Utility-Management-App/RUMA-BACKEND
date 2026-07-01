package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const insecureDefaultJWTSecret = "dev-secret-change-me"

type Config struct {
	Env            string // "development" | "production"
	Port           string
	MongoURI       string
	MongoDBName    string
	JWTSecret      string
	JWTExpireHours int
	AllowedOrigins []string // danh sách origin được phép gọi API (CORS)

	// Thông tin tài khoản ngân hàng dùng để tự sinh mã VietQR chuyển khoản.
	BankID          string // Mã ngân hàng theo chuẩn VietQR (BIN hoặc mã ngắn), vd: "970436" hoặc "VCB"
	BankAccountNo   string // Số tài khoản nhận tiền
	BankAccountName string // Tên chủ tài khoản (KHÔNG dấu, IN HOA - đúng như trên thẻ/tài khoản)
	VietQRTemplate  string // Kiểu giao diện QR: compact2 | compact | qr_only | print

	// API Key để xác thực webhook gọi đến từ SePay (cấu hình cùng giá trị bên my.sepay.vn).
	// Để trống -> webhook bị từ chối hoàn toàn (an toàn theo mặc định, không cho phép bỏ qua xác thực).
	SepayWebhookAPIKey string
}

func Load() *Config {
	// Không bắt buộc phải có .env (production có thể set env trực tiếp)
	if err := godotenv.Load(); err != nil {
		log.Println("Không tìm thấy file .env, dùng biến môi trường hệ thống")
	}

	env := getEnv("APP_ENV", "development")

	expireHours, err := strconv.Atoi(getEnv("JWT_EXPIRE_HOURS", "72"))
	if err != nil {
		expireHours = 72
	}

	jwtSecret := loadJWTSecret(env)
	allowedOrigins := loadAllowedOrigins()

	return &Config{
		Env:            env,
		Port:           getEnv("PORT", "8080"),
		MongoURI:       getEnv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDBName:    getEnv("MONGO_DB_NAME", "rental_app"),
		JWTSecret:      jwtSecret,
		JWTExpireHours: expireHours,
		AllowedOrigins: allowedOrigins,

		BankID:          getEnv("BANK_ID", ""),
		BankAccountNo:   getEnv("BANK_ACCOUNT_NO", ""),
		BankAccountName: getEnv("BANK_ACCOUNT_NAME", ""),
		VietQRTemplate:  getEnv("VIETQR_TEMPLATE", "compact2"),

		SepayWebhookAPIKey: getEnv("SEPAY_WEBHOOK_API_KEY", ""),
	}
}

// loadJWTSecret bắt buộc phải có JWT_SECRET hợp lệ khi chạy production.
// Ở development, nếu thiếu thì dùng secret mặc định kèm cảnh báo (KHÔNG được dùng khi deploy thật).
func loadJWTSecret(env string) string {
	secret := os.Getenv("JWT_SECRET")

	if env == "production" {
		if secret == "" {
			log.Fatal("❌ JWT_SECRET chưa được cấu hình. Bắt buộc phải set biến môi trường JWT_SECRET khi APP_ENV=production.")
		}
		if secret == insecureDefaultJWTSecret {
			log.Fatal("❌ JWT_SECRET đang dùng giá trị mặc định không an toàn. Hãy đổi sang một chuỗi bí mật ngẫu nhiên.")
		}
		if len(secret) < 32 {
			log.Fatal("❌ JWT_SECRET quá ngắn (cần tối thiểu 32 ký tự) để đảm bảo an toàn khi chạy production.")
		}
		return secret
	}

	if secret == "" {
		log.Println("⚠️  JWT_SECRET chưa được set, dùng secret mặc định CHỈ DÀNH CHO DEV. Không được dùng secret này khi deploy thật.")
		return insecureDefaultJWTSecret
	}
	return secret
}

// loadAllowedOrigins đọc CORS_ALLOWED_ORIGINS dạng "https://a.com,https://b.com".
// Mặc định "*" (cho phép tất cả) để không phá vỡ trải nghiệm dev, nhưng nên set rõ khi production.
func loadAllowedOrigins() []string {
	raw := getEnv("CORS_ALLOWED_ORIGINS", "*")
	if raw == "*" {
		return []string{"*"}
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			origins = append(origins, p)
		}
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
