package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS trả về middleware xử lý Cross-Origin Resource Sharing.
//
// allowedOrigins: danh sách domain được phép gọi API, ví dụ:
//
//	[]string{"https://app.example.com", "https://admin.example.com"}
//
// Nếu allowedOrigins chỉ chứa "*", mọi origin đều được phép truy cập
// (lúc này KHÔNG bật Access-Control-Allow-Credentials vì trình duyệt không cho phép
// kết hợp wildcard origin với credentials).
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 1 && allowedOrigins[0] == "*"

	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.TrimSpace(o)] = true
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		switch {
		case allowAll:
			c.Header("Access-Control-Allow-Origin", "*")
		case origin != "" && allowed[origin]:
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "43200") // 12 giờ

		// Trình duyệt gửi preflight request bằng OPTIONS trước khi gọi API thật.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
