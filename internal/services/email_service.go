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

const resendAPIURL = "https://api.resend.com/emails"

// EmailService gửi email qua Resend HTTP API (https://resend.com), dùng port 443 (HTTPS)
// thay vì SMTP (port 25/465/587) - vì nhiều nhà cung cấp hosting (vd: Render free tier)
// chặn traffic outbound tới các port SMTP để chống spam, khiến gửi mail qua net/smtp bị
// "connection timed out". Gọi qua HTTPS API tránh được vấn đề này.
type EmailService struct {
	apiKey    string
	fromName  string
	fromEmail string
	client    *http.Client
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{
		apiKey:    cfg.ResendAPIKey,
		fromName:  cfg.ResendFromName,
		fromEmail: cfg.ResendFromEmail,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// IsConfigured kiểm tra đã có API key của Resend chưa.
func (s *EmailService) IsConfigured() bool {
	return s.apiKey != "" && s.fromEmail != ""
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

// send gửi 1 email HTML qua Resend API.
func (s *EmailService) send(to, subject, htmlBody string) error {
	if !s.IsConfigured() {
		return fmt.Errorf("Resend chưa được cấu hình (thiếu RESEND_API_KEY)")
	}

	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", s.fromName, s.fromEmail)
	}

	payload := resendRequest{
		From:    from,
		To:      []string{to},
		Subject: subject,
		HTML:    htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("không thể tạo nội dung email: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, resendAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("không thể tạo request tới Resend: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("gọi Resend API thất bại: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Resend API trả lỗi (status %d): %s", resp.StatusCode, string(respBody))
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
