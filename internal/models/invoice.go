package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type InvoiceStatus string

const (
	InvoiceStatusUnpaid  InvoiceStatus = "unpaid"
	InvoiceStatusPartial InvoiceStatus = "partial"
	InvoiceStatusPaid    InvoiceStatus = "paid"
)

// Invoice = hóa đơn của 1 phòng trong 1 tháng, gồm tiền nhà + điện + nước
type Invoice struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	RoomID   primitive.ObjectID `bson:"room_id" json:"room_id"`
	TenantID primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	Month int `bson:"month" json:"month"` // 1-12
	Year  int `bson:"year" json:"year"`

	RentAmount float64 `bson:"rent_amount" json:"rent_amount"`

	ElectricOld    float64 `bson:"electric_old" json:"electric_old"` // chỉ số cũ
	ElectricNew    float64 `bson:"electric_new" json:"electric_new"` // chỉ số mới
	ElectricPrice  float64 `bson:"electric_price" json:"electric_price"`
	ElectricAmount float64 `bson:"electric_amount" json:"electric_amount"` // = (new-old)*price

	WaterOld    float64 `bson:"water_old" json:"water_old"`
	WaterNew    float64 `bson:"water_new" json:"water_new"`
	WaterPrice  float64 `bson:"water_price" json:"water_price"`
	WaterAmount float64 `bson:"water_amount" json:"water_amount"`

	OtherFees float64 `bson:"other_fees,omitempty" json:"other_fees,omitempty"` // phí khác: rác, internet...
	OtherNote string  `bson:"other_note,omitempty" json:"other_note,omitempty"`

	Occupants              int     `bson:"occupants" json:"occupants"`                                 // số người ở phòng tại thời điểm chốt hóa đơn
	ManagementFeePerPerson float64 `bson:"management_fee_per_person" json:"management_fee_per_person"` // đơn giá phí quản lý/người dùng để tính tháng này
	ManagementFeeAmount    float64 `bson:"management_fee_amount" json:"management_fee_amount"`         // = occupants * management_fee_per_person

	TotalAmount float64       `bson:"total_amount" json:"total_amount"`
	PaidAmount  float64       `bson:"paid_amount" json:"paid_amount"`
	Status      InvoiceStatus `bson:"status" json:"status"`

	DueDate   time.Time `bson:"due_date" json:"due_date"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}
