package handlers

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rental-app/internal/models"
)

// ErrRoomFull được addTenantToRoom trả về khi phòng đã đạt capacity, dùng để
// handler phân biệt với lỗi hệ thống chung và trả về 409 kèm thông báo phù hợp.
var ErrRoomFull = errors.New("room is full")

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

// hasActiveContractForRoom kiểm tra phòng có đang gắn với 1 hợp đồng "active"
// hay không. Dùng để chặn các thao tác gán/trả phòng "tắt" (không qua
// /api/contracts) khi phòng đang thuộc quản lý của 1 hợp đồng hiệu lực -
// tránh để hợp đồng bị "mồ côi" (vẫn active nhưng phòng đã trống/đổi tenant).
func hasActiveContractForRoom(ctx context.Context, contractsCol *mongo.Collection, roomID primitive.ObjectID) (bool, error) {
	count, err := contractsCol.CountDocuments(ctx, bson.M{"room_id": roomID, "status": models.ContractStatusActive})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// hasActiveContractForTenant kiểm tra tenant có đang đứng tên trong 1 hợp
// đồng "active" hay không (tương tự hasActiveContractForRoom nhưng xét theo
// phía tenant, dùng khi đổi/trả phòng cho 1 tenant cụ thể).
func hasActiveContractForTenant(ctx context.Context, contractsCol *mongo.Collection, tenantID primitive.ObjectID) (bool, error) {
	count, err := contractsCol.CountDocuments(ctx, bson.M{"tenant_ids": tenantID, "status": models.ContractStatusActive})
	if err != nil {
		return false, err
	}
	return count > 0, nil
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
		// Chặn gán vượt quá sức chứa phòng. Capacity == 0 nghĩa là phòng cũ
		// chưa được khai báo capacity -> tạm thời không giới hạn (nên yêu cầu
		// manager cập nhật capacity cho các phòng này).
		if room.Capacity > 0 && len(room.TenantIDs) >= room.Capacity {
			return nil, ErrRoomFull
		}
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
