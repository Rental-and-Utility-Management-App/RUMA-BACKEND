package handlers

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rental-app/internal/models"
)

// options_findSortByCreatedDesc trả về option sắp xếp mới nhất trước
func options_findSortByCreatedDesc() *options.FindOptions {
	return options.Find().SetSort(map[string]interface{}{"created_at": -1})
}

// containsObjectID kiểm tra id có nằm trong slice không (dùng để check
// tenant đã có trong room.TenantIDs chưa -> tránh gán trùng).
func containsObjectID(ids []primitive.ObjectID, target primitive.ObjectID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// removeObjectID trả về slice mới đã loại bỏ target (dùng khi đổi/trả phòng).
func removeObjectID(ids []primitive.ObjectID, target primitive.ObjectID) []primitive.ObjectID {
	result := make([]primitive.ObjectID, 0, len(ids))
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

// addTenantToRoom thêm 1 tenant vào phòng (idempotent - không tạo phần tử
// trùng nếu tenant đã có sẵn trong phòng), đồng thời đồng bộ lại
// status (-> occupied) và occupants (= số tenant hiện có).
// Trả về mongo.ErrNoDocuments nếu không tìm thấy phòng.
func addTenantToRoom(ctx context.Context, roomsCol *mongo.Collection, roomID, tenantID primitive.ObjectID) (*models.Room, error) {
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, err
	}

	if !containsObjectID(room.TenantIDs, tenantID) {
		room.TenantIDs = append(room.TenantIDs, tenantID)
	}

	_, err := roomsCol.UpdateOne(ctx, bson.M{"_id": roomID}, bson.M{
		"$set": bson.M{
			"tenant_ids": room.TenantIDs,
			"status":     models.RoomStatusOccupied,
			"occupants":  len(room.TenantIDs),
			"updated_at": time.Now(),
		},
	})
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// removeTenantFromRoom gỡ 1 tenant khỏi phòng, đồng bộ lại occupants và
// status (-> available nếu không còn ai ở). Idempotent nếu tenant không có
// sẵn trong phòng (không lỗi, chỉ đơn giản là không đổi gì).
// Trả về mongo.ErrNoDocuments nếu không tìm thấy phòng.
func removeTenantFromRoom(ctx context.Context, roomsCol *mongo.Collection, roomID, tenantID primitive.ObjectID) (*models.Room, error) {
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		return nil, err
	}

	room.TenantIDs = removeObjectID(room.TenantIDs, tenantID)

	update := bson.M{
		"occupants":  len(room.TenantIDs),
		"updated_at": time.Now(),
	}
	unset := bson.M{}
	if len(room.TenantIDs) == 0 {
		update["status"] = models.RoomStatusAvailable
		unset["tenant_ids"] = ""
	} else {
		update["status"] = models.RoomStatusOccupied
		update["tenant_ids"] = room.TenantIDs
	}

	change := bson.M{"$set": update}
	if len(unset) > 0 {
		change["$unset"] = unset
	}

	_, err := roomsCol.UpdateOne(ctx, bson.M{"_id": roomID}, change)
	if err != nil {
		return nil, err
	}
	return &room, nil
}
