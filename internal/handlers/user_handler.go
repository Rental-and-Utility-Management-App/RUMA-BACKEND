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

type UserHandler struct{}

func NewUserHandler() *UserHandler {
	return &UserHandler{}
}

type createTenantRequest struct {
	FullName string `json:"full_name" binding:"required"`
	Phone    string `json:"phone" binding:"required"`
	Email    string `json:"email"`
	Password string `json:"password" binding:"required,min=6"`
	RoomID   string `json:"room_id"` // optional lúc tạo, có thể gán phòng sau
}

// POST /api/users - chỉ manager được tạo tài khoản tenant
func (h *UserHandler) CreateTenant(c *gin.Context) {
	var req createTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")

	// Kiểm tra trùng số điện thoại
	count, err := usersCol.CountDocuments(ctx, bson.M{"phone": req.Phone})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count > 0 {
		utils.Error(c, http.StatusConflict, "Số điện thoại đã được sử dụng")
		return
	}

	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể mã hóa mật khẩu")
		return
	}

	user := models.User{
		ID:           primitive.NewObjectID(),
		FullName:     req.FullName,
		Phone:        req.Phone,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         models.RoleTenant,
		IsActive:     true,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if req.RoomID != "" {
		roomID, err := primitive.ObjectIDFromHex(req.RoomID)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "Room ID không hợp lệ")
			return
		}
		user.RoomID = &roomID
	}

	if _, err := usersCol.InsertOne(ctx, user); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo tài khoản")
		return
	}

	// Nếu có gán phòng, cập nhật phòng đó -> occupied + tenant_id
	if user.RoomID != nil {
		roomsCol := config.GetCollection("rooms")
		_, err = roomsCol.UpdateOne(ctx, bson.M{"_id": *user.RoomID}, bson.M{
			"$set": bson.M{"status": models.RoomStatusOccupied, "tenant_id": user.ID, "updated_at": time.Now()},
		})
		if err != nil {
			utils.Error(c, http.StatusInternalServerError, "Tạo user thành công nhưng cập nhật phòng thất bại")
			return
		}
	}

	utils.Success(c, http.StatusCreated, "Tạo tài khoản người thuê thành công", user.ToResponse())
}

// GET /api/users - manager xem danh sách tất cả tenant
func (h *UserHandler) ListTenants(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	cursor, err := usersCol.Find(ctx, bson.M{"role": models.RoleTenant})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	var users []models.User
	if err := cursor.All(ctx, &users); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	responses := make([]models.UserResponse, 0, len(users))
	for _, u := range users {
		responses = append(responses, u.ToResponse())
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách thành công", responses)
}

// GET /api/users/:id
func (h *UserHandler) GetTenant(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	var user models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": id}).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy người dùng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	utils.Success(c, http.StatusOK, "OK", user.ToResponse())
}

type updateTenantRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	IsActive *bool  `json:"is_active"`
}

// PUT /api/users/:id - manager cập nhật thông tin tenant
func (h *UserHandler) UpdateTenant(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updateTenantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	update := bson.M{"updated_at": time.Now()}
	if req.FullName != "" {
		update["full_name"] = req.FullName
	}
	if req.Email != "" {
		update["email"] = req.Email
	}
	if req.IsActive != nil {
		update["is_active"] = *req.IsActive
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	res, err := usersCol.UpdateOne(ctx, bson.M{"_id": id, "role": models.RoleTenant}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if res.MatchedCount == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy người dùng")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật thành công", nil)
}
