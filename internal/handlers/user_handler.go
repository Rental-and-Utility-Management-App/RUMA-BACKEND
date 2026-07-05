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

// CreateTenant godoc
// @Summary Tạo tài khoản người thuê
// @Description Chỉ Manager được tạo tài khoản Tenant.
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createTenantRequest true "Thông tin người thuê"
// @Success 201 {object} map[string]interface{}
// @Router /api/users [post]
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

		roomsCol := config.GetCollection("rooms")
		roomCount, err := roomsCol.CountDocuments(ctx, bson.M{"_id": roomID})
		if err != nil {
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
		if roomCount == 0 {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}

		// Không cho gán trực tiếp vào phòng đang gắn hợp đồng active
		contractsCol := config.GetCollection("contracts")
		hasActive, err := hasActiveContractForRoom(ctx, contractsCol, roomID)
		if err != nil {
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
		if hasActive {
			utils.Error(c, http.StatusConflict, "Phòng đang gắn với 1 hợp đồng hiệu lực; hãy thêm người thuê thông qua hợp đồng (POST /api/contracts) thay vì gán trực tiếp lúc tạo tài khoản")
			return
		}

		user.RoomID = &roomID
	}

	if _, err := usersCol.InsertOne(ctx, user); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo tài khoản")
		return
	}

	if user.RoomID != nil {
		roomsCol := config.GetCollection("rooms")
		if _, err := addTenantToRoom(ctx, roomsCol, *user.RoomID, user.ID); err != nil {
			if err == mongo.ErrNoDocuments {
				utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
				return
			}
			if err == ErrRoomFull {
				utils.Error(c, http.StatusConflict, "Phòng đã đủ số người tối đa (capacity), không thể gán thêm")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Tạo user thành công nhưng cập nhật phòng thất bại")
			return
		}
	}

	utils.Success(c, http.StatusCreated, "Tạo tài khoản người thuê thành công", user.ToResponse())
}

// ListTenants godoc
// @Summary Danh sách người thuê
// @Description Manager xem danh sách tất cả tenant.
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/users [get]
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

	// Lấy danh sách RoomID
	var roomIDs []primitive.ObjectID
	for _, u := range users {
		if u.RoomID != nil {
			roomIDs = append(roomIDs, *u.RoomID)
		}
	}

	// Truy vấn tất cả phòng liên quan
	roomMap := make(map[primitive.ObjectID]models.Room)
	if len(roomIDs) > 0 {
		roomsCol := config.GetCollection("rooms")
		roomCursor, err := roomsCol.Find(ctx, bson.M{"_id": bson.M{"$in": roomIDs}})
		if err == nil {
			var rooms []models.Room
			if err := roomCursor.All(ctx, &rooms); err == nil {
				for _, r := range rooms {
					roomMap[r.ID] = r
				}
			}
		}
	}

	responses := make([]models.UserResponse, 0, len(users))
	for _, u := range users {
		res := u.ToResponse()
		if u.RoomID != nil {
			if r, ok := roomMap[*u.RoomID]; ok {
				res.Room = &r
			}
		}
		responses = append(responses, res)
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách thành công", responses)
}

// GetTenant godoc
// @Summary Xem chi tiết người thuê
// @Description Manager xem thông tin chi tiết một tenant theo ID.
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Tenant ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/users/{id} [get]
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

	res := user.ToResponse()

	// Truy vấn phòng nếu user có RoomID
	if user.RoomID != nil {
		roomsCol := config.GetCollection("rooms")
		var room models.Room
		if err := roomsCol.FindOne(ctx, bson.M{"_id": *user.RoomID}).Decode(&room); err == nil {
			res.Room = &room
		}
	}

	utils.Success(c, http.StatusOK, "OK", res)
}

type updateTenantRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	IsActive *bool  `json:"is_active"`
}

// UpdateTenant godoc
// @Summary Cập nhật người thuê
// @Description Manager cập nhật thông tin tenant.
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Tenant ID"
// @Param request body updateTenantRequest true "Thông tin cập nhật"
// @Success 200 {object} map[string]interface{}
// @Router /api/users/{id} [put]
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")

	if req.IsActive != nil && !*req.IsActive {
		contractsCol := config.GetCollection("contracts")
		hasActive, err := hasActiveContractForTenant(ctx, contractsCol, id)
		if err != nil {
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
		if hasActive {
			utils.Error(c, http.StatusConflict, "Người thuê đang đứng tên trong 1 hợp đồng hiệu lực; hãy checkout/cancel hợp đồng đó trước khi khóa tài khoản")
			return
		}
	}
	if req.IsActive != nil {
		update["is_active"] = *req.IsActive
	}

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

type assignRoomRequest struct {
	RoomID string `json:"room_id" binding:"required"`
}

// AssignRoom godoc
// @Summary Gán/đổi phòng cho người thuê có sẵn
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Tenant ID"
// @Param request body assignRoomRequest true "Phòng muốn gán"
// @Success 200 {object} map[string]interface{}
// @Router /api/users/{id}/room [put]
func (h *UserHandler) AssignRoom(c *gin.Context) {
	tenantID, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req assignRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	newRoomID, err := primitive.ObjectIDFromHex(req.RoomID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Room ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	roomsCol := config.GetCollection("rooms")
	contractsCol := config.GetCollection("contracts")

	var tenant models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": tenantID, "role": models.RoleTenant}).Decode(&tenant); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy người thuê")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if tenant.RoomID != nil && *tenant.RoomID == newRoomID {
		utils.Error(c, http.StatusConflict, "Người thuê đã ở phòng này rồi")
		return
	}

	tenantHasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if tenantHasActive {
		utils.Error(c, http.StatusConflict, "Người thuê đang đứng tên trong 1 hợp đồng hiệu lực; hãy checkout hoặc hủy hợp đồng đó trước (POST /api/contracts/{id}/checkout hoặc /cancel) rồi mới đổi phòng")
		return
	}

	count, err := roomsCol.CountDocuments(ctx, bson.M{"_id": newRoomID})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}

	roomHasActive, err := hasActiveContractForRoom(ctx, contractsCol, newRoomID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if roomHasActive {
		utils.Error(c, http.StatusConflict, "Phòng đích đang gắn với 1 hợp đồng hiệu lực; hãy quản lý người ở ghép thông qua hợp đồng (POST /api/contracts) thay vì gán trực tiếp")
		return
	}

	if tenant.RoomID != nil {
		if _, err := removeTenantFromRoom(ctx, roomsCol, *tenant.RoomID, tenantID); err != nil && err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Không thể gỡ người thuê khỏi phòng cũ")
			return
		}
	}

	newRoom, err := addTenantToRoom(ctx, roomsCol, newRoomID, tenantID)
	if err != nil {
		if err == ErrRoomFull {
			utils.Error(c, http.StatusConflict, "Phòng đích đã đủ số người tối đa (capacity), không thể gán thêm")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể gán người thuê vào phòng mới")
		return
	}

	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set": bson.M{"room_id": newRoomID, "updated_at": time.Now()},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gán phòng nhưng cập nhật tài khoản người thuê thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Gán phòng thành công", gin.H{
		"tenant_id": tenantID,
		"room_id":   newRoomID,
		"occupants": len(newRoom.TenantIDs),
	})
}

// UnassignRoom godoc
// @Summary Trả phòng cho 1 người thuê cụ thể
// @Tags Users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Tenant ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/users/{id}/room [delete]
func (h *UserHandler) UnassignRoom(c *gin.Context) {
	tenantID, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	roomsCol := config.GetCollection("rooms")

	var tenant models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": tenantID, "role": models.RoleTenant}).Decode(&tenant); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy người thuê")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if tenant.RoomID == nil {
		utils.Error(c, http.StatusConflict, "Người thuê hiện không thuộc phòng nào")
		return
	}

	contractsCol := config.GetCollection("contracts")
	hasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if hasActive {
		utils.Error(c, http.StatusConflict, "Người thuê này hiện đang đứng tên trên một hợp đồng còn hiệu lực, nên không thể xóa/thao tác trực tiếp được. Bạn cần thực hiện thủ tục trả phòng cho hợp đồng đó trước — nếu đã thu tiền cọc thì dùng chức năng \"Trả phòng\" (checkout), còn nếu chưa thu cọc thì dùng chức năng \"Hủy hợp đồng\" (cancel).")
		return
	}

	oldRoomID := *tenant.RoomID
	if _, err := removeTenantFromRoom(ctx, roomsCol, oldRoomID, tenantID); err != nil {
		if err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật phòng")
			return
		}
	}

	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set":   bson.M{"updated_at": time.Now()},
		"$unset": bson.M{"room_id": ""},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Trả phòng thành công nhưng cập nhật tài khoản người thuê thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Trả phòng thành công", gin.H{
		"tenant_id": tenantID,
		"room_id":   oldRoomID,
	})
}
