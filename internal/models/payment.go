package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type PaymentMethod string

const (
	PaymentMethodCash     PaymentMethod = "cash"
	PaymentMethodTransfer PaymentMethod = "bank_transfer"
	PaymentMethodOther    PaymentMethod = "other"
)

// ValidPaymentMethods liệt kê toàn bộ giá trị hợp lệ, dùng để validate input.
var ValidPaymentMethods = []PaymentMethod{PaymentMethodCash, PaymentMethodTransfer, PaymentMethodOther}

// IsValid kiểm tra PaymentMethod có phải là 1 trong các giá trị enum hợp lệ không.
func (m PaymentMethod) IsValid() bool {
	switch m {
	case PaymentMethodCash, PaymentMethodTransfer, PaymentMethodOther:
		return true
	}
	return false
}

// Payment = 1 lần thanh toán, gắn với 1 invoice (invoice có thể thanh toán nhiều lần)
type Payment struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	InvoiceID primitive.ObjectID `bson:"invoice_id" json:"invoice_id"`
	RoomID    primitive.ObjectID `bson:"room_id" json:"room_id"`

	// TenantID: tenant "đại diện" của hóa đơn liên quan. Giữ để tương thích ngược.
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	// TenantIDs: copy từ invoice.TenantIDs tại thời điểm ghi nhận thanh toán,
	// để mọi tenant ở ghép cùng phòng đều xem được lịch sử thanh toán của phòng.
	TenantIDs []primitive.ObjectID `bson:"tenant_ids,omitempty" json:"tenant_ids,omitempty"`

	Amount      float64            `bson:"amount" json:"amount"`
	Method      PaymentMethod      `bson:"method" json:"method"`
	Note        string             `bson:"note,omitempty" json:"note,omitempty"`
	ConfirmedBy primitive.ObjectID `bson:"confirmed_by" json:"confirmed_by"` // manager xác nhận; để trống (zero) nếu hệ thống tự xác nhận qua webhook

	// IsAutoConfirmed: true nếu thanh toán này được hệ thống tự động ghi nhận qua
	// webhook (SePay...), false nếu do manager tự tay ghi nhận.
	IsAutoConfirmed bool `bson:"is_auto_confirmed" json:"is_auto_confirmed"`

	// ExternalTransactionID: ID giao dịch phía nhà cung cấp webhook (SePay...),
	// dùng để chống xử lý trùng khi webhook bị gửi lại (retry).
	// Có index unique dạng sparse -> chỉ áp dụng cho các document có field này.
	ExternalTransactionID string `bson:"external_transaction_id,omitempty" json:"external_transaction_id,omitempty"`

	PaidAt    time.Time `bson:"paid_at" json:"paid_at"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}
