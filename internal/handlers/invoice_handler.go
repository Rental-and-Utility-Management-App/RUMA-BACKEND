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

type InvoiceHandler struct{}

func NewInvoiceHandler() *InvoiceHandler {
	return &InvoiceHandler{}
}

type createInvoiceRequest struct {
	RoomID string `json:"room_id" binding:"required"`
	Month  int    `json:"month" binding:"required,min=1,max=12"`
	Year   int    `json:"year" binding:"required"`

	ElectricOld float64 `json:"electric_old"`
	ElectricNew float64 `json:"electric_new" binding:"required"`

	WaterOld float64 `json:"water_old"`
	WaterNew float64 `json:"water_new" binding:"required"`

	OtherFees float64 `json:"other_fees"`
	OtherNote string  `json:"other_note"`

	DueDate string `json:"due_date"` // format: 2006-01-02
}

// POST /api/invoices - manager tạo hóa đơn cho 1 phòng trong 1 tháng
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

	if req.ElectricNew < req.ElectricOld {
		utils.Error(c, http.StatusBadRequest, "Chỉ số điện mới không được nhỏ hơn chỉ số cũ")
		return
	}
	if req.WaterNew < req.WaterOld {
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

	electricAmount := (req.ElectricNew - req.ElectricOld) * room.ElectricPrice
	waterAmount := (req.WaterNew - req.WaterOld) * room.WaterPrice
	totalAmount := room.MonthlyRent + electricAmount + waterAmount + req.OtherFees

	dueDate := time.Now().AddDate(0, 0, 7)
	if req.DueDate != "" {
		if parsed, err := time.Parse("2006-01-02", req.DueDate); err == nil {
			dueDate = parsed
		}
	}

	invoice := models.Invoice{
		ID:       primitive.NewObjectID(),
		RoomID:   roomID,
		TenantID: *room.TenantID,

		Month: req.Month,
		Year:  req.Year,

		RentAmount: room.MonthlyRent,

		ElectricOld:    req.ElectricOld,
		ElectricNew:    req.ElectricNew,
		ElectricPrice:  room.ElectricPrice,
		ElectricAmount: electricAmount,

		WaterOld:    req.WaterOld,
		WaterNew:    req.WaterNew,
		WaterPrice:  room.WaterPrice,
		WaterAmount: waterAmount,

		OtherFees: req.OtherFees,
		OtherNote: req.OtherNote,

		TotalAmount: totalAmount,
		PaidAmount:  0,
		Status:      models.InvoiceStatusUnpaid,

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

// GET /api/invoices - manager xem tất cả, tenant chỉ xem hóa đơn của mình
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

// GET /api/invoices/:id
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
