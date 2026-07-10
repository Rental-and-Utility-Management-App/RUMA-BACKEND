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

const sendGridAPIURL = "https://api.sendgrid.com/v3/mail/send"

// EmailService gửi email qua SendGrid HTTP API (https://sendgrid.com), dùng port 443 (HTTPS)
// thay vì SMTP (port 25/465/587) - vì nhiều nhà cung cấp hosting (vd: Render free tier)
// chặn traffic outbound tới các port SMTP để chống spam, khiến gửi mail qua net/smtp bị
// "connection timed out". Gọi qua HTTPS API tránh được vấn đề này.
//
// Sender (SendGridFromEmail) cần được verify qua "Single Sender Verification" trong
// SendGrid Dashboard > Settings > Sender Authentication - không cần sở hữu domain riêng,
// chỉ cần bấm link xác nhận gửi vào chính email đó. Sau khi verify, gửi được tới bất kỳ
// người nhận nào (khác với Resend sandbox chỉ cho gửi về đúng email chủ tài khoản).
type EmailService struct {
	apiKey    string
	fromName  string
	fromEmail string
	client    *http.Client
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{
		apiKey:    cfg.SendGridAPIKey,
		fromName:  cfg.SendGridFromName,
		fromEmail: cfg.SendGridFromEmail,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// IsConfigured kiểm tra đã có API key + sender email của SendGrid chưa.
func (s *EmailService) IsConfigured() bool {
	return s.apiKey != "" && s.fromEmail != ""
}

type sendGridEmailAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sendGridPersonalization struct {
	To []sendGridEmailAddress `json:"to"`
}

type sendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type sendGridRequest struct {
	Personalizations []sendGridPersonalization `json:"personalizations"`
	From             sendGridEmailAddress      `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendGridContent         `json:"content"`
}

// send gửi 1 email HTML qua SendGrid API.
func (s *EmailService) send(to, subject, htmlBody string) error {
	if !s.IsConfigured() {
		return fmt.Errorf("SendGrid chưa được cấu hình (thiếu SENDGRID_API_KEY/SENDGRID_FROM_EMAIL)")
	}

	payload := sendGridRequest{
		Personalizations: []sendGridPersonalization{
			{To: []sendGridEmailAddress{{Email: to}}},
		},
		From:    sendGridEmailAddress{Email: s.fromEmail, Name: s.fromName},
		Subject: subject,
		Content: []sendGridContent{
			{Type: "text/html", Value: htmlBody},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("không thể tạo nội dung email: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, sendGridAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("không thể tạo request tới SendGrid: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("gọi SendGrid API thất bại: %w", err)
	}
	defer resp.Body.Close()

	// SendGrid trả 202 Accepted khi thành công, không có body.
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SendGrid API trả lỗi (status %d): %s", resp.StatusCode, string(respBody))
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
