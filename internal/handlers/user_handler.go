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

		// Không cho gán trực tiếp vào phòng đang gắn hợp đồng active: hợp đồng đó
		// không có tenant mới này trong tenant_ids -> gán "tắt" sẽ làm room.tenant_ids
		// lệch khỏi contract.tenant_ids. Phải thêm người thông qua /api/contracts.
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

	// Nếu có gán phòng, cập nhật phòng đó -> occupied + thêm vào tenant_ids
	// (dùng $addToSet để không bao giờ bị trùng phần tử trong mảng).
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

	responses := make([]models.UserResponse, 0, len(users))
	for _, u := range users {
		responses = append(responses, u.ToResponse())
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

	utils.Success(c, http.StatusOK, "OK", user.ToResponse())
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

type assignRoomRequest struct {
	RoomID string `json:"room_id" binding:"required"`
}

// AssignRoom godoc
// @Summary Gán/đổi phòng cho người thuê có sẵn
// @Description Manager gán 1 tenant đã tồn tại vào 1 phòng, hoặc đổi tenant đó
// @Description sang phòng khác nếu đang ở phòng cũ. 1 phòng có thể chứa nhiều
// @Description tenant (ở ghép). Có validate để tránh gán trùng (tenant đã ở
// @Description đúng phòng đó rồi thì báo lỗi thay vì gán lại vô nghĩa).
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

	// Validate tránh gán trùng: tenant đã ở đúng phòng này rồi.
	if tenant.RoomID != nil && *tenant.RoomID == newRoomID {
		utils.Error(c, http.StatusConflict, "Người thuê đã ở phòng này rồi")
		return
	}

	// Không cho đổi phòng "tắt" nếu tenant đang đứng tên trong 1 hợp đồng active -
	// đổi thẳng sẽ làm hợp đồng đó lệch khỏi thực tế (tenant đã rời phòng cũ
	// nhưng hợp đồng vẫn active với room_id cũ). Phải checkout/cancel hợp đồng
	// đó trước.
	tenantHasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if tenantHasActive {
		utils.Error(c, http.StatusConflict, "Người thuê đang đứng tên trong 1 hợp đồng hiệu lực; hãy checkout hoặc hủy hợp đồng đó trước (POST /api/contracts/{id}/checkout hoặc /cancel) rồi mới đổi phòng")
		return
	}

	// Phòng đích phải tồn tại trước khi thao tác gì (báo lỗi sớm, không đụng tới phòng cũ).
	count, err := roomsCol.CountDocuments(ctx, bson.M{"_id": newRoomID})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}

	// Không cho gán vào phòng đích đang gắn hợp đồng active của (những) tenant
	// khác - hợp đồng đó không có tenant này trong tenant_ids nên gán "tắt" sẽ
	// làm room.tenant_ids lệch khỏi contract.tenant_ids.
	roomHasActive, err := hasActiveContractForRoom(ctx, contractsCol, newRoomID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if roomHasActive {
		utils.Error(c, http.StatusConflict, "Phòng đích đang gắn với 1 hợp đồng hiệu lực; hãy quản lý người ở ghép thông qua hợp đồng (POST /api/contracts) thay vì gán trực tiếp")
		return
	}

	// Nếu đang ở phòng khác -> đây là thao tác ĐỔI phòng: gỡ khỏi phòng cũ trước.
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
// @Description Manager gỡ 1 tenant khỏi phòng hiện tại (những tenant khác ở
// @Description ghép cùng phòng, nếu có, không bị ảnh hưởng). Phòng tự chuyển
// @Description về "available" khi không còn tenant nào.
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

	// Không cho trả phòng "tắt" nếu tenant đang đứng tên trong 1 hợp đồng active -
	// sẽ làm hợp đồng bị "mồ côi" (vẫn active nhưng tenant đã rời phòng). Phải
	// checkout/cancel hợp đồng đó trước để giữ đồng bộ dữ liệu và xử lý cọc đúng quy trình.
	contractsCol := config.GetCollection("contracts")
	hasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if hasActive {
		utils.Error(c, http.StatusConflict, "Người thuê đang đứng tên trong 1 hợp đồng hiệu lực; hãy dùng POST /api/contracts/{id}/checkout (hoặc /cancel nếu chưa thu cọc) để trả phòng đúng quy trình")
		return
	}

	oldRoomID := *tenant.RoomID
	if _, err := removeTenantFromRoom(ctx, roomsCol, oldRoomID, tenantID); err != nil {
		if err == mongo.ErrNoDocuments {
			// Phòng cũ không còn tồn tại -> vẫn tiếp tục gỡ room_id khỏi tenant bên dưới.
		} else {
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
