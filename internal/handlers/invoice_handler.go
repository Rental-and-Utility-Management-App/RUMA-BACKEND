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

	if len(room.TenantIDs) == 0 {
		utils.Error(c, http.StatusConflict, "Phòng chưa có người thuê, không thể tạo hóa đơn")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	count, err := invoicesCol.CountDocuments(ctx, bson.M{
		"room_id": roomID, "month": req.Month, "year": req.Year,
		"status": bson.M{"$ne": models.InvoiceStatusCancelled}, // hóa đơn đã hủy không tính là trùng
	})
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

	// Xác định giá thuê áp dụng cho hóa đơn: nếu phòng đang có hợp đồng active,
	// PHẢI dùng monthly_rent đã chốt trong hợp đồng (snapshot lúc ký/gia hạn),
	// KHÔNG dùng room.MonthlyRent (giá niêm yết hiện tại) - tránh việc manager
	// sửa giá niêm yết để chào khách mới lại vô tình đổi luôn tiền nhà của
	// khách đang thuê giữa hợp đồng với giá đã thỏa thuận khác.
	// Chỉ fallback về giá niêm yết của phòng khi phòng chưa/không có hợp đồng
	// active (trường hợp gán phòng không qua hợp đồng chính thức).
	rentAmount := room.MonthlyRent
	contractsCol := config.GetCollection("contracts")
	var activeContract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"room_id": roomID, "status": models.ContractStatusActive}).Decode(&activeContract); err == nil {
		rentAmount = activeContract.MonthlyRent
	} else if err != mongo.ErrNoDocuments {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	occupants := room.Occupants
	if req.Occupants != nil {
		// Occupants nhập tay chỉ được chấp nhận trong khoảng hợp lý: tối thiểu 1,
		// tối đa bằng capacity của phòng (nếu có khai báo) - tránh nhập sai số để
		// thổi phồng/giảm phí quản lý.
		if *req.Occupants < 1 {
			utils.Error(c, http.StatusBadRequest, "Occupants phải lớn hơn hoặc bằng 1")
			return
		}
		if room.Capacity > 0 && *req.Occupants > room.Capacity {
			utils.Error(c, http.StatusBadRequest, "Occupants không được vượt quá capacity của phòng")
			return
		}
		occupants = *req.Occupants
	}
	managementFeeAmount := float64(occupants) * room.ManagementFeePerPerson

	totalAmount := rentAmount + electricAmount + waterAmount + managementFeeAmount + req.OtherFees

	dueDate := time.Now().AddDate(0, 0, 7)
	if req.DueDate != "" {
		if parsed, err := time.Parse("2006-01-02", req.DueDate); err == nil {
			dueDate = parsed
		}
	}

	invoiceID := primitive.NewObjectID()
	tenantIDsSnapshot := make([]primitive.ObjectID, len(room.TenantIDs))
	copy(tenantIDsSnapshot, room.TenantIDs)
	invoice := models.Invoice{
		ID:     invoiceID,
		RoomID: roomID,
		// TenantID: tenant "đại diện", giữ để tương thích ngược.
		TenantID: room.TenantIDs[0],
		// TenantIDs: snapshot MỌI tenant đang ở phòng lúc tạo hóa đơn, để mọi
		// người ở ghép đều xem được hóa đơn/lịch sử thanh toán của phòng mình
		// (thay vì chỉ người "đại diện").
		TenantIDs: tenantIDsSnapshot,

		Month: req.Month,
		Year:  req.Year,

		RentAmount: rentAmount,

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
		// Match hóa đơn mà tenant này nằm trong tenant_ids (ở ghép), hoặc là tenant
		// đại diện của hóa đơn cũ chưa có tenant_ids (tạo trước khi có field này).
		filter["$or"] = bson.A{
			bson.M{"tenant_ids": userID},
			bson.M{"tenant_id": userID, "tenant_ids": bson.M{"$exists": false}},
		}
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

	// Tenant chỉ được xem hóa đơn của phòng mình đang/đã ở (bất kỳ ai trong
	// tenant_ids, không chỉ người đại diện). Hóa đơn cũ chưa có tenant_ids thì
	// fallback so khớp tenant_id.
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		allowed := err == nil && (containsObjectID(invoice.TenantIDs, userID) ||
			(len(invoice.TenantIDs) == 0 && invoice.TenantID == userID))
		if !allowed {
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

	// Tenant chỉ được lấy QR của hóa đơn thuộc phòng mình (mọi tenant ở ghép).
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		allowed := err == nil && (containsObjectID(invoice.TenantIDs, userID) ||
			(len(invoice.TenantIDs) == 0 && invoice.TenantID == userID))
		if !allowed {
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

type updateInvoiceRequest struct {
	ElectricNew *float64 `json:"electric_new"`
	WaterNew    *float64 `json:"water_new"`
	OtherFees   *float64 `json:"other_fees"`
	OtherNote   *string  `json:"other_note"`
	Occupants   *int     `json:"occupants"`
	DueDate     string   `json:"due_date"` // format: 2006-01-02
}

// UpdateInvoice godoc
// @Summary Sửa hóa đơn
// @Description Manager sửa hóa đơn tạo sai. CHỈ cho phép sửa khi hóa đơn chưa
// @Description ghi nhận thanh toán nào (paid_amount == 0) - nếu đã có thanh
// @Description toán, hãy hủy hóa đơn (cancel) rồi tạo lại để tránh làm lệch
// @Description số tiền đã thu.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Invoice ID"
// @Param request body updateInvoiceRequest true "Dữ liệu cập nhật"
// @Success 200 {object} map[string]interface{}
// @Router /api/invoices/{id} [put]
func (h *InvoiceHandler) UpdateInvoice(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updateInvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
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

	if invoice.Status == models.InvoiceStatusCancelled {
		utils.Error(c, http.StatusConflict, "Hóa đơn đã bị hủy, không thể sửa")
		return
	}
	if invoice.PaidAmount > 0 {
		utils.Error(c, http.StatusConflict, "Hóa đơn đã ghi nhận thanh toán, không thể sửa trực tiếp - hãy hủy hóa đơn (cancel) rồi tạo lại")
		return
	}

	roomsCol := config.GetCollection("rooms")
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": invoice.RoomID}).Decode(&room); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	electricNew := invoice.ElectricNew
	if req.ElectricNew != nil {
		electricNew = *req.ElectricNew
	}
	waterNew := invoice.WaterNew
	if req.WaterNew != nil {
		waterNew = *req.WaterNew
	}
	if electricNew < invoice.ElectricOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số điện mới không được nhỏ hơn chỉ số cũ")
		return
	}
	if waterNew < invoice.WaterOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số nước mới không được nhỏ hơn chỉ số cũ")
		return
	}

	otherFees := invoice.OtherFees
	if req.OtherFees != nil {
		otherFees = *req.OtherFees
	}
	otherNote := invoice.OtherNote
	if req.OtherNote != nil {
		otherNote = *req.OtherNote
	}

	occupants := invoice.Occupants
	if req.Occupants != nil {
		if *req.Occupants < 1 {
			utils.Error(c, http.StatusBadRequest, "Occupants phải lớn hơn hoặc bằng 1")
			return
		}
		if room.Capacity > 0 && *req.Occupants > room.Capacity {
			utils.Error(c, http.StatusBadRequest, "Occupants không được vượt quá capacity của phòng")
			return
		}
		occupants = *req.Occupants
	}

	dueDate := invoice.DueDate
	if req.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "due_date không hợp lệ, đúng format 2006-01-02")
			return
		}
		dueDate = parsed
	}

	electricAmount := (electricNew - invoice.ElectricOld) * invoice.ElectricPrice
	waterAmount := (waterNew - invoice.WaterOld) * invoice.WaterPrice
	managementFeeAmount := float64(occupants) * invoice.ManagementFeePerPerson
	totalAmount := invoice.RentAmount + electricAmount + waterAmount + managementFeeAmount + otherFees

	update := bson.M{
		"electric_new":          electricNew,
		"electric_amount":       electricAmount,
		"water_new":             waterNew,
		"water_amount":          waterAmount,
		"other_fees":            otherFees,
		"other_note":            otherNote,
		"occupants":             occupants,
		"management_fee_amount": managementFeeAmount,
		"total_amount":          totalAmount,
		"due_date":              dueDate,
		"updated_at":            time.Now(),
	}

	if _, err := invoicesCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật hóa đơn")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật hóa đơn thành công", nil)
}

// CancelInvoice godoc
// @Summary Hủy hóa đơn
// @Description Manager hủy 1 hóa đơn tạo sai. CHỈ cho phép hủy khi chưa ghi
// @Description nhận thanh toán nào (paid_amount == 0), để không mất dấu vết
// @Description dòng tiền đã thu. Hóa đơn hủy vẫn được giữ lại (soft-cancel) để
// @Description tra cứu, không tính vào check trùng phòng/tháng khi tạo hóa đơn mới.
// @Tags Invoices
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Invoice ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/invoices/{id}/cancel [post]
func (h *InvoiceHandler) CancelInvoice(c *gin.Context) {
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

	if invoice.Status == models.InvoiceStatusCancelled {
		utils.Error(c, http.StatusConflict, "Hóa đơn đã bị hủy trước đó")
		return
	}
	if invoice.PaidAmount > 0 {
		utils.Error(c, http.StatusConflict, "Hóa đơn đã ghi nhận thanh toán, không thể hủy - hãy xóa các payment liên quan trước (DELETE /api/payments/:id)")
		return
	}

	if _, err := invoicesCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"status": models.InvoiceStatusCancelled, "updated_at": time.Now()},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể hủy hóa đơn")
		return
	}

	utils.Success(c, http.StatusOK, "Hủy hóa đơn thành công", nil)
}
