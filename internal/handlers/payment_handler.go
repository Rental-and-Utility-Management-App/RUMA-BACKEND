package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

type PaymentHandler struct {
	cfg *config.Config
}

func NewPaymentHandler(cfg *config.Config) *PaymentHandler {
	return &PaymentHandler{cfg: cfg}
}

type createPaymentRequest struct {
	InvoiceID string               `json:"invoice_id" binding:"required"`
	Amount    float64              `json:"amount" binding:"required,gt=0"`
	Method    models.PaymentMethod `json:"method" binding:"required,oneof=cash bank_transfer other"`
	Note      string               `json:"note"`
	PaidAt    string               `json:"paid_at"` // format: 2006-01-02, mặc định là hôm nay
}

// CreatePayment godoc
// @Summary Ghi nhận thanh toán
// @Description Manager ghi nhận 1 lần thanh toán cho hóa đơn.
// @Tags Payments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createPaymentRequest true "Thông tin thanh toán"
// @Success 201 {object} map[string]interface{}
// @Router /api/payments [post]
func (h *PaymentHandler) CreatePayment(c *gin.Context) {
	var req createPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	invoiceID, err := primitive.ObjectIDFromHex(req.InvoiceID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Invoice ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"_id": invoiceID}).Decode(&invoice); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hóa đơn")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if invoice.Status == models.InvoiceStatusCancelled {
		utils.Error(c, http.StatusConflict, "Hóa đơn này đã bị hủy, không thể ghi nhận thanh toán")
		return
	}
	if invoice.Status == models.InvoiceStatusPaid {
		utils.Error(c, http.StatusConflict, "Hóa đơn này đã được thanh toán đầy đủ")
		return
	}

	remaining := invoice.TotalAmount - invoice.PaidAmount
	if req.Amount > remaining {
		utils.Error(c, http.StatusBadRequest, "Số tiền thanh toán vượt quá số tiền còn lại")
		return
	}

	confirmedByStr := c.GetString("user_id")
	confirmedBy, err := primitive.ObjectIDFromHex(confirmedByStr)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
		return
	}

	paidAt := time.Now()
	if req.PaidAt != "" {
		if parsed, err := time.Parse("2006-01-02", req.PaidAt); err == nil {
			paidAt = parsed
		}
	}

	payment := models.Payment{
		ID:          primitive.NewObjectID(),
		InvoiceID:   invoiceID,
		RoomID:      invoice.RoomID,
		TenantID:    invoice.TenantID,
		TenantIDs:   invoice.TenantIDs,
		Amount:      req.Amount,
		Method:      req.Method,
		Note:        req.Note,
		ConfirmedBy: confirmedBy,
		PaidAt:      paidAt,
		CreatedAt:   time.Now(),
	}

	paymentsCol := config.GetCollection("payments")
	if _, err := paymentsCol.InsertOne(ctx, payment); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận thanh toán")
		return
	}

	// Cập nhật invoice: cộng dồn paid_amount, xác định lại status
	newPaidAmount := invoice.PaidAmount + req.Amount
	newStatus := models.InvoiceStatusPartial
	if newPaidAmount >= invoice.TotalAmount {
		newStatus = models.InvoiceStatusPaid
	}

	_, err = invoicesCol.UpdateOne(ctx, bson.M{"_id": invoiceID}, bson.M{
		"$set": bson.M{
			"paid_amount": newPaidAmount,
			"status":      newStatus,
			"updated_at":  time.Now(),
		},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Ghi nhận thanh toán thành công nhưng cập nhật hóa đơn thất bại")
		return
	}

	utils.Success(c, http.StatusCreated, "Ghi nhận thanh toán thành công", gin.H{
		"payment":        payment,
		"invoice_status": newStatus,
		"remaining":      invoice.TotalAmount - newPaidAmount,
	})
}

// ListPayments godoc
// @Summary Danh sách thanh toán
// @Description Manager xem tất cả, Tenant chỉ xem của mình. Có thể lọc theo invoice_id.
// @Tags Payments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param invoice_id query string false "Lọc theo hóa đơn"
// @Success 200 {object} map[string]interface{}
// @Router /api/payments [get]
func (h *PaymentHandler) ListPayments(c *gin.Context) {
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
		// Match payment mà tenant này nằm trong tenant_ids (ở ghép), hoặc là tenant
		// đại diện của payment cũ chưa có tenant_ids (tạo trước khi có field này).
		filter["$or"] = bson.A{
			bson.M{"tenant_ids": userID},
			bson.M{"tenant_id": userID, "tenant_ids": bson.M{"$exists": false}},
		}
	}

	if invoiceIDStr := c.Query("invoice_id"); invoiceIDStr != "" {
		invoiceID, err := primitive.ObjectIDFromHex(invoiceIDStr)
		if err == nil {
			filter["invoice_id"] = invoiceID
		}
	}

	paymentsCol := config.GetCollection("payments")
	cursor, err := paymentsCol.Find(ctx, filter, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	var payments []models.Payment
	if err := cursor.All(ctx, &payments); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách thanh toán thành công", payments)
}

// sepayWebhookPayload theo đúng cấu trúc SePay gửi về, xem:
// https://docs.sepay.vn/tich-hop-webhooks.html
type sepayWebhookPayload struct {
	ID              int64   `json:"id"` // ID giao dịch phía SePay, dùng để chống trùng lặp
	Gateway         string  `json:"gateway"`
	TransactionDate string  `json:"transactionDate"`
	AccountNumber   string  `json:"accountNumber"`
	SubAccount      string  `json:"subAccount"`
	Code            string  `json:"code"`
	Content         string  `json:"content"`
	TransferType    string  `json:"transferType"` // "in" | "out"
	Description     string  `json:"description"`
	TransferAmount  float64 `json:"transferAmount"`
	Accumulated     float64 `json:"accumulated"`
	ReferenceCode   string  `json:"referenceCode"`
}

// SepayWebhook godoc
// @Summary Webhook nhận báo giao dịch từ SePay
// @Description Endpoint public (không cần JWT) để SePay gọi đến mỗi khi có biến động số dư.
// @Description Xác thực bằng header Authorization: Apikey <SEPAY_WEBHOOK_API_KEY>.
// @Description Tự động đối soát theo mã tham chiếu trong nội dung chuyển khoản và ghi nhận thanh toán.
// @Tags Payments
// @Accept json
// @Produce json
// @Param request body sepayWebhookPayload true "Payload từ SePay"
// @Success 200 {object} map[string]interface{}
// @Router /api/webhooks/sepay [post]
func (h *PaymentHandler) SepayWebhook(c *gin.Context) {
	// --- Xác thực API Key ---
	// Bắt buộc phải cấu hình SEPAY_WEBHOOK_API_KEY, không cho phép bỏ qua xác thực.
	if h.cfg.SepayWebhookAPIKey == "" {
		log.Println("⚠️  Nhận webhook SePay nhưng SEPAY_WEBHOOK_API_KEY chưa được cấu hình -> từ chối")
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false})
		return
	}

	authHeader := c.GetHeader("Authorization")
	expected := "Apikey " + h.cfg.SepayWebhookAPIKey
	if subtle.ConstantTimeCompare([]byte(authHeader), []byte(expected)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false})
		return
	}

	// --- Đọc và parse payload ---
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false})
		return
	}

	var payload sepayWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false})
		return
	}

	// Chỉ xử lý giao dịch tiền VÀO. Vẫn trả success để SePay không retry vô ích.
	if payload.TransferType != "in" {
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// --- Chống xử lý trùng: nếu transaction id này đã ghi nhận rồi thì bỏ qua ---
	externalTxnID := formatSepayTxnID(payload.ID)
	paymentsCol := config.GetCollection("payments")

	var existing models.Payment
	err = paymentsCol.FindOne(ctx, bson.M{"external_transaction_id": externalTxnID}).Decode(&existing)
	if err == nil {
		// Đã xử lý từ trước (webhook gửi lại) -> báo thành công, không làm lại.
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}
	if err != mongo.ErrNoDocuments {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false})
		return
	}

	// --- Đối soát: tìm mã tham chiếu hóa đơn trong nội dung giao dịch ---
	refCode, found := utils.ExtractInvoiceRefCode(payload.Content)
	if !found {
		refCode, found = utils.ExtractInvoiceRefCode(payload.Description)
	}
	if !found {
		log.Printf("ℹ️  Webhook SePay: không tìm thấy mã hóa đơn trong nội dung \"%s\" (giao dịch id=%d)\n", payload.Content, payload.ID)
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"payment_ref_code": refCode}).Decode(&invoice); err != nil {
		if err == mongo.ErrNoDocuments {
			log.Printf("ℹ️  Webhook SePay: không tìm thấy hóa đơn với mã %s (giao dịch id=%d)\n", refCode, payload.ID)
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false})
		return
	}

	if invoice.Status == models.InvoiceStatusPaid {
		// Hóa đơn đã đủ tiền từ trước (vd: manager lỡ ghi nhận tay) -> vẫn lưu lại
		// giao dịch để không mất dấu vết dòng tiền, nhưng không cộng dồn thêm vào invoice.
		log.Printf("ℹ️  Webhook SePay: hóa đơn %s đã thanh toán đủ, chỉ lưu lại giao dịch để đối soát\n", refCode)
	}

	payment := models.Payment{
		ID:                    primitive.NewObjectID(),
		InvoiceID:             invoice.ID,
		RoomID:                invoice.RoomID,
		TenantID:              invoice.TenantID,
		TenantIDs:             invoice.TenantIDs,
		Amount:                payload.TransferAmount,
		Method:                models.PaymentMethodTransfer,
		Note:                  "Tự động xác nhận qua SePay - GD " + payload.ReferenceCode,
		ConfirmedBy:           primitive.NilObjectID, // hệ thống tự xác nhận, không phải manager
		IsAutoConfirmed:       true,
		ExternalTransactionID: externalTxnID,
		PaidAt:                time.Now(),
		CreatedAt:             time.Now(),
	}

	if _, err := paymentsCol.InsertOne(ctx, payment); err != nil {
		// Có thể do trùng unique index (webhook gửi đua nhau) -> coi như đã xử lý, báo thành công.
		if mongo.IsDuplicateKeyError(err) {
			c.JSON(http.StatusOK, gin.H{"success": true})
			return
		}
		log.Printf("❌ Webhook SePay: lỗi lưu payment cho hóa đơn %s: %v\n", refCode, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false})
		return
	}

	if invoice.Status != models.InvoiceStatusPaid {
		newPaidAmount := invoice.PaidAmount + payload.TransferAmount
		newStatus := models.InvoiceStatusPartial
		if newPaidAmount >= invoice.TotalAmount {
			newStatus = models.InvoiceStatusPaid
		}

		_, err = invoicesCol.UpdateOne(ctx, bson.M{"_id": invoice.ID}, bson.M{
			"$set": bson.M{
				"paid_amount": newPaidAmount,
				"status":      newStatus,
				"updated_at":  time.Now(),
			},
		})
		if err != nil {
			log.Printf("❌ Webhook SePay: lưu payment thành công nhưng cập nhật hóa đơn %s thất bại: %v\n", refCode, err)
			// Vẫn báo success cho SePay vì tiền + payment đã ghi nhận đúng; cần kiểm tra thủ công qua log.
		}
	}

	log.Printf("✅ Webhook SePay: tự động ghi nhận thanh toán %.0f cho hóa đơn %s\n", payload.TransferAmount, refCode)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// recalculateInvoiceAfterPaymentChange tính lại paid_amount/status của invoice
// dựa trên tổng thực tế các payment còn lại (KHÔNG cộng/trừ dồn thủ công), để
// tránh lệch số nếu có sai sót ở lần sửa/xóa trước đó.
func recalculateInvoiceAfterPaymentChange(ctx context.Context, paymentsCol, invoicesCol *mongo.Collection, invoice models.Invoice) error {
	cursor, err := paymentsCol.Find(ctx, bson.M{"invoice_id": invoice.ID})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)

	var payments []models.Payment
	if err := cursor.All(ctx, &payments); err != nil {
		return err
	}

	totalPaid := 0.0
	for _, p := range payments {
		totalPaid += p.Amount
	}

	newStatus := models.InvoiceStatusUnpaid
	if totalPaid > 0 && totalPaid < invoice.TotalAmount {
		newStatus = models.InvoiceStatusPartial
	} else if totalPaid >= invoice.TotalAmount {
		newStatus = models.InvoiceStatusPaid
	}
	// Hóa đơn đã bị hủy thì không đổi lại status dù có payment (không nên xảy ra
	// vì cancel chỉ cho phép khi paid_amount == 0, nhưng vẫn phòng hờ).
	if invoice.Status == models.InvoiceStatusCancelled {
		newStatus = models.InvoiceStatusCancelled
	}

	_, err = invoicesCol.UpdateOne(ctx, bson.M{"_id": invoice.ID}, bson.M{
		"$set": bson.M{
			"paid_amount": totalPaid,
			"status":      newStatus,
			"updated_at":  time.Now(),
		},
	})
	return err
}

type updatePaymentRequest struct {
	Amount *float64             `json:"amount" binding:"omitempty,gt=0"`
	Method models.PaymentMethod `json:"method" binding:"omitempty,oneof=cash bank_transfer other"`
	Note   *string              `json:"note"`
	PaidAt string               `json:"paid_at"` // format: 2006-01-02
}

// UpdatePayment godoc
// @Summary Sửa 1 lần thanh toán
// @Description Manager sửa lại 1 payment ghi nhận nhầm (sai số tiền/phương thức/ngày).
// @Description Invoice liên quan sẽ được tính lại paid_amount/status từ tổng
// @Description các payment thực tế còn lại. Không cho sửa payment tự động qua webhook
// @Description (is_auto_confirmed) vì phải khớp với giao dịch ngân hàng thật.
// @Tags Payments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Payment ID"
// @Param request body updatePaymentRequest true "Dữ liệu cập nhật"
// @Success 200 {object} map[string]interface{}
// @Router /api/payments/{id} [put]
func (h *PaymentHandler) UpdatePayment(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	paymentsCol := config.GetCollection("payments")
	var payment models.Payment
	if err := paymentsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&payment); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy thanh toán")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if payment.IsAutoConfirmed {
		utils.Error(c, http.StatusConflict, "Không thể sửa thanh toán tự động ghi nhận qua webhook")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"_id": payment.InvoiceID}).Decode(&invoice); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	update := bson.M{}
	if req.Amount != nil {
		update["amount"] = *req.Amount
	}
	if req.Method != "" {
		update["method"] = req.Method
	}
	if req.Note != nil {
		update["note"] = *req.Note
	}
	if req.PaidAt != "" {
		parsed, err := time.Parse("2006-01-02", req.PaidAt)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "paid_at không hợp lệ, đúng format 2006-01-02")
			return
		}
		update["paid_at"] = parsed
	}

	if len(update) == 0 {
		utils.Error(c, http.StatusBadRequest, "Không có dữ liệu để cập nhật")
		return
	}

	if _, err := paymentsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật thanh toán")
		return
	}

	if err := recalculateInvoiceAfterPaymentChange(ctx, paymentsCol, invoicesCol, invoice); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Sửa thanh toán thành công nhưng cập nhật hóa đơn thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật thanh toán thành công", nil)
}

// DeletePayment godoc
// @Summary Xóa 1 lần thanh toán
// @Description Manager xóa 1 payment ghi nhận nhầm. Invoice liên quan sẽ được
// @Description tính lại paid_amount/status từ tổng các payment thực tế còn lại.
// @Description Không cho xóa payment tự động qua webhook (is_auto_confirmed) -
// @Description dữ liệu đó phải khớp với giao dịch ngân hàng thật, không nên xóa tay.
// @Tags Payments
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Payment ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/payments/{id} [delete]
func (h *PaymentHandler) DeletePayment(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	paymentsCol := config.GetCollection("payments")
	var payment models.Payment
	if err := paymentsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&payment); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy thanh toán")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if payment.IsAutoConfirmed {
		utils.Error(c, http.StatusConflict, "Không thể xóa thanh toán tự động ghi nhận qua webhook")
		return
	}

	invoicesCol := config.GetCollection("invoices")
	var invoice models.Invoice
	if err := invoicesCol.FindOne(ctx, bson.M{"_id": payment.InvoiceID}).Decode(&invoice); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if _, err := paymentsCol.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể xóa thanh toán")
		return
	}

	if err := recalculateInvoiceAfterPaymentChange(ctx, paymentsCol, invoicesCol, invoice); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Xóa thanh toán thành công nhưng cập nhật hóa đơn thất bại")
		return
	}

	utils.Success(c, http.StatusOK, "Xóa thanh toán thành công", nil)
}

// formatSepayTxnID chuẩn hóa transaction id của SePay thành string để lưu/so khớp.
func formatSepayTxnID(id int64) string {
	return "sepay_" + strconv.FormatInt(id, 10)
}
