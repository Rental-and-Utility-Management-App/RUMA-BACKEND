package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"rental-app/internal/config"
)

const brevoAPIURL = "https://api.brevo.com/v3/smtp/email"

// EmailService gửi email qua Brevo HTTP API (https://brevo.com), dùng port 443 (HTTPS)
// thay vì SMTP (port 25/465/587) - vì nhiều nhà cung cấp hosting (vd: Render free tier)
// chặn traffic outbound tới các port SMTP để chống spam, khiến gửi mail qua net/smtp bị
// "connection timed out". Gọi qua HTTPS API tránh được vấn đề này.
//
// Sender (BrevoSenderEmail) cần được verify trong Brevo Dashboard > Senders, Domains &
// Dedicated IPs > Senders > Add a Sender - không cần sở hữu domain riêng, chỉ cần bấm
// link xác nhận gửi vào chính email đó. Sau khi verify, gửi được tới bất kỳ người nhận nào.
type EmailService struct {
	apiKey      string
	senderName  string
	senderEmail string
	client      *http.Client
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{
		apiKey:      cfg.BrevoAPIKey,
		senderName:  cfg.BrevoSenderName,
		senderEmail: cfg.BrevoSenderEmail,
		client:      &http.Client{Timeout: 15 * time.Second},
	}
}

// IsConfigured kiểm tra đã có API key + sender email của Brevo chưa.
func (s *EmailService) IsConfigured() bool {
	return s.apiKey != "" && s.senderEmail != ""
}

type brevoEmailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type brevoRequest struct {
	Sender      brevoEmailAddress   `json:"sender"`
	To          []brevoEmailAddress `json:"to"`
	Subject     string              `json:"subject"`
	HTMLContent string              `json:"htmlContent"`
}

// send gửi 1 email HTML qua Brevo API.
func (s *EmailService) send(to, subject, htmlBody string) error {
	if !s.IsConfigured() {
		return fmt.Errorf("Brevo chưa được cấu hình (thiếu BREVO_API_KEY/BREVO_SENDER_EMAIL)")
	}

	payload := brevoRequest{
		Sender:      brevoEmailAddress{Name: s.senderName, Email: s.senderEmail},
		To:          []brevoEmailAddress{{Email: to}},
		Subject:     subject,
		HTMLContent: htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("không thể tạo nội dung email: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, brevoAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("không thể tạo request tới Brevo: %w", err)
	}
	// Brevo dùng header "api-key" riêng, KHÔNG dùng "Authorization: Bearer ..." như Resend/SendGrid.
	req.Header.Set("api-key", s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("gọi Brevo API thất bại: %w", err)
	}
	defer resp.Body.Close()

	// Brevo trả 201 Created khi thành công.
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Brevo API trả lỗi (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// SendTenantCredentials gửi email cấp tài khoản (username = số điện thoại) + mật khẩu
// cho tenant vừa được Manager tạo. Lỗi gửi mail được log lại, không nên chặn luồng tạo user.
func (s *EmailService) SendTenantCredentials(to, fullName, phone, password string) error {
	subject := "Tài khoản RUMA của bạn đã được tạo"

	body := fmt.Sprintf(`
		<div style="font-family: Arial, sans-serif; max-width: 480px; margin: 0 auto;">
			<h2 style="color:#221D0F;">Chào %s,</h2>
			<p>Tài khoản của bạn trên hệ thống <strong>RUMA</strong> đã được tạo. Dưới đây là thông tin đăng nhập:</p>
			<table style="border-collapse: collapse; margin: 16px 0;">
				<tr>
					<td style="padding:6px 12px; color:#8A8270;">Số điện thoại (tài khoản)</td>
					<td style="padding:6px 12px; font-weight:bold;">%s</td>
				</tr>
				<tr>
					<td style="padding:6px 12px; color:#8A8270;">Mật khẩu</td>
					<td style="padding:6px 12px; font-weight:bold;">%s</td>
				</tr>
			</table>
			<p style="color:#8A8270; font-size: 13px;">
				Vì lý do bảo mật, vui lòng đổi mật khẩu ngay sau lần đăng nhập đầu tiên và không chia sẻ thông tin này cho người khác.
			</p>
		</div>
	`, fullName, phone, password)

	err := s.send(to, subject, body)
	if err != nil {
		log.Printf("⚠️  Gửi email cấp tài khoản tới %s thất bại: %v", to, err)
	}
	return err
}
