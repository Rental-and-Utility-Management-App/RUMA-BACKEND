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

// resolveCurrentMonthPayment tra cứu hóa đơn của phòng trong THÁNG HIỆN TẠI
// (theo giờ server) và quy ra RoomPaymentStatus để gắn vào response phòng.
// Không tính hóa đơn "cancelled" (coi như không tồn tại).
func resolveCurrentMonthPayment(ctx context.Context, invoicesCol *mongo.Collection, roomID primitive.ObjectID) (*models.RoomCurrentMonthPayment, error) {
	now := time.Now()
	month, year := int(now.Month()), now.Year()

	var invoice models.Invoice
	err := invoicesCol.FindOne(ctx, bson.M{
		"room_id": roomID, "month": month, "year": year,
		"status": bson.M{"$ne": models.InvoiceStatusCancelled},
	}).Decode(&invoice)

	if err != nil {
		if err == mongo.ErrNoDocuments {
			return &models.RoomCurrentMonthPayment{
				Month:  month,
				Year:   year,
				Status: models.RoomPaymentStatusNoInvoice,
			}, nil
		}
		return nil, err
	}

	status := models.RoomPaymentStatusUnpaid
	switch invoice.Status {
	case models.InvoiceStatusDraft:
		status = models.RoomPaymentStatusDraft
	case models.InvoiceStatusPartial:
		status = models.RoomPaymentStatusPartial
	case models.InvoiceStatusPaid:
		status = models.RoomPaymentStatusPaid
	}

	dueDate := invoice.DueDate
	return &models.RoomCurrentMonthPayment{
		Month:       month,
		Year:        year,
		Status:      status,
		InvoiceID:   invoice.ID,
		TotalAmount: invoice.TotalAmount,
		PaidAmount:  invoice.PaidAmount,
		DueDate:     &dueDate,
		Overdue:     invoice.Overdue,
	}, nil
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
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	count, err := roomsCol.CountDocuments(ctx, bson.M{"code": req.Code})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if count > 0 {
		utils.Error(c, http.StatusConflict, "Mã phòng này đã được sử dụng, vui lòng chọn mã khác")
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
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo phòng mới, vui lòng thử lại")
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
			utils.Error(c, http.StatusBadRequest, "Không xác định được tài khoản của bạn, vui lòng đăng nhập lại")
			return
		}
		filter["tenant_ids"] = userID
	}

	cursor, err := roomsCol.Find(ctx, filter)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	defer cursor.Close(ctx)

	rooms := make([]models.Room, 0)
	if err := cursor.All(ctx, &rooms); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tải danh sách phòng, vui lòng thử lại")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	for i := range rooms {
		payment, err := resolveCurrentMonthPayment(ctx, invoicesCol, rooms[i].ID)
		if err == nil {
			rooms[i].CurrentMonthPayment = payment
		}
		// Lỗi tra cứu hóa đơn (hiếm khi xảy ra) không nên chặn cả danh sách phòng -
		// bỏ qua, phòng đó sẽ không có current_month_payment trong response.
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
		utils.Error(c, http.StatusBadRequest, "Không tìm thấy phòng này")
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
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
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

	invoicesCol := config.GetCollection("invoices")
	if payment, err := resolveCurrentMonthPayment(ctx, invoicesCol, room.ID); err == nil {
		room.CurrentMonthPayment = payment
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
		utils.Error(c, http.StatusBadRequest, "Không tìm thấy phòng này")
		return
	}

	var req updateRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
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
			utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
			return
		}

		if req.Occupants != nil {
			if *req.Occupants != len(current.TenantIDs) {
				utils.Error(c, http.StatusConflict, "Số người ở không khớp với số người đang thực tế thuê phòng. Vui lòng thêm hoặc bớt người thuê thông qua chức năng quản lý hợp đồng/người thuê thay vì sửa số liệu này trực tiếp")
				return
			}
			update["occupants"] = *req.Occupants
		}

		if req.Status != "" {
			wantOccupied := req.Status == models.RoomStatusOccupied
			actuallyOccupied := len(current.TenantIDs) > 0
			if wantOccupied != actuallyOccupied {
				utils.Error(c, http.StatusConflict, "Tình trạng phòng bạn chọn không khớp với thực tế đang có người ở hay không. Vui lòng thực hiện đúng theo quy trình nhận/trả phòng thay vì đổi tình trạng trực tiếp")
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
			utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
			return
		}
		if len(current.TenantIDs) > *req.Capacity {
			utils.Error(c, http.StatusConflict, "Không thể đặt số người tối đa thấp hơn số người đang ở trong phòng")
			return
		}
		update["capacity"] = *req.Capacity
	}

	res, err := roomsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
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
		utils.Error(c, http.StatusBadRequest, "Không tìm thấy phòng này")
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
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if invoiceCount > 0 {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng này vì đã từng phát sinh hóa đơn. Bạn có thể chuyển phòng sang trạng thái ngừng sử dụng thay vì xóa")
		return
	}

	contractsCol := config.GetCollection("contracts")
	contractCount, err := contractsCol.CountDocuments(ctx, bson.M{"room_id": id})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contractCount > 0 {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng này vì đã từng có hợp đồng thuê gắn với phòng. Bạn có thể chuyển phòng sang trạng thái ngừng sử dụng thay vì xóa")
		return
	}

	if _, err := roomsCol.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể xóa phòng, vui lòng thử lại")
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
		utils.Error(c, http.StatusBadRequest, "Không tìm thấy phòng này")
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
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}

	if len(room.TenantIDs) == 0 {
		utils.Error(c, http.StatusConflict, "Phòng hiện đang trống, không có người thuê để trả phòng")
		return
	}

	contractsCol := config.GetCollection("contracts")
	hasActive, err := hasActiveContractForRoom(ctx, contractsCol, id)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if hasActive {
		utils.Error(c, http.StatusConflict, "Phòng này đang có hợp đồng thuê còn hiệu lực. Vui lòng thực hiện trả phòng thông qua chức năng trả phòng/hủy hợp đồng để đảm bảo đúng quy trình")
		return
	}

	tenantIDs := room.TenantIDs

	_, err = roomsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set":   bson.M{"status": models.RoomStatusAvailable, "occupants": 0, "updated_at": time.Now()},
		"$unset": bson.M{"tenant_ids": ""},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật thông tin phòng, vui lòng thử lại")
		return
	}

	usersCol := config.GetCollection("users")
	_, err = usersCol.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": tenantIDs}}, bson.M{
		"$set":   bson.M{"updated_at": time.Now()},
		"$unset": bson.M{"room_id": ""},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Trả phòng thành công nhưng cập nhật thông tin người thuê chưa hoàn tất, vui lòng kiểm tra lại")
		return
	}

	utils.Success(c, http.StatusOK, "Trả phòng thành công", gin.H{
		"room_id":    id,
		"tenant_ids": tenantIDs,
	})
}
