package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

type ReportHandler struct{}

func NewReportHandler() *ReportHandler {
	return &ReportHandler{}
}

// SummaryReport = báo cáo tổng hợp cho manager: doanh thu đã lập/đã thu trong
// 1 tháng cụ thể, tỷ lệ lấp đầy hiện tại, và danh sách công nợ hiện tại (hóa
// đơn unpaid/partial, có đánh dấu hóa đơn nào đã quá hạn).
type SummaryReport struct {
	Month int `json:"month"`
	Year  int `json:"year"`

	// ---- Doanh thu trong kỳ (theo month/year của invoice, KHÔNG phải paid_at) ----
	TotalInvoiced    float64 `json:"total_invoiced"`    // tổng total_amount các hóa đơn chính thức (không tính draft/cancelled) trong kỳ
	TotalCollected   float64 `json:"total_collected"`   // tổng paid_amount các hóa đơn trong kỳ
	TotalOutstanding float64 `json:"total_outstanding"` // = total_invoiced - total_collected
	InvoiceCount     int     `json:"invoice_count"`

	// ---- Tỷ lệ lấp đầy (snapshot hiện tại, không phụ thuộc month/year) ----
	TotalRooms    int     `json:"total_rooms"`
	OccupiedRooms int     `json:"occupied_rooms"`
	OccupancyRate float64 `json:"occupancy_rate"` // %, vd 87.5

	// ---- Công nợ hiện tại (snapshot hiện tại, không phụ thuộc month/year) ----
	OverdueCount  int              `json:"overdue_count"`
	OverdueAmount float64          `json:"overdue_amount"`
	UnpaidCount   int              `json:"unpaid_count"` // gồm cả unpaid + partial, kể cả chưa quá hạn
	UnpaidAmount  float64          `json:"unpaid_amount"`
	DebtList      []models.Invoice `json:"debt_list"` // chi tiết từng hóa đơn còn nợ
}

// GetSummary godoc
// @Summary Báo cáo tổng hợp (doanh thu / tỷ lệ lấp đầy / công nợ)
// @Description Manager xem nhanh doanh thu đã lập/đã thu trong 1 tháng, tỷ lệ
// @Description lấp đầy phòng hiện tại, và danh sách công nợ (hóa đơn chưa thu đủ).
// @Tags Reports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param month query int false "Tháng (mặc định tháng hiện tại)"
// @Param year query int false "Năm (mặc định năm hiện tại)"
// @Success 200 {object} map[string]interface{}
// @Router /api/reports/summary [get]
func (h *ReportHandler) GetSummary(c *gin.Context) {
	now := time.Now()
	month := queryIntOrDefault(c, "month", int(now.Month()))
	year := queryIntOrDefault(c, "year", now.Year())
	if month < 1 || month > 12 {
		utils.Error(c, http.StatusBadRequest, "Tháng không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	report := SummaryReport{Month: month, Year: year}

	invoicesCol := config.GetCollection("invoices")

	// ---- Doanh thu trong kỳ ----
	cursor, err := invoicesCol.Find(ctx, bson.M{
		"month":  month,
		"year":   year,
		"status": bson.M{"$nin": []models.InvoiceStatus{models.InvoiceStatusDraft, models.InvoiceStatusCancelled}},
	})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	var periodInvoices []models.Invoice
	if err := cursor.All(ctx, &periodInvoices); err != nil {
		cursor.Close(ctx)
		utils.Error(c, http.StatusInternalServerError, "Không thể đọc dữ liệu hóa đơn")
		return
	}
	cursor.Close(ctx)

	for _, inv := range periodInvoices {
		report.TotalInvoiced += inv.TotalAmount
		report.TotalCollected += inv.PaidAmount
		report.InvoiceCount++
	}
	report.TotalOutstanding = report.TotalInvoiced - report.TotalCollected

	// ---- Tỷ lệ lấp đầy (snapshot hiện tại) ----
	roomsCol := config.GetCollection("rooms")
	totalRooms, err := roomsCol.CountDocuments(ctx, bson.M{})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	occupiedRooms, err := roomsCol.CountDocuments(ctx, bson.M{"status": models.RoomStatusOccupied})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	report.TotalRooms = int(totalRooms)
	report.OccupiedRooms = int(occupiedRooms)
	if totalRooms > 0 {
		report.OccupancyRate = float64(occupiedRooms) / float64(totalRooms) * 100
	}

	// ---- Công nợ hiện tại (mọi hóa đơn unpaid/partial, không giới hạn theo month/year) ----
	debtCursor, err := invoicesCol.Find(ctx, bson.M{
		"status": bson.M{"$in": []models.InvoiceStatus{models.InvoiceStatusUnpaid, models.InvoiceStatusPartial}},
	}, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	var debtInvoices []models.Invoice
	if err := debtCursor.All(ctx, &debtInvoices); err != nil {
		debtCursor.Close(ctx)
		utils.Error(c, http.StatusInternalServerError, "Không thể đọc dữ liệu công nợ")
		return
	}
	debtCursor.Close(ctx)

	report.DebtList = debtInvoices
	for _, inv := range debtInvoices {
		remaining := inv.TotalAmount - inv.PaidAmount
		report.UnpaidCount++
		report.UnpaidAmount += remaining
		if inv.Overdue {
			report.OverdueCount++
			report.OverdueAmount += remaining
		}
	}

	utils.Success(c, http.StatusOK, "OK", report)
}

// queryIntOrDefault đọc 1 query param dạng số nguyên, trả về giá trị mặc định
// nếu không có hoặc parse lỗi.
func queryIntOrDefault(c *gin.Context, key string, fallback int) int {
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
