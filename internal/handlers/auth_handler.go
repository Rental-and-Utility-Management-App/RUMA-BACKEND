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
	Phone    string `json:"phone" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// POST /api/auth/login - dùng chung cho cả manager và tenant
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

// GET /api/auth/me - lấy thông tin user đang đăng nhập
func (h *AuthHandler) Me(c *gin.Context) {
	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": userID}).Decode(&user); err != nil {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy người dùng")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy thông tin thành công", user.ToResponse())
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

// PUT /api/auth/change-password
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": userID}).Decode(&user); err != nil {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy người dùng")
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
