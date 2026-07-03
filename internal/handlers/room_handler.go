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

type RoomHandler struct{}

func NewRoomHandler() *RoomHandler {
	return &RoomHandler{}
}

type createRoomRequest struct {
	Code                   string  `json:"code" binding:"required"`
	Name                   string  `json:"name"`
	Floor                  int     `json:"floor"`
	Capacity               int     `json:"capacity" binding:"required,min=1"`
	MonthlyRent            float64 `json:"monthly_rent" binding:"required"`
	ElectricPrice          float64 `json:"price_electricity" binding:"required"`
	WaterPrice             float64 `json:"price_water" binding:"required"`
	Occupants              int     `json:"occupants"`
	ManagementFeePerPerson float64 `json:"management_fee_per_person"`
	Note                   string  `json:"note"`
}

// CreateRoom godoc
// @Summary Tạo phòng mới
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createRoomRequest true "Thông tin phòng"
// @Success 201 {object} map[string]interface{}
// @Router /api/rooms [post]
func (h *RoomHandler) CreateRoom(c *gin.Context) {
	var req createRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	count, err := roomsCol.CountDocuments(ctx, bson.M{"code": req.Code})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count > 0 {
		utils.Error(c, http.StatusConflict, "Mã phòng đã tồn tại")
		return
	}

	room := models.Room{
		ID:                     primitive.NewObjectID(),
		Code:                   req.Code,
		Name:                   req.Name,
		Floor:                  req.Floor,
		Capacity:               req.Capacity,
		MonthlyRent:            req.MonthlyRent,
		ElectricPrice:          req.ElectricPrice,
		WaterPrice:             req.WaterPrice,
		Occupants:              req.Occupants,
		ManagementFeePerPerson: req.ManagementFeePerPerson,
		Status:                 models.RoomStatusAvailable,
		Note:                   req.Note,
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
	}

	if _, err := roomsCol.InsertOne(ctx, room); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo phòng")
		return
	}

	utils.Success(c, http.StatusCreated, "Tạo phòng thành công", room)
}

// ListRooms godoc
// @Summary Lấy danh sách phòng
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/rooms [get]
func (h *RoomHandler) ListRooms(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	filter := bson.M{}
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
			return
		}
		filter["tenant_ids"] = userID
	}

	cursor, err := roomsCol.Find(ctx, filter)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	rooms := make([]models.Room, 0)
	if err := cursor.All(ctx, &rooms); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách phòng thành công", rooms)
}

// GetRoom godoc
// @Summary Xem chi tiết một phòng
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "ID của phòng"
// @Success 200 {object} map[string]interface{}
// @Router /api/rooms/{id} [get]
func (h *RoomHandler) GetRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		if err != nil || !containsObjectID(room.TenantIDs, userID) {
			utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem phòng này")
			return
		}
	}

	// Truy vấn danh sách Tenant đang ở trong phòng
	if len(room.TenantIDs) > 0 {
		usersCol := config.GetCollection("users")
		cursor, err := usersCol.Find(ctx, bson.M{"_id": bson.M{"$in": room.TenantIDs}})
		if err == nil {
			var tenants []models.User
			if err := cursor.All(ctx, &tenants); err == nil {
				for _, t := range tenants {
					room.Tenants = append(room.Tenants, t.ToResponse())
				}
			}
		}
	}

	utils.Success(c, http.StatusOK, "OK", room)
}

type updateRoomRequest struct {
	Name                   string            `json:"name"`
	Floor                  *int              `json:"floor"`
	Capacity               *int              `json:"capacity" binding:"omitempty,min=1"`
	MonthlyRent            *float64          `json:"monthly_rent"`
	ElectricPrice          *float64          `json:"price_electricity"`
	WaterPrice             *float64          `json:"price_water"`
	Occupants              *int              `json:"occupants"`
	ManagementFeePerPerson *float64          `json:"management_fee_per_person"`
	Status                 models.RoomStatus `json:"status" binding:"omitempty,oneof=available occupied"`
	Note                   *string           `json:"note"`
}

// UpdateRoom godoc
// @Summary Cập nhật thông tin phòng
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "ID của phòng"
// @Param request body updateRoomRequest true "Dữ liệu cập nhật"
// @Success 200 {object} map[string]interface{}
// @Router /api/rooms/{id} [put]
func (h *RoomHandler) UpdateRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updateRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	update := bson.M{"updated_at": time.Now()}
	if req.Name != "" {
		update["name"] = req.Name
	}
	if req.Floor != nil {
		update["floor"] = *req.Floor
	}
	if req.MonthlyRent != nil {
		update["monthly_rent"] = *req.MonthlyRent
	}
	if req.ElectricPrice != nil {
		update["price_electricity"] = *req.ElectricPrice
	}
	if req.WaterPrice != nil {
		update["price_water"] = *req.WaterPrice
	}
	if req.ManagementFeePerPerson != nil {
		update["management_fee_per_person"] = *req.ManagementFeePerPerson
	}
	if req.Note != nil {
		update["note"] = *req.Note
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	if req.Occupants != nil || req.Status != "" {
		var current models.Room
		if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&current); err != nil {
			if err == mongo.ErrNoDocuments {
				utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}

		if req.Occupants != nil {
			if *req.Occupants != len(current.TenantIDs) {
				utils.Error(c, http.StatusConflict, "occupants phải khớp với số người thực tế đang ở phòng (tenant_ids); hãy dùng POST /api/contracts hoặc /api/users/:id/room để thêm/bớt người")
				return
			}
			update["occupants"] = *req.Occupants
		}

		if req.Status != "" {
			wantOccupied := req.Status == models.RoomStatusOccupied
			actuallyOccupied := len(current.TenantIDs) > 0
			if wantOccupied != actuallyOccupied {
				utils.Error(c, http.StatusConflict, "status không khớp với số người thực tế đang ở phòng; hãy dùng đúng luồng check-in/checkout thay vì sửa status trực tiếp")
				return
			}
			update["status"] = req.Status
		}
	}

	if req.Capacity != nil {
		var current models.Room
		if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&current); err != nil {
			if err == mongo.ErrNoDocuments {
				utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
		if len(current.TenantIDs) > *req.Capacity {
			utils.Error(c, http.StatusConflict, "Không thể giảm capacity xuống thấp hơn số người đang ở phòng")
			return
		}
		update["capacity"] = *req.Capacity
	}

	res, err := roomsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if res.MatchedCount == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật phòng thành công", nil)
}

// DeleteRoom godoc
// @Summary Xóa phòng
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "ID của phòng"
// @Success 200 {object} map[string]interface{}
// @Router /api/rooms/{id} [delete]
func (h *RoomHandler) DeleteRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}

	if len(room.TenantIDs) > 0 || room.Status == models.RoomStatusOccupied {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng đang có người thuê")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	invoiceCount, err := invoicesCol.CountDocuments(ctx, bson.M{"room_id": id})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if invoiceCount > 0 {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng đã từng có hóa đơn (sẽ làm mồ côi dữ liệu lịch sử); hãy đặt status phù hợp thay vì xóa")
		return
	}

	contractsCol := config.GetCollection("contracts")
	contractCount, err := contractsCol.CountDocuments(ctx, bson.M{"room_id": id})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contractCount > 0 {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng đã từng gắn với hợp đồng thuê (sẽ làm mồ côi dữ liệu lịch sử hợp đồng/cọc); hãy đặt status phù hợp thay vì xóa")
		return
	}

	if _, err := roomsCol.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể xóa phòng")
		return
	}

	utils.Success(c, http.StatusOK, "Xóa phòng thành công", nil)
}

// CheckoutRoom godoc
// @Summary Trả phòng
// @Tags Rooms
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Room ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/rooms/{id}/checkout [post]
func (h *RoomHandler) CheckoutRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if len(room.TenantIDs) == 0 {
		utils.Error(c, http.StatusConflict, "Phòng hiện đang trống, không có người thuê để trả phòng")
		return
	}

	contractsCol := config.GetCollection("contracts")
	hasActive, err := hasActiveContractForRoom(ctx, contractsCol, id)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if hasActive {
		utils.Error(c, http.StatusConflict, "Phòng đang gắn với 1 hợp đồng hiệu lực; hãy dùng POST /api/contracts/{id}/checkout (hoặc /cancel nếu chưa thu cọc) để trả phòng đúng quy trình")
		return
	}

	tenantIDs := room.TenantIDs

	_, err = roomsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set":   bson.M{"status": models.RoomStatusAvailable, "occupants": 0, "updated_at": time.Now()},
		"$unset": bson.M{"tenant_ids": ""},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật phòng")
		return
	}

	usersCol := config.GetCollection("users")
	_, err = usersCol.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": tenantIDs}}, bson.M{
		"$set":   bson.M{"updated_at": time.Now()},
		"$unset": bson.M{"room_id": ""},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Trả phòng thành công nhưng cập nhật tài khoản người thuê thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Trả phòng thành công", gin.H{
		"room_id":    id,
		"tenant_ids": tenantIDs,
	})
}
