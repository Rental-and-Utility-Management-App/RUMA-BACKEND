package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

type AuthHandler struct {
	Cfg *config.Config
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{Cfg: cfg}
}

type loginRequest struct {
	Phone    string `json:"phone" binding:"required" extensions:"x-order=0"`
	Password string `json:"password" binding:"required" extensions:"x-order=1"`
}

// Login godoc
// @Summary Đăng nhập
// @Description Đăng nhập chung cho cả Manager và Tenant, trả về JWT token.
// @Tags Auth
// @Accept json
// @Produce json
// @Param request body loginRequest true "Thông tin đăng nhập"
// @Success 200 {object} map[string]interface{}
// @Router /api/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	err := usersCol.FindOne(ctx, bson.M{"phone": req.Phone}).Decode(&user)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusUnauthorized, "Số điện thoại hoặc mật khẩu không đúng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if !user.IsActive {
		utils.Error(c, http.StatusForbidden, "Tài khoản đã bị vô hiệu hóa")
		return
	}

	if !utils.CheckPasswordHash(req.Password, user.PasswordHash) {
		utils.Error(c, http.StatusUnauthorized, "Số điện thoại hoặc mật khẩu không đúng")
		return
	}

	token, err := utils.GenerateToken(user.ID, string(user.Role), h.Cfg.JWTSecret, h.Cfg.JWTExpireHours)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo token")
		return
	}

	utils.Success(c, http.StatusOK, "Đăng nhập thành công", gin.H{
		"token": token,
		"user":  user.ToResponse(),
	})
}

// Me godoc
// @Summary Lấy thông tin bản thân
// @Description Lấy thông tin user đang đăng nhập (từ JWT token).
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/auth/me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.Error(c, http.StatusUnauthorized, "Token không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": userID}).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			// Token còn hạn nhưng user_id trong token không còn tồn tại trong DB
			// (tài khoản đã bị xóa, hoặc DB đã được seed/reset lại). Về bản chất
			// đây là "phiên đăng nhập không còn hợp lệ" chứ không phải "không tìm
			// thấy resource", nên trả 401 để frontend tự động logout + xóa token
			// cũ, thay vì 404 khiến người dùng bối rối.
			utils.Error(c, http.StatusUnauthorized, "Phiên đăng nhập không còn hợp lệ, vui lòng đăng nhập lại")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if !user.IsActive {
		// Tài khoản đã bị khóa sau khi token được cấp -> cũng coi là phiên không
		// còn hợp lệ, chặn ngay thay vì cho phép dùng tiếp token cũ.
		utils.Error(c, http.StatusForbidden, "Tài khoản đã bị vô hiệu hóa")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy thông tin thành công", user.ToResponse())
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required" extensions:"x-order=0"`
	NewPassword string `json:"new_password" binding:"required,min=6" extensions:"x-order=1"`
}

// ChangePassword godoc
// @Summary Đổi mật khẩu
// @Description Đổi mật khẩu cho user đang đăng nhập.
// @Tags Auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body changePasswordRequest true "Mật khẩu cũ và mới"
// @Success 200 {object} map[string]interface{}
// @Router /api/auth/change-password [put]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.Error(c, http.StatusUnauthorized, "Token không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": userID}).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusUnauthorized, "Phiên đăng nhập không còn hợp lệ, vui lòng đăng nhập lại")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if !utils.CheckPasswordHash(req.OldPassword, user.PasswordHash) {
		utils.Error(c, http.StatusUnauthorized, "Mật khẩu cũ không đúng")
		return
	}

	newHash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể mã hóa mật khẩu")
		return
	}

	_, err = usersCol.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set": bson.M{"password_hash": newHash, "updated_at": time.Now()},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật mật khẩu")
		return
	}

	utils.Success(c, http.StatusOK, "Đổi mật khẩu thành công", nil)
}
