package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"rental-app/internal/utils"
)

type SystemHandler struct{}

func NewSystemHandler() *SystemHandler {
	return &SystemHandler{}
}

// RunDailyJobs godoc
// @Summary Chạy tay các cron job hàng ngày (test/vận hành)
// @Description Chạy ngay lập tức các job mà bình thường cron chạy tự động lúc
// @Description nửa đêm: quét hóa đơn quá hạn, quét hợp đồng sắp hết hạn. Hữu
// @Description ích để kiểm tra ngay không cần chờ tới giờ cron hoặc qua ngày mới.
// @Description Không bao gồm tạo hóa đơn nháp đầu tháng - dùng riêng
// @Description POST /api/invoices/generate-draft cho việc đó.
// @Tags System
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/system/run-daily-jobs [post]
func (h *SystemHandler) RunDailyJobs(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	flagged, unflagged, err := RunOverdueInvoiceCheck(ctx)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi khi quét hóa đơn quá hạn: "+err.Error())
		return
	}

	reminded, err := RunContractExpiryCheck(ctx)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi khi quét hợp đồng sắp hết hạn: "+err.Error())
		return
	}

	utils.Success(c, http.StatusOK, "Đã chạy xong các cron job hàng ngày", gin.H{
		"overdue_invoices_flagged":   flagged,
		"overdue_invoices_unflagged": unflagged,
		"contracts_expiry_reminded":  reminded,
	})
}
