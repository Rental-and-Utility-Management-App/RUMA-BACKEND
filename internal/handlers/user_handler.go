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
	"rental-app/internal/services"
	"rental-app/internal/utils"
)

// maxAvatarSizeBytes giới hạn kích thước file avatar upload lên (2MB).
const maxAvatarSizeBytes = 2 << 20 // 2MB

// allowedAvatarContentTypes liệt kê các định dạng ảnh được phép làm avatar.
var allowedAvatarContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

type UserHandler struct {
	cloudinary *services.CloudinaryService
	email      *services.EmailService
}

func NewUserHandler(cloudinary *services.CloudinaryService, email *services.EmailService) *UserHandler {
	return &UserHandler{cloudinary: cloudinary, email: email}
}

type createTenantRequest struct {
	FullName string `json:"full_name" binding:"required"`
	Phone    string `json:"phone" binding:"required"`
	Email    string `json:"email" binding:"required,email"`
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

	// Kiểm tra trùng email (email là bắt buộc và dùng để gửi tài khoản/mật khẩu)
	emailCount, err := usersCol.CountDocuments(ctx, bson.M{"email": req.Email})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if emailCount > 0 {
		utils.Error(c, http.StatusConflict, "Email đã được sử dụng")
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

	// Gửi email cấp tài khoản/mật khẩu cho tenant. Chạy nền (goroutine) để không làm
	// chậm response nếu SMTP xử lý lâu; lỗi gửi mail được log bên trong service, không
	// làm fail request tạo user (tài khoản đã tạo thành công trong DB).
	if h.email != nil {
		go h.email.SendTenantCredentials(user.Email, user.FullName, user.Phone, req.Password)
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
	Email    string `json:"email" binding:"required,email"`
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
	update["email"] = req.Email

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")

	// Kiểm tra email không bị trùng với tài khoản khác (loại trừ chính tenant đang sửa)
	emailCount, err := usersCol.CountDocuments(ctx, bson.M{"email": req.Email, "_id": bson.M{"$ne": id}})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if emailCount > 0 {
		utils.Error(c, http.StatusConflict, "Email đã được sử dụng bởi tài khoản khác")
		return
	}

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

// UploadAvatar godoc
// @Summary Upload/đổi ảnh đại diện
// @Description Upload ảnh đại diện cho user đang đăng nhập (cả Manager và Tenant), lưu trên Cloudinary.
// @Tags Users
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param avatar formData file true "File ảnh (jpeg/png/webp, tối đa 5MB)"
// @Success 200 {object} map[string]interface{}
// @Router /api/users/me/avatar [post]
func (h *UserHandler) UploadAvatar(c *gin.Context) {
	if h.cloudinary == nil || !h.cloudinary.IsConfigured() {
		utils.Error(c, http.StatusServiceUnavailable, "Chức năng upload avatar chưa được cấu hình (thiếu CLOUDINARY_API_KEY/CLOUDINARY_API_SECRET)")
		return
	}

	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
		return
	}

	fileHeader, err := c.FormFile("avatar")
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Vui lòng chọn file ảnh (field 'avatar')")
		return
	}

	if fileHeader.Size > maxAvatarSizeBytes {
		utils.Error(c, http.StatusBadRequest, "File ảnh vượt quá dung lượng cho phép (tối đa 5MB)")
		return
	}

	contentType := fileHeader.Header.Get("Content-Type")
	if !allowedAvatarContentTypes[contentType] {
		utils.Error(c, http.StatusBadRequest, "Chỉ hỗ trợ file ảnh JPEG, PNG hoặc WEBP")
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể đọc file ảnh")
		return
	}
	defer file.Close()

	result, err := h.cloudinary.UploadAvatar(file, fileHeader.Filename, userID.Hex())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Upload ảnh thất bại: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set": bson.M{
			"avatar_url":       result.SecureURL,
			"avatar_public_id": result.PublicID,
			"updated_at":       time.Now(),
		},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Upload ảnh thành công nhưng cập nhật tài khoản thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật ảnh đại diện thành công", gin.H{
		"avatar_url": result.SecureURL,
	})
}

// DeleteAvatar godoc
// @Summary Gỡ ảnh đại diện
// @Description Gỡ ảnh đại diện hiện tại của user đang đăng nhập.
// @Tags Users
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/users/me/avatar [delete]
func (h *UserHandler) DeleteAvatar(c *gin.Context) {
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

	if user.AvatarPublicID == "" {
		utils.Error(c, http.StatusConflict, "Tài khoản hiện không có ảnh đại diện")
		return
	}

	// Không coi lỗi xóa trên Cloudinary là nghiêm trọng - vẫn tiếp tục gỡ khỏi DB
	// để trải nghiệm người dùng không bị chặn bởi lỗi tạm thời của dịch vụ ngoài.
	if h.cloudinary != nil && h.cloudinary.IsConfigured() {
		_ = h.cloudinary.DeleteImage(user.AvatarPublicID)
	}

	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": userID}, bson.M{
		"$set":   bson.M{"updated_at": time.Now()},
		"$unset": bson.M{"avatar_url": "", "avatar_public_id": ""},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật tài khoản")
		return
	}

	utils.Success(c, http.StatusOK, "Gỡ ảnh đại diện thành công", nil)
}
