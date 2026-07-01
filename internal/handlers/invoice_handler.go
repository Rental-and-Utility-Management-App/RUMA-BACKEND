package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

type InvoiceHandler struct {
	cfg *config.Config
}

func NewInvoiceHandler(cfg *config.Config) *InvoiceHandler {
	return &InvoiceHandler{cfg: cfg}
}

// resolveOldReadings quyết định chỉ số điện/nước cũ dùng để tính hóa đơn:
//   - Nếu người dùng tự nhập (khác nil) -> dùng giá trị đó (ghi đè, vd: mới thay đồng hồ).
//   - Nếu để trống -> lấy electric_new/water_new của hóa đơn gần nhất cùng phòng.
//   - Nếu phòng chưa từng có hóa đơn nào -> mặc định 0 (hóa đơn đầu tiên).
func resolveOldReadings(
	ctx context.Context,
	invoicesCol *mongo.Collection,
	roomID primitive.ObjectID,
	electricOldOverride *float64,
	waterOldOverride *float64,
) (electricOld float64, waterOld float64, err error) {
	if electricOldOverride != nil {
		electricOld = *electricOldOverride
	}
	if waterOldOverride != nil {
		waterOld = *waterOldOverride
	}

	if electricOldOverride != nil && waterOldOverride != nil {
		return electricOld, waterOld, nil
	}

	var lastInvoice models.Invoice
	findErr := invoicesCol.FindOne(
		ctx,
		bson.M{"room_id": roomID},
		options.FindOne().SetSort(bson.D{{Key: "year", Value: -1}, {Key: "month", Value: -1}}),
	).Decode(&lastInvoice)

	if findErr != nil {
		if findErr == mongo.ErrNoDocuments {
			// Phòng chưa có hóa đơn nào trước đó -> giữ mặc định 0 cho phần chưa override.
			return electricOld, waterOld, nil
		}
		return 0, 0, findErr
	}

	if electricOldOverride == nil {
		electricOld = lastInvoice.ElectricNew
	}
	if waterOldOverride == nil {
		waterOld = lastInvoice.WaterNew
	}

	return electricOld, waterOld, nil
}

type createInvoiceRequest struct {
	RoomID string `json:"room_id" binding:"required"`
	Month  int    `json:"month" binding:"required,min=1,max=12"`
	Year   int    `json:"year" binding:"required"`

	// ElectricOld/WaterOld: để trống (null) để hệ thống TỰ ĐỘNG lấy chỉ số mới nhất
	// (electric_new/water_new) của hóa đơn gần nhất cùng phòng làm chỉ số cũ tháng này.
	// Chỉ cần nhập tay khi: hóa đơn đầu tiên của phòng, hoặc vừa thay đồng hồ điện/nước mới.
	ElectricOld *float64 `json:"electric_old"`
	ElectricNew float64  `json:"electric_new" binding:"required"`

	WaterOld *float64 `json:"water_old"`
	WaterNew float64  `json:"water_new" binding:"required"`

	OtherFees float64 `json:"other_fees"`
	OtherNote string  `json:"other_note"`

	// Occupants: để trống (null) để hệ thống tự lấy số người hiện tại của phòng
	// (room.occupants). Chỉ cần nhập tay nếu tháng này số người ở khác với
	// số đang lưu trên phòng (vd: có người mới dọn vào/ra giữa tháng).
	Occupants *int `json:"occupants"`

	DueDate string `json:"due_date"` // format: 2006-01-02
}

// CreateInvoice godoc
// @Summary Tạo hóa đơn
// @Description Manager tạo hóa đơn cho 1 phòng trong 1 tháng.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createInvoiceRequest true "Thông tin hóa đơn"
// @Success 201 {object} map[string]interface{}
// @Router /api/invoices [post]
func (h *InvoiceHandler) CreateInvoice(c *gin.Context) {
	var req createInvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	roomID, err := primitive.ObjectIDFromHex(req.RoomID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Room ID không hợp lệ")
		return
	}

	if req.ElectricOld != nil && req.ElectricNew < *req.ElectricOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số điện mới không được nhỏ hơn chỉ số cũ")
		return
	}
	if req.WaterOld != nil && req.WaterNew < *req.WaterOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số nước mới không được nhỏ hơn chỉ số cũ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if room.TenantID == nil {
		utils.Error(c, http.StatusConflict, "Phòng chưa có người thuê, không thể tạo hóa đơn")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	count, err := invoicesCol.CountDocuments(ctx, bson.M{"room_id": roomID, "month": req.Month, "year": req.Year})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count > 0 {
		utils.Error(c, http.StatusConflict, "Hóa đơn của phòng này trong tháng đã tồn tại")
		return
	}

	// Tự động lấy chỉ số cũ từ hóa đơn gần nhất của phòng (nếu người dùng không tự nhập).
	electricOld, waterOld, err := resolveOldReadings(ctx, invoicesCol, roomID, req.ElectricOld, req.WaterOld)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if req.ElectricNew < electricOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số điện mới không được nhỏ hơn chỉ số cũ (đã tự động lấy từ hóa đơn trước)")
		return
	}
	if req.WaterNew < waterOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số nước mới không được nhỏ hơn chỉ số cũ (đã tự động lấy từ hóa đơn trước)")
		return
	}

	electricAmount := (req.ElectricNew - electricOld) * room.ElectricPrice
	waterAmount := (req.WaterNew - waterOld) * room.WaterPrice

	occupants := room.Occupants
	if req.Occupants != nil {
		occupants = *req.Occupants
	}
	managementFeeAmount := float64(occupants) * room.ManagementFeePerPerson

	totalAmount := room.MonthlyRent + electricAmount + waterAmount + managementFeeAmount + req.OtherFees

	dueDate := time.Now().AddDate(0, 0, 7)
	if req.DueDate != "" {
		if parsed, err := time.Parse("2006-01-02", req.DueDate); err == nil {
			dueDate = parsed
		}
	}

	invoiceID := primitive.NewObjectID()
	invoice := models.Invoice{
		ID:       invoiceID,
		RoomID:   roomID,
		TenantID: *room.TenantID,

		Month: req.Month,
		Year:  req.Year,

		RentAmount: room.MonthlyRent,

		ElectricOld:    electricOld,
		ElectricNew:    req.ElectricNew,
		ElectricPrice:  room.ElectricPrice,
		ElectricAmount: electricAmount,

		WaterOld:    waterOld,
		WaterNew:    req.WaterNew,
		WaterPrice:  room.WaterPrice,
		WaterAmount: waterAmount,

		OtherFees: req.OtherFees,
		OtherNote: req.OtherNote,

		Occupants:              occupants,
		ManagementFeePerPerson: room.ManagementFeePerPerson,
		ManagementFeeAmount:    managementFeeAmount,

		TotalAmount: totalAmount,
		PaidAmount:  0,
		Status:      models.InvoiceStatusUnpaid,

		PaymentRefCode: utils.GenerateInvoiceRefCode(invoiceID),

		DueDate:   dueDate,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if _, err := invoicesCol.InsertOne(ctx, invoice); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo hóa đơn")
		return
	}

	utils.Success(c, http.StatusCreated, "Tạo hóa đơn thành công", invoice)
}

// ListInvoices godoc
// @Summary Danh sách hóa đơn
// @Description Manager xem tất cả, Tenant chỉ xem hóa đơn của mình. Có thể lọc theo room_id, status.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param room_id query string false "Lọc theo phòng"
// @Param status query string false "Lọc theo trạng thái"
// @Success 200 {object} map[string]interface{}
// @Router /api/invoices [get]
func (h *InvoiceHandler) ListInvoices(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{}
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
			return
		}
		filter["tenant_id"] = userID
	}

	if roomIDStr := c.Query("room_id"); roomIDStr != "" {
		roomID, err := primitive.ObjectIDFromHex(roomIDStr)
		if err == nil {
			filter["room_id"] = roomID
		}
	}
	if status := c.Query("status"); status != "" {
		filter["status"] = status
	}

	invoicesCol := config.GetCollection("invoices")
	cursor, err := invoicesCol.Find(ctx, filter, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	var invoices []models.Invoice
	if err := cursor.All(ctx, &invoices); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách hóa đơn thành công", invoices)
}

// GetInvoice godoc
// @Summary Xem chi tiết hóa đơn
// @Description Xem thông tin chi tiết một hóa đơn theo ID.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Invoice ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/invoices/{id} [get]
func (h *InvoiceHandler) GetInvoice(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"_id": id}).Decode(&invoice); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hóa đơn")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	// Tenant chỉ được xem hóa đơn của chính mình
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		if invoice.TenantID.Hex() != userIDStr {
			utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hóa đơn này")
			return
		}
	}

	utils.Success(c, http.StatusOK, "OK", invoice)
}

// GetInvoiceQRCode godoc
// @Summary Lấy mã QR chuyển khoản cho hóa đơn
// @Description Sinh mã VietQR (chuyển khoản ngân hàng) với số tiền = số tiền còn lại của hóa đơn,
// @Description nội dung chuyển khoản tự động điền theo mã phòng + tháng/năm.
// @Description Cần cấu hình BANK_ID, BANK_ACCOUNT_NO, BANK_ACCOUNT_NAME trong biến môi trường.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Invoice ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/invoices/{id}/qr-code [get]
func (h *InvoiceHandler) GetInvoiceQRCode(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"_id": id}).Decode(&invoice); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hóa đơn")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	// Tenant chỉ được lấy QR của hóa đơn chính mình
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		if invoice.TenantID.Hex() != userIDStr {
			utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hóa đơn này")
			return
		}
	}

	remaining := invoice.TotalAmount - invoice.PaidAmount
	if remaining <= 0 {
		utils.Error(c, http.StatusConflict, "Hóa đơn này đã được thanh toán đầy đủ")
		return
	}

	if h.cfg.BankID == "" || h.cfg.BankAccountNo == "" {
		utils.Error(c, http.StatusServiceUnavailable, "Chưa cấu hình tài khoản ngân hàng (BANK_ID, BANK_ACCOUNT_NO) để tạo mã QR")
		return
	}

	// Hóa đơn tạo trước khi có tính năng đối soát tự động sẽ chưa có ref code -> sinh và lưu lại (backfill).
	if invoice.PaymentRefCode == "" {
		invoice.PaymentRefCode = utils.GenerateInvoiceRefCode(invoice.ID)
		if _, err := invoicesCol.UpdateOne(ctx, bson.M{"_id": invoice.ID}, bson.M{
			"$set": bson.M{"payment_ref_code": invoice.PaymentRefCode, "updated_at": time.Now()},
		}); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
	}

	roomsCol := config.GetCollection("rooms")
	var room models.Room
	roomCode := ""
	if err := roomsCol.FindOne(ctx, bson.M{"_id": invoice.RoomID}).Decode(&room); err == nil {
		roomCode = room.Code
	}

	// Ref code luôn đứng đầu nội dung để tối đa khả năng ngân hàng giữ nguyên,
	// giúp webhook đối soát tự động khớp đúng hóa đơn.
	addInfo := fmt.Sprintf("%s Thanh toan P%s T%d-%d", invoice.PaymentRefCode, roomCode, invoice.Month, invoice.Year)
	if roomCode == "" {
		addInfo = fmt.Sprintf("%s Thanh toan hoa don T%d-%d", invoice.PaymentRefCode, invoice.Month, invoice.Year)
	}

	qrURL := utils.BuildVietQRImageURL(utils.VietQRParams{
		BankID:      h.cfg.BankID,
		AccountNo:   h.cfg.BankAccountNo,
		AccountName: h.cfg.BankAccountName,
		Template:    h.cfg.VietQRTemplate,
		Amount:      remaining,
		AddInfo:     addInfo,
	})

	utils.Success(c, http.StatusOK, "OK", gin.H{
		"invoice_id":       invoice.ID,
		"payment_ref_code": invoice.PaymentRefCode,
		"amount":           remaining,
		"add_info":         utils.RemoveVietnameseTones(addInfo),
		"bank_id":          h.cfg.BankID,
		"account_no":       h.cfg.BankAccountNo,
		"account_name":     h.cfg.BankAccountName,
		"qr_code_url":      qrURL,
	})
}
