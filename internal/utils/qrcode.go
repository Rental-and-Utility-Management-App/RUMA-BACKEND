package utils

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// invoiceRefCodePattern nhận diện mã tham chiếu hóa đơn dạng "HD" + 8 ký tự hex,
// dùng để bóc tách từ nội dung chuyển khoản do webhook SePay gửi về.
var invoiceRefCodePattern = regexp.MustCompile(`(?i)HD[0-9A-F]{8}`)

// depositRefCodePattern nhận diện mã tham chiếu tiền cọc dạng "CK" + 8 ký tự hex
// (CK = "cọc"), tương tự invoiceRefCodePattern nhưng dùng riêng cho cọc để
// webhook phân biệt được đây là giao dịch thu cọc, không phải thanh toán hóa đơn.
var depositRefCodePattern = regexp.MustCompile(`(?i)CK[0-9A-F]{8}`)

// GenerateInvoiceRefCode sinh mã tham chiếu ngắn, duy nhất cho 1 hóa đơn, dùng làm
// nội dung chuyển khoản để đối soát tự động khi có webhook báo tiền về (SePay/Casso...).
// Dạng: "HD" + 8 ký tự hex cuối của ObjectID, in hoa. Vd: "HDA1B2C3D4"... (thực tế 8 ký tự).
func GenerateInvoiceRefCode(id primitive.ObjectID) string {
	hex := id.Hex()
	suffix := hex[len(hex)-8:]
	return "HD" + strings.ToUpper(suffix)
}

// ExtractInvoiceRefCode bóc tách mã tham chiếu hóa đơn (nếu có) từ 1 đoạn text
// (nội dung/diễn giải giao dịch ngân hàng). Trả về mã đã chuẩn hóa IN HOA và true
// nếu tìm thấy, ngược lại trả về "" và false.
func ExtractInvoiceRefCode(text string) (string, bool) {
	match := invoiceRefCodePattern.FindString(text)
	if match == "" {
		return "", false
	}
	return strings.ToUpper(match), true
}

// GenerateDepositRefCode sinh mã tham chiếu ngắn, duy nhất cho tiền cọc của 1
// hợp đồng, dùng làm nội dung chuyển khoản để webhook tự động nhận diện đây là
// giao dịch thu cọc (khác với thanh toán hóa đơn) và đối soát về đúng hợp đồng.
// Dạng: "CK" + 8 ký tự hex cuối của ObjectID, in hoa.
func GenerateDepositRefCode(id primitive.ObjectID) string {
	hex := id.Hex()
	suffix := hex[len(hex)-8:]
	return "CK" + strings.ToUpper(suffix)
}

// ExtractDepositRefCode bóc tách mã tham chiếu cọc (nếu có) từ nội dung/diễn
// giải giao dịch ngân hàng. Trả về mã đã chuẩn hóa IN HOA và true nếu tìm thấy.
func ExtractDepositRefCode(text string) (string, bool) {
	match := depositRefCodePattern.FindString(text)
	if match == "" {
		return "", false
	}
	return strings.ToUpper(match), true
}

// vietnameseToneMap ánh xạ ký tự có dấu tiếng Việt -> không dấu.
// Nội dung chuyển khoản (addInfo) nên bỏ dấu vì một số app ngân hàng
// hiển thị/nhận sai ký tự Unicode có dấu trong content chuyển khoản.
var vietnameseToneMap = map[rune]rune{
	'à': 'a', 'á': 'a', 'ạ': 'a', 'ả': 'a', 'ã': 'a',
	'â': 'a', 'ầ': 'a', 'ấ': 'a', 'ậ': 'a', 'ẩ': 'a', 'ẫ': 'a',
	'ă': 'a', 'ằ': 'a', 'ắ': 'a', 'ặ': 'a', 'ẳ': 'a', 'ẵ': 'a',
	'è': 'e', 'é': 'e', 'ẹ': 'e', 'ẻ': 'e', 'ẽ': 'e',
	'ê': 'e', 'ề': 'e', 'ế': 'e', 'ệ': 'e', 'ể': 'e', 'ễ': 'e',
	'ì': 'i', 'í': 'i', 'ị': 'i', 'ỉ': 'i', 'ĩ': 'i',
	'ò': 'o', 'ó': 'o', 'ọ': 'o', 'ỏ': 'o', 'õ': 'o',
	'ô': 'o', 'ồ': 'o', 'ố': 'o', 'ộ': 'o', 'ổ': 'o', 'ỗ': 'o',
	'ơ': 'o', 'ờ': 'o', 'ớ': 'o', 'ợ': 'o', 'ở': 'o', 'ỡ': 'o',
	'ù': 'u', 'ú': 'u', 'ụ': 'u', 'ủ': 'u', 'ũ': 'u',
	'ư': 'u', 'ừ': 'u', 'ứ': 'u', 'ự': 'u', 'ử': 'u', 'ữ': 'u',
	'ỳ': 'y', 'ý': 'y', 'ỵ': 'y', 'ỷ': 'y', 'ỹ': 'y',
	'đ': 'd',
	'À': 'A', 'Á': 'A', 'Ạ': 'A', 'Ả': 'A', 'Ã': 'A',
	'Â': 'A', 'Ầ': 'A', 'Ấ': 'A', 'Ậ': 'A', 'Ẩ': 'A', 'Ẫ': 'A',
	'Ă': 'A', 'Ằ': 'A', 'Ắ': 'A', 'Ặ': 'A', 'Ẳ': 'A', 'Ẵ': 'A',
	'È': 'E', 'É': 'E', 'Ẹ': 'E', 'Ẻ': 'E', 'Ẽ': 'E',
	'Ê': 'E', 'Ề': 'E', 'Ế': 'E', 'Ệ': 'E', 'Ể': 'E', 'Ễ': 'E',
	'Ì': 'I', 'Í': 'I', 'Ị': 'I', 'Ỉ': 'I', 'Ĩ': 'I',
	'Ò': 'O', 'Ó': 'O', 'Ọ': 'O', 'Ỏ': 'O', 'Õ': 'O',
	'Ô': 'O', 'Ồ': 'O', 'Ố': 'O', 'Ộ': 'O', 'Ổ': 'O', 'Ỗ': 'O',
	'Ơ': 'O', 'Ờ': 'O', 'Ớ': 'O', 'Ợ': 'O', 'Ở': 'O', 'Ỡ': 'O',
	'Ù': 'U', 'Ú': 'U', 'Ụ': 'U', 'Ủ': 'U', 'Ũ': 'U',
	'Ư': 'U', 'Ừ': 'U', 'Ứ': 'U', 'Ự': 'U', 'Ử': 'U', 'Ữ': 'U',
	'Ỳ': 'Y', 'Ý': 'Y', 'Ỵ': 'Y', 'Ỷ': 'Y', 'Ỹ': 'Y',
	'Đ': 'D',
}

// RemoveVietnameseTones bỏ dấu tiếng Việt, dùng cho nội dung chuyển khoản
// để tối đa khả năng tương thích với các app ngân hàng.
func RemoveVietnameseTones(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if mapped, ok := vietnameseToneMap[r]; ok {
			b.WriteRune(mapped)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// VietQRParams chứa thông tin cần thiết để build URL ảnh QR chuyển khoản.
type VietQRParams struct {
	BankID      string // BIN hoặc mã ngân hàng theo chuẩn VietQR, vd: "970436"
	AccountNo   string // Số tài khoản
	AccountName string // Tên chủ tài khoản
	Template    string // compact2 | compact | qr_only | print
	Amount      float64
	AddInfo     string // Nội dung chuyển khoản
}

// BuildVietQRImageURL build URL ảnh QR theo chuẩn VietQR (img.vietqr.io).
// Trả về "" nếu chưa cấu hình BankID/AccountNo (chưa đủ thông tin để tạo QR).
//
// Đây là dịch vụ ảnh QR công khai của VietQR (Napas + 40+ ngân hàng tại VN
// đứng sau chuẩn này), không cần API key cho nhu cầu hiển thị QR tĩnh/động.
// Người dùng dùng app ngân hàng bất kỳ hỗ trợ VietQR quét là điền sẵn
// số tiền + nội dung, không cần nhập tay.
func BuildVietQRImageURL(p VietQRParams) string {
	if p.BankID == "" || p.AccountNo == "" {
		return ""
	}

	template := p.Template
	if template == "" {
		template = "compact2"
	}

	base := fmt.Sprintf("https://img.vietqr.io/image/%s-%s-%s.png",
		url.PathEscape(p.BankID), url.PathEscape(p.AccountNo), url.PathEscape(template))

	q := url.Values{}
	if p.Amount > 0 {
		q.Set("amount", fmt.Sprintf("%.0f", p.Amount))
	}
	if p.AddInfo != "" {
		q.Set("addInfo", RemoveVietnameseTones(p.AddInfo))
	}
	if p.AccountName != "" {
		q.Set("accountName", RemoveVietnameseTones(p.AccountName))
	}

	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}
