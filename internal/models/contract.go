package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ===================== Contract =====================

type ContractStatus string

const (
	// ContractStatusActive: hợp đồng đang hiệu lực (đã checkin, chưa checkout).
	ContractStatusActive ContractStatus = "active"
	// ContractStatusEnded: hợp đồng đã kết thúc (checkout đúng hạn hoặc sau khi gia hạn).
	ContractStatusEnded ContractStatus = "ended"
	// ContractStatusTerminated: hợp đồng bị chấm dứt SỚM (trước end_date đã thỏa thuận).
	ContractStatusTerminated ContractStatus = "terminated"
	// ContractStatusCancelled: hủy hợp đồng trước khi tenant chuyển vào (ký nhầm, đổi ý...).
	// Chỉ được phép hủy nếu chưa thu cọc (deposit_paid == 0) và chưa checkin phòng.
	ContractStatusCancelled ContractStatus = "cancelled"
)

var ValidContractStatuses = []ContractStatus{ContractStatusActive, ContractStatusEnded, ContractStatusTerminated, ContractStatusCancelled}

func (s ContractStatus) IsValid() bool {
	switch s {
	case ContractStatusActive, ContractStatusEnded, ContractStatusTerminated, ContractStatusCancelled:
		return true
	}
	return false
}

// IsClosed trả về true nếu hợp đồng đã đóng (không còn hiệu lực thuê).
func (s ContractStatus) IsClosed() bool {
	return s == ContractStatusEnded || s == ContractStatusTerminated || s == ContractStatusCancelled
}

type DepositStatus string

const (
	// DepositStatusUnpaid: chưa thu đồng cọc nào.
	DepositStatusUnpaid DepositStatus = "unpaid"
	// DepositStatusPartial: đã thu 1 phần cọc (0 < deposit_paid < deposit_amount).
	DepositStatusPartial DepositStatus = "partial"
	// DepositStatusHeld: đã thu đủ cọc, đang giữ (deposit_paid >= deposit_amount).
	DepositStatusHeld DepositStatus = "held"
	// DepositStatusPartialRefunded: đã hoàn 1 phần cọc (còn giữ 1 phần, thường do trừ phí/nợ).
	DepositStatusPartialRefunded DepositStatus = "partial_refunded"
	// DepositStatusRefunded: đã hoàn trả toàn bộ phần cọc còn giữ lại cho tenant.
	DepositStatusRefunded DepositStatus = "refunded"
	// DepositStatusForfeited: tenant vi phạm/bỏ cọc -> chủ nhà giữ toàn bộ, không hoàn lại đồng nào.
	DepositStatusForfeited DepositStatus = "forfeited"
)

var ValidDepositStatuses = []DepositStatus{
	DepositStatusUnpaid, DepositStatusPartial, DepositStatusHeld,
	DepositStatusPartialRefunded, DepositStatusRefunded, DepositStatusForfeited,
}

func (s DepositStatus) IsValid() bool {
	switch s {
	case DepositStatusUnpaid, DepositStatusPartial, DepositStatusHeld,
		DepositStatusPartialRefunded, DepositStatusRefunded, DepositStatusForfeited:
		return true
	}
	return false
}

// RenewalRecord lưu lại lịch sử mỗi lần gia hạn hợp đồng, để tra cứu về sau
// (hợp đồng có thể được gia hạn nhiều lần trong suốt vòng đời).
type RenewalRecord struct {
	OldEndDate     time.Time          `bson:"old_end_date" json:"old_end_date"`
	NewEndDate     time.Time          `bson:"new_end_date" json:"new_end_date"`
	OldMonthlyRent float64            `bson:"old_monthly_rent,omitempty" json:"old_monthly_rent,omitempty"`
	NewMonthlyRent float64            `bson:"new_monthly_rent,omitempty" json:"new_monthly_rent,omitempty"`
	Note           string             `bson:"note,omitempty" json:"note,omitempty"`
	CreatedBy      primitive.ObjectID `bson:"created_by" json:"created_by"`
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
}

// TenantBrief: thông tin rút gọn của 1 tenant, dùng để hiển thị tên trong
// danh sách/chi tiết hợp đồng mà không cần FE gọi thêm API riêng để tra tên
// theo từng ID trong tenant_ids.
type TenantBrief struct {
	ID       primitive.ObjectID `json:"id"`
	FullName string             `json:"full_name"`
	Phone    string             `json:"phone"`
}

// Contract = hợp đồng thuê phòng của 1 nhóm tenant, gắn với 1 phòng, có cọc
// và thời hạn thuê. 1 phòng tại 1 thời điểm chỉ có tối đa 1 hợp đồng active
// (được đảm bảo ở tầng handler khi tạo mới).
type Contract struct {
	ID     primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RoomID primitive.ObjectID `bson:"room_id" json:"room_id"`
	// RoomCode: snapshot mã phòng tại thời điểm ký, tiện hiển thị/tra cứu
	// mà không cần join sang collection rooms.
	RoomCode string `bson:"room_code,omitempty" json:"room_code,omitempty"`

	// TenantIDs: toàn bộ tenant đứng tên trong hợp đồng (hỗ trợ ở ghép).
	TenantIDs []primitive.ObjectID `bson:"tenant_ids" json:"tenant_ids"`

	// Tenants: thông tin rút gọn (tên, sđt) của từng phần tử trong TenantIDs.
	// KHÔNG lưu vào DB (bson:"-") — chỉ được populate ở tầng handler (xem
	// populateContractTenants trong handlers/contract.go) trước khi trả response,
	// để FE hiển thị tên thay vì phải tự tra ID.
	Tenants []TenantBrief `bson:"-" json:"tenants,omitempty"`

	// MonthlyRent: giá thuê snapshot tại thời điểm ký/gia hạn gần nhất.
	// Tách khỏi Room.MonthlyRent vì giá phòng niêm yết có thể đổi theo thời
	// gian nhưng không ảnh hưởng tới hợp đồng đã ký cố định giá.
	MonthlyRent float64 `bson:"monthly_rent" json:"monthly_rent"`

	// ---- Tiền cọc ----
	DepositAmount   float64       `bson:"deposit_amount" json:"deposit_amount"`     // số tiền cọc thỏa thuận
	DepositPaid     float64       `bson:"deposit_paid" json:"deposit_paid"`         // tổng đã thu (có thể thu nhiều lần)
	DepositRefunded float64       `bson:"deposit_refunded" json:"deposit_refunded"` // tổng đã hoàn trả
	DepositStatus   DepositStatus `bson:"deposit_status" json:"deposit_status"`

	// DepositRefCode: mã tham chiếu duy nhất dùng làm nội dung chuyển khoản khi
	// tenant chuyển cọc qua ngân hàng, giúp webhook (SePay...) tự động đối soát
	// và ghi nhận thu cọc mà không cần manager nhập tay CollectDeposit.
	DepositRefCode string `bson:"deposit_ref_code,omitempty" json:"deposit_ref_code,omitempty"`

	// ---- Thời hạn ----
	StartDate time.Time `bson:"start_date" json:"start_date"` // ngày bắt đầu hiệu lực / checkin
	EndDate   time.Time `bson:"end_date" json:"end_date"`     // ngày hết hạn dự kiến (có thể gia hạn)

	// ActualEndDate: ngày thực tế trả phòng/kết thúc hợp đồng. nil nếu còn hiệu lực.
	ActualEndDate *time.Time `bson:"actual_end_date,omitempty" json:"actual_end_date,omitempty"`

	Status ContractStatus `bson:"status" json:"status"`

	Renewals []RenewalRecord `bson:"renewals,omitempty" json:"renewals,omitempty"`

	// TerminationReason: lý do khi checkout/terminate/cancel (tenant vi phạm,
	// hết hạn không gia hạn, 2 bên thỏa thuận chấm dứt sớm...).
	TerminationReason string `bson:"termination_reason,omitempty" json:"termination_reason,omitempty"`

	Note string `bson:"note,omitempty" json:"note,omitempty"`

	// ExpiryReminderSentAt: thời điểm cron nhắc gia hạn đã xử lý hợp đồng này
	// gần nhất (tránh nhắc lặp lại mỗi ngày trong cùng 1 lần end_date sắp tới).
	ExpiryReminderSentAt *time.Time `bson:"expiry_reminder_sent_at,omitempty" json:"expiry_reminder_sent_at,omitempty"`

	CreatedBy primitive.ObjectID `bson:"created_by" json:"created_by"` // manager tạo hợp đồng
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time          `bson:"updated_at" json:"updated_at"`
}

// ===================== Deposit Transaction =====================

type DepositTxType string

const (
	DepositTxCollect DepositTxType = "collect" // thu cọc (lúc ký hoặc bổ sung)
	DepositTxRefund  DepositTxType = "refund"  // hoàn cọc khi checkout
	DepositTxForfeit DepositTxType = "forfeit" // giữ cọc do tenant vi phạm/bỏ cọc
)

var ValidDepositTxTypes = []DepositTxType{DepositTxCollect, DepositTxRefund, DepositTxForfeit}

func (t DepositTxType) IsValid() bool {
	switch t {
	case DepositTxCollect, DepositTxRefund, DepositTxForfeit:
		return true
	}
	return false
}

// DepositTransaction = 1 lần thu/hoàn/giữ cọc, gắn với 1 contract.
// Tách khỏi Payment (vốn gắn chặt với Invoice) để không phá vỡ luồng hóa đơn
// tiền phòng/điện/nước hiện có, đồng thời giữ lịch sử dòng tiền cọc riêng biệt,
// rõ ràng, dễ đối soát.
type DepositTransaction struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	ContractID primitive.ObjectID `bson:"contract_id" json:"contract_id"`
	RoomID     primitive.ObjectID `bson:"room_id" json:"room_id"`

	Type   DepositTxType `bson:"type" json:"type"`
	Amount float64       `bson:"amount" json:"amount"`
	Method PaymentMethod `bson:"method,omitempty" json:"method,omitempty"`
	Note   string        `bson:"note,omitempty" json:"note,omitempty"`

	ConfirmedBy primitive.ObjectID `bson:"confirmed_by" json:"confirmed_by"` // manager thực hiện giao dịch

	// IsAutoConfirmed: true nếu giao dịch cọc này được hệ thống tự động ghi
	// nhận qua webhook (SePay...), false nếu do manager tự tay ghi nhận.
	IsAutoConfirmed bool `bson:"is_auto_confirmed,omitempty" json:"is_auto_confirmed,omitempty"`

	// ExternalTransactionID: ID giao dịch phía nhà cung cấp webhook (SePay...),
	// dùng để chống xử lý trùng khi webhook bị gửi lại (retry). Sparse unique.
	ExternalTransactionID string `bson:"external_transaction_id,omitempty" json:"external_transaction_id,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}
