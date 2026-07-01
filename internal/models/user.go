package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Role string

const (
	RoleManager Role = "manager"
	RoleTenant  Role = "tenant"
)

type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	FullName     string             `bson:"full_name" json:"full_name"`
	Phone        string             `bson:"phone" json:"phone"`
	Email        string             `bson:"email,omitempty" json:"email,omitempty"`
	PasswordHash string             `bson:"password_hash" json:"-"`
	Role         Role               `bson:"role" json:"role"`
	RoomID       *primitive.ObjectID `bson:"room_id,omitempty" json:"room_id,omitempty"` // chỉ tenant mới có
	IsActive     bool               `bson:"is_active" json:"is_active"`
	CreatedAt    time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time          `bson:"updated_at" json:"updated_at"`
}

// Dữ liệu trả về khi login/register (không có password hash)
type UserResponse struct {
	ID       primitive.ObjectID `json:"id"`
	FullName string             `json:"full_name"`
	Phone    string             `json:"phone"`
	Email    string             `json:"email,omitempty"`
	Role     Role               `json:"role"`
	RoomID   *primitive.ObjectID `json:"room_id,omitempty"`
	IsActive bool               `json:"is_active"`
}

func (u *User) ToResponse() UserResponse {
	return UserResponse{
		ID:       u.ID,
		FullName: u.FullName,
		Phone:    u.Phone,
		Email:    u.Email,
		Role:     u.Role,
		RoomID:   u.RoomID,
		IsActive: u.IsActive,
	}
}