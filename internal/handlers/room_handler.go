package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

type RoomHandler struct{}

func NewRoomHandler() *RoomHandler {
	return &RoomHandler{}
}

type createRoomRequest struct {
	Code          string  `json:"code" binding:"required"`
	Name          string  `json:"name"`
	Floor         int     `json:"floor"`
	MonthlyRent   float64 `json:"monthly_rent" binding:"required"`
	ElectricPrice float64 `json:"electric_price" binding:"required"`
	WaterPrice    float64 `json:"water_price" binding:"required"`
	Note          string  `json:"note"`
}

// POST /api/rooms - manager tạo phòng mới
func (h *RoomHandler) CreateRoom(c *gin.Context) {
	var req createRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	count, err := roomsCol.CountDocuments(ctx, bson.M{"code": req.Code})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if count > 0 {
		utils.Error(c, http.StatusConflict, "Mã phòng đã tồn tại")
		return
	}

	room := models.Room{
		ID:            primitive.NewObjectID(),
		Code:          req.Code,
		Name:          req.Name,
		Floor:         req.Floor,
		MonthlyRent:   req.MonthlyRent,
		ElectricPrice: req.ElectricPrice,
		WaterPrice:    req.WaterPrice,
		Status:        models.RoomStatusAvailable,
		Note:          req.Note,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if _, err := roomsCol.InsertOne(ctx, room); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo phòng")
		return
	}

	utils.Success(c, http.StatusCreated, "Tạo phòng thành công", room)
}

// GET /api/rooms - manager xem tất cả, tenant chỉ xem phòng của mình (được lọc ở route/handler)
func (h *RoomHandler) ListRooms(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	filter := bson.M{}
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		userID, err := primitive.ObjectIDFromHex(userIDStr)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
			return
		}
		filter["tenant_id"] = userID
	}

	cursor, err := roomsCol.Find(ctx, filter)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	var rooms []models.Room
	if err := cursor.All(ctx, &rooms); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách phòng thành công", rooms)
}

// GET /api/rooms/:id
func (h *RoomHandler) GetRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")
	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	// Tenant chỉ được xem phòng của chính mình
	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userIDStr := c.GetString("user_id")
		if room.TenantID == nil || room.TenantID.Hex() != userIDStr {
			utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem phòng này")
			return
		}
	}

	utils.Success(c, http.StatusOK, "OK", room)
}

type updateRoomRequest struct {
	Name          string   `json:"name"`
	Floor         *int     `json:"floor"`
	MonthlyRent   *float64 `json:"monthly_rent"`
	ElectricPrice *float64 `json:"electric_price"`
	WaterPrice    *float64 `json:"water_price"`
	Status        string   `json:"status"`
	Note          string   `json:"note"`
}

// PUT /api/rooms/:id - manager cập nhật thông tin phòng
func (h *RoomHandler) UpdateRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updateRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	update := bson.M{"updated_at": time.Now()}
	if req.Name != "" {
		update["name"] = req.Name
	}
	if req.Floor != nil {
		update["floor"] = *req.Floor
	}
	if req.MonthlyRent != nil {
		update["monthly_rent"] = *req.MonthlyRent
	}
	if req.ElectricPrice != nil {
		update["electric_price"] = *req.ElectricPrice
	}
	if req.WaterPrice != nil {
		update["water_price"] = *req.WaterPrice
	}
	if req.Status != "" {
		update["status"] = req.Status
	}
	if req.Note != "" {
		update["note"] = req.Note
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")
	res, err := roomsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if res.MatchedCount == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật phòng thành công", nil)
}

// DELETE /api/rooms/:id
func (h *RoomHandler) DeleteRoom(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")

	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&room); err != nil {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
		return
	}
	if room.Status == models.RoomStatusOccupied {
		utils.Error(c, http.StatusConflict, "Không thể xóa phòng đang có người thuê")
		return
	}

	if _, err := roomsCol.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể xóa phòng")
		return
	}

	utils.Success(c, http.StatusOK, "Xóa phòng thành công", nil)
}
