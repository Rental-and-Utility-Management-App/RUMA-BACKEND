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

type PaymentHandler struct{}

func NewPaymentHandler() *PaymentHandler {
	return &PaymentHandler{}
}

type createPaymentRequest struct {
	InvoiceID string  `json:"invoice_id" binding:"required"`
	Amount    float64 `json:"amount" binding:"required,gt=0"`
	Method    string  `json:"method" binding:"required"` // cash | bank_transfer | other
	Note      string  `json:"note"`
	PaidAt    string  `json:"paid_at"` // format: 2006-01-02, mặc định là hôm nay
}

// POST /api/payments - manager ghi nhận 1 lần thanh toán cho hóa đơn
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

	method := models.PaymentMethod(req.Method)

	payment := models.Payment{
		ID:          primitive.NewObjectID(),
		InvoiceID:   invoiceID,
		RoomID:      invoice.RoomID,
		TenantID:    invoice.TenantID,
		Amount:      req.Amount,
		Method:      method,
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

// GET /api/payments - manager xem tất cả, tenant xem của mình. Lọc theo invoice_id
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
		filter["tenant_id"] = userID
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
