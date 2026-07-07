package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type RoomStatus string

const (
	RoomStatusAvailable RoomStatus = "available"
	RoomStatusOccupied  RoomStatus = "occupied"
)

// ValidRoomStatuses liệt kê toàn bộ giá trị hợp lệ của RoomStatus, dùng để
// validate input (vd: oneof=... trong binding tag) hoặc hiển thị cho client.
var ValidRoomStatuses = []RoomStatus{RoomStatusAvailable, RoomStatusOccupied}

// IsValid kiểm tra RoomStatus có phải là 1 trong các giá trị enum hợp lệ không.
func (s RoomStatus) IsValid() bool {
	switch s {
	case RoomStatusAvailable, RoomStatusOccupied:
		return true
	}
	return false
}

// Room = phòng cho thuê
type Room struct {
	ID    primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Code  string             `bson:"code" json:"code"`
	Name  string             `bson:"name,omitempty" json:"name,omitempty"`
	Floor int                `bson:"floor,omitempty" json:"floor,omitempty"`

	// TenantIDs: danh sách người thuê hiện đang ở phòng này.
	// 1 phòng có thể có nhiều tenant (ở ghép); dùng $addToSet/$pull khi
	// gán/đổi/trả phòng để tránh trùng lặp phần tử.
	TenantIDs []primitive.ObjectID `bson:"tenant_ids,omitempty" json:"tenant_ids,omitempty"`

	Tenants []UserResponse `bson:"-" json:"tenants,omitempty"`

	// Capacity: so nguoi toi da duoc phep o phong nay. 0 = khong gioi han
	// (chi nen dung cho du lieu cu truoc khi co field nay; phong tao moi
	// bat buoc phai khai bao capacity > 0). Dung de chan gan/doi phong
	// vuot qua suc chua o addTenantToRoom.
	Capacity int `bson:"capacity" json:"capacity"`

	MonthlyRent            float64 `bson:"monthly_rent" json:"monthly_rent"`
	ElectricPrice          float64 `bson:"price_electricity" json:"price_electricity"`
	WaterPrice             float64 `bson:"price_water" json:"price_water"`
	Occupants              int     `bson:"occupants" json:"occupants"`                                 // số người đang ở phòng
	ManagementFeePerPerson float64 `bson:"management_fee_per_person" json:"management_fee_per_person"` // đơn giá phí quản lý / người / tháng

	Status RoomStatus `bson:"status" json:"status"`
	Note   string     `bson:"note,omitempty" json:"note,omitempty"`

	// CurrentMonthPayment: tình trạng thanh toán tiền phòng của THÁNG HIỆN TẠI.
	// Đây là field tính động (KHÔNG lưu DB, bson:"-"), được handler tự query và
	// gắn vào mỗi khi trả về ListRooms/GetRoom, để FE hiển thị ngay "phòng này
	// tháng này đã đóng tiền chưa" mà không cần tự gọi thêm API hóa đơn.
	CurrentMonthPayment *RoomCurrentMonthPayment `bson:"-" json:"current_month_payment,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// RoomPaymentStatus: trạng thái thanh toán rút gọn hiển thị cho FE, suy ra từ
// InvoiceStatus của hóa đơn tháng hiện tại (nếu có).
type RoomPaymentStatus string

const (
	// RoomPaymentStatusNoInvoice: tháng này phòng CHƯA có hóa đơn nào (phòng
	// trống, hợp đồng mới ký chưa tới kỳ tạo hóa đơn, hoặc cron chưa chạy).
	RoomPaymentStatusNoInvoice RoomPaymentStatus = "no_invoice"
	// RoomPaymentStatusDraft: đã có hóa đơn nhưng còn ở dạng nháp (cron tự tạo,
	// chưa có chỉ số điện/nước thật) - chưa xác nhận thì tenant cũng chưa thấy.
	RoomPaymentStatusDraft   RoomPaymentStatus = "draft"
	RoomPaymentStatusUnpaid  RoomPaymentStatus = "unpaid"
	RoomPaymentStatusPartial RoomPaymentStatus = "partial"
	RoomPaymentStatusPaid    RoomPaymentStatus = "paid"
)

// RoomCurrentMonthPayment tóm tắt tình trạng thanh toán tiền phòng của tháng
// hiện tại, kèm vài thông tin cần thiết để FE hiển thị mà không phải gọi thêm
// API hóa đơn (vd: hiện badge đỏ "quá hạn", hiện số tiền còn thiếu...).
type RoomCurrentMonthPayment struct {
	Month  int               `json:"month"`
	Year   int               `json:"year"`
	Status RoomPaymentStatus `json:"status"`

	// Các field dưới đây chỉ có giá trị khi Status != no_invoice.
	InvoiceID   primitive.ObjectID `json:"invoice_id,omitempty"`
	TotalAmount float64            `json:"total_amount,omitempty"`
	PaidAmount  float64            `json:"paid_amount,omitempty"`
	DueDate     *time.Time         `json:"due_date,omitempty"`
	Overdue     bool               `json:"overdue,omitempty"`
}
