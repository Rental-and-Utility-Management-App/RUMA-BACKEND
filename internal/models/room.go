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

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}
