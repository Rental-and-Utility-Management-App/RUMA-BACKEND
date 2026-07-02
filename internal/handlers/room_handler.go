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
	Code  string `json:"code" binding:"required"`
	Name  string `json:"name"`
	Floor int    `json:"floor"`
	// Capacity: số người tối đa được phép ở phòng, bắt buộc phải khai báo khi tạo
	// phòng mới để hệ thống có thể chặn gán quá tải ngay từ đầu.
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
// @Description Manager tạo phòng mới. Cần quyền Manager.
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
// @Description Manager xem tất cả phòng, Tenant chỉ xem phòng của mình.
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
		filter["tenant_ids"] = userID // Mongo tự khớp nếu userID nằm trong mảng tenant_ids
	}

	cursor, err := roomsCol.Find(ctx, filter)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	// Fix: Khởi tạo mảng rỗng để frontend không bị lỗi null
	rooms := make([]models.Room, 0)
	if err := cursor.All(ctx, &rooms); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách phòng thành công", rooms)
}

// GetRoom godoc
// @Summary Xem chi tiết một phòng
// @Description Lấy thông tin chi tiết phòng theo ID.
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

	utils.Success(c, http.StatusOK, "OK", room)
}

// Sửa Note thành con trỏ *string để có thể xóa trắng ghi chú
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
// @Description Manager cập nhật thông tin phòng.
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
	if req.Occupants != nil {
		update["occupants"] = *req.Occupants
	}
	if req.ManagementFeePerPerson != nil {
		update["management_fee_per_person"] = *req.ManagementFeePerPerson
	}
	if req.Status != "" {
		update["status"] = req.Status
	}
	if req.Note != nil {
		update["note"] = *req.Note
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	// Nếu manager giảm capacity, phải chặn nếu số tenant hiện tại đã vượt quá
	// giá trị mới (tránh phòng bị "quá tải" ngay sau khi update).
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
// @Description Manager xóa phòng (Không thể xóa phòng đang có người ở).
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
	if room.Status == models.RoomStatusOccupied {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng đang có người thuê")
		return
	}

	// Chặn xóa nếu phòng đã từng có hóa đơn: xóa cứng sẽ làm invoice/payment cũ
	// mồ côi (room_id trỏ tới phòng không còn tồn tại), gây khó tra cứu lịch sử.
	// Manager nên dùng cách khác (đổi status, đổi note) để "ẩn" phòng không dùng nữa.
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

	// Tương tự: chặn xóa nếu phòng đã từng gắn với hợp đồng nào (kể cả đã đóng) -
	// xóa cứng sẽ làm contracts/deposit_transactions cũ mồ côi (room_id trỏ tới
	// phòng không còn tồn tại), mất dấu lịch sử thu/hoàn cọc.
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
// @Description Manager xác nhận người thuê trả phòng: phòng chuyển về trạng thái trống (available),
// @Description gỡ liên kết tenant khỏi phòng và gỡ phòng khỏi tài khoản tenant.
// @Description Hóa đơn/thanh toán cũ của tenant vẫn được giữ nguyên để tra cứu lịch sử.
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

	// Nếu phòng đang gắn với 1 hợp đồng active, KHÔNG cho trả phòng qua lối tắt
	// này (sẽ làm hợp đồng bị "mồ côi": vẫn active nhưng phòng đã trống, đồng
	// thời chặn tạo hợp đồng mới cho phòng do ràng buộc unique room_id+active).
	// Bắt buộc phải checkout đúng quy trình qua /api/contracts/:id/checkout
	// (hoặc /cancel nếu chưa thu cọc) để đóng hợp đồng và xử lý cọc trước.
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

	// Trả TOÀN BỘ phòng: gỡ hết tenant đang ở (phòng có thể có nhiều người).
	// Nếu chỉ muốn trả phòng cho 1 tenant cụ thể (còn người khác ở lại),
	// dùng DELETE /api/users/:id/room thay vì endpoint này.
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
		// Phòng đã được trả nhưng gỡ liên kết ở tài khoản tenant thất bại -> báo rõ để manager kiểm tra lại thủ công.
		utils.Error(c, http.StatusInternalServerError, "Trả phòng thành công nhưng cập nhật tài khoản người thuê thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Trả phòng thành công", gin.H{
		"room_id":    id,
		"tenant_ids": tenantIDs,
	})
}
