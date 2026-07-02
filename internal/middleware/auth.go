package middleware

import (
	"net/http"
	"strings"

	"rental-app/internal/utils"

	"github.com/gin-gonic/gin"
)

// AuthRequired kiểm tra JWT token trong header Authorization: Bearer <token>
func AuthRequired(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			utils.Error(c, http.StatusUnauthorized, "Thiếu token xác thực")
			c.Abort()
			return
		}

		// Cho phép cả 2 dạng: "Bearer <token>" (chuẩn HTTP) và chỉ "<token>"
		// (tiện cho Swagger UI - dán thẳng token vào ô Authorize, không cần
		// gõ thêm "Bearer " phía trước).
		tokenStr := authHeader
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 {
			if parts[0] != "Bearer" {
				utils.Error(c, http.StatusUnauthorized, "Định dạng token không hợp lệ")
				c.Abort()
				return
			}
			tokenStr = parts[1]
		}

		claims, err := utils.ParseToken(tokenStr, jwtSecret)
		if err != nil {
			utils.Error(c, http.StatusUnauthorized, "Token không hợp lệ hoặc đã hết hạn")
			c.Abort()
			return
		}

		// Lưu thông tin user vào context để handler dùng
		c.Set("user_id", claims.UserID)
		c.Set("role", claims.Role)

		c.Next()
	}
}

// RequireRole chỉ cho phép các role được liệt kê truy cập
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists {
			utils.Error(c, http.StatusUnauthorized, "Không xác định được vai trò người dùng")
			c.Abort()
			return
		}

		roleStr := role.(string)
		allowed := false
		for _, r := range roles {
			if r == roleStr {
				allowed = true
				break
			}
		}

		if !allowed {
			utils.Error(c, http.StatusForbidden, "Bạn không có quyền thực hiện hành động này")
			c.Abort()
			return
		}

		c.Next()
	}
}
