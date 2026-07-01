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

// Payment = 1 lần thanh toán, gắn với 1 invoice (invoice có thể thanh toán nhiều lần)
type Payment struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	InvoiceID primitive.ObjectID `bson:"invoice_id" json:"invoice_id"`
	RoomID    primitive.ObjectID `bson:"room_id" json:"room_id"`
	TenantID  primitive.ObjectID `bson:"tenant_id" json:"tenant_id"`

	Amount      float64       `bson:"amount" json:"amount"`
	Method      PaymentMethod `bson:"method" json:"method"`
	Note        string        `bson:"note,omitempty" json:"note,omitempty"`
	ConfirmedBy primitive.ObjectID `bson:"confirmed_by" json:"confirmed_by"` // manager xác nhận

	PaidAt    time.Time `bson:"paid_at" json:"paid_at"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}