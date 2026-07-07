// Package scheduler chạy các job nền (cron) của hệ thống: tạo hóa đơn nháp
// đầu tháng, quét hóa đơn quá hạn, quét hợp đồng sắp hết hạn.
//
// Không dùng thư viện cron ngoài (robfig/cron...) để tránh thêm dependency -
// tự cài đặt 1 vòng lặp đơn giản: ngủ tới đúng giờ chạy kế tiếp (mỗi ngày lúc
// 01:00 sáng, giờ server), chạy job, rồi lặp lại.
package scheduler

import (
	"context"
	"log"
	"time"

	"rental-app/internal/handlers"
)

// dailyRunHour: giờ trong ngày (0-23, theo giờ server) mà các job hàng ngày
// sẽ chạy. Chọn giờ khuya để không ảnh hưởng tải hệ thống giờ cao điểm.
const dailyRunHour = 1

// Start khởi động scheduler chạy nền (không block). Gọi 1 lần từ main().
func Start() {
	go run()
}

func run() {
	for {
		sleepUntilNextRun()
		runDailyJobs()
	}
}

// sleepUntilNextRun ngủ tới lần chạy 01:00 kế tiếp (hôm nay nếu chưa qua giờ
// đó, hoặc 01:00 ngày mai nếu đã qua).
func sleepUntilNextRun() {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), dailyRunHour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	time.Sleep(time.Until(next))
}

// runDailyJobs chạy toàn bộ job hàng ngày, mỗi job độc lập (job này lỗi không
// chặn job khác), luôn log lại kết quả để dễ theo dõi qua log server.
func runDailyJobs() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Println("🕐 [scheduler] Bắt đầu chạy các cron job hàng ngày...")

	if flagged, unflagged, err := handlers.RunOverdueInvoiceCheck(ctx); err != nil {
		log.Printf("❌ [scheduler] Quét hóa đơn quá hạn thất bại: %v\n", err)
	} else {
		log.Printf("✅ [scheduler] Quét hóa đơn quá hạn: gắn cờ %d, gỡ cờ %d\n", flagged, unflagged)
	}

	if reminded, err := handlers.RunContractExpiryCheck(ctx); err != nil {
		log.Printf("❌ [scheduler] Quét hợp đồng sắp hết hạn thất bại: %v\n", err)
	} else {
		log.Printf("✅ [scheduler] Quét hợp đồng sắp hết hạn: nhắc %d hợp đồng\n", reminded)
	}

	// Tạo hóa đơn nháp đầu tháng: chỉ chạy vào ngày 1 hàng tháng.
	now := time.Now()
	if now.Day() == 1 {
		result, err := handlers.GenerateMonthlyDraftInvoices(ctx, int(now.Month()), now.Year())
		if err != nil {
			log.Printf("❌ [scheduler] Tạo hóa đơn nháp đầu tháng thất bại: %v\n", err)
		} else {
			log.Printf("✅ [scheduler] Tạo hóa đơn nháp đầu tháng %d/%d: tạo mới %d, bỏ qua %d, lỗi %d\n",
				now.Month(), now.Year(), result.Created, result.Skipped, len(result.Errors))
		}
	}

	log.Println("🕐 [scheduler] Hoàn tất các cron job hàng ngày.")
}
