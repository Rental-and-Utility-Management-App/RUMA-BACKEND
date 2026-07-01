package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"rental-app/internal/utils"
)

// RateLimiter giới hạn số request theo IP trong một khoảng thời gian (fixed window).
// Dùng cho các endpoint dễ bị brute-force như đăng nhập.
// Đây là rate limiter in-memory, phù hợp khi chạy 1 instance. Nếu scale nhiều instance
// (nhiều pod/container), nên thay bằng Redis để đếm request dùng chung.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    int
	window   time.Duration
}

type visitor struct {
	count     int
	resetTime time.Time
}

// NewRateLimiter tạo rate limiter cho phép tối đa `limit` request mỗi `window` thời gian, theo từng IP.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		window:   window,
	}
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop dọn định kỳ các IP đã hết hạn window để tránh memory leak.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, v := range rl.visitors {
			if now.After(v.resetTime) {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	v, exists := rl.visitors[ip]

	if !exists || now.After(v.resetTime) {
		rl.visitors[ip] = &visitor{count: 1, resetTime: now.Add(rl.window)}
		return true
	}

	if v.count >= rl.limit {
		return false
	}

	v.count++
	return true
}

// Middleware trả về gin.HandlerFunc chặn request nếu IP vượt quá giới hạn.
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()

		if !rl.allow(ip) {
			utils.Error(c, http.StatusTooManyRequests, "Bạn thao tác quá nhanh, vui lòng thử lại sau ít phút")
			c.Abort()
			return
		}

		c.Next()
	}
}
