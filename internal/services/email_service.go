package services

import (
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"rental-app/internal/config"
)

// EmailService gửi email qua SMTP (vd: Gmail SMTP + App Password).
// Dùng net/smtp có sẵn trong Go, không cần thêm dependency ngoài vào go.mod.
type EmailService struct {
	host      string
	port      string
	username  string
	password  string
	fromName  string
	fromEmail string
}

func NewEmailService(cfg *config.Config) *EmailService {
	return &EmailService{
		host:      cfg.SMTPHost,
		port:      cfg.SMTPPort,
		username:  cfg.SMTPUsername,
		password:  cfg.SMTPPassword,
		fromName:  cfg.SMTPFromName,
		fromEmail: cfg.SMTPFromEmail,
	}
}

// IsConfigured kiểm tra đã đủ thông tin SMTP để gửi mail chưa.
func (s *EmailService) IsConfigured() bool {
	return s.host != "" && s.port != "" && s.username != "" && s.password != "" && s.fromEmail != ""
}

// send gửi 1 email HTML đơn giản tới 1 địa chỉ duy nhất.
func (s *EmailService) send(to, subject, htmlBody string) error {
	if !s.IsConfigured() {
		return fmt.Errorf("SMTP chưa được cấu hình (thiếu SMTP_USERNAME/SMTP_PASSWORD)")
	}

	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", s.fromName, s.fromEmail)
	}

	headers := map[string]string{
		"From":         from,
		"To":           to,
		"Subject":      subject,
		"MIME-Version": "1.0",
		"Content-Type": "text/html; charset=\"UTF-8\"",
	}

	var msg strings.Builder
	for k, v := range headers {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	addr := fmt.Sprintf("%s:%s", s.host, s.port)

	// net/smtp.SendMail tự động dùng STARTTLS nếu server hỗ trợ (Gmail port 587 có hỗ trợ),
	// nên không cần tự quản lý TLS handshake thủ công.
	return smtp.SendMail(addr, auth, s.fromEmail, []string{to}, []byte(msg.String()))
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
