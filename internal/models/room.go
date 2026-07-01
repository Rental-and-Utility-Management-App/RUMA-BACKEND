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

// Room = phòng cho thuê
type Room struct {
	ID       primitive.ObjectID  `bson:"_id,omitempty" json:"id"`
	Code     string              `bson:"code" json:"code"`
	Name     string              `bson:"name,omitempty" json:"name,omitempty"`
	Floor    int                 `bson:"floor,omitempty" json:"floor,omitempty"`
	TenantID *primitive.ObjectID `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`

	MonthlyRent   float64 `bson:"monthly_rent" json:"monthly_rent"`
	ElectricPrice float64 `bson:"electric_price" json:"electric_price"`
	WaterPrice    float64 `bson:"water_price" json:"water_price"`

	Status RoomStatus `bson:"status" json:"status"`
	Note   string     `bson:"note,omitempty" json:"note,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}
