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

type ContractHandler struct{}

func NewContractHandler() *ContractHandler {
	return &ContractHandler{}
}

// ---------------------------------------------------------------------
// Ghi chú thiết kế:
//
//   - 1 phòng tại 1 thời điểm chỉ có tối đa 1 hợp đồng ở trạng thái "active"
//     (đảm bảo bằng partial unique index room_id+status=active, xem
//     config/database.go, kèm check ở tầng handler để trả lỗi rõ ràng).
//   - Tạo hợp đồng (CreateContract) đồng thời đóng vai trò "checkin": gán
//     luôn tenant vào phòng (tái dùng addTenantToRoom), tương tự AssignRoom.
//   - Tiền cọc được theo dõi qua DepositTransaction (collection riêng), TÁCH
//     khỏi Payment/Invoice hiện có, để không ảnh hưởng luồng hóa đơn
//     tiền phòng/điện/nước đang chạy.
//   - Checkout hợp đồng sẽ tự gỡ tenant khỏi phòng (tái dùng
//     removeTenantFromRoom), record hoàn/giữ cọc, và đóng hợp đồng.
//
// ---------------------------------------------------------------------

// canAccessContract kiểm tra quyền xem 1 hợp đồng: manager xem tất cả,
// tenant chỉ xem được hợp đồng có tên mình trong tenant_ids.
func canAccessContract(c *gin.Context, contract *models.Contract) bool {
	role := c.GetString("role")
	if role == string(models.RoleManager) {
		return true
	}
	userIDStr := c.GetString("user_id")
	userID, err := primitive.ObjectIDFromHex(userIDStr)
	if err != nil {
		return false
	}
	return containsObjectID(contract.TenantIDs, userID)
}

// resolveDepositStatusAfterCollect tính lại DepositStatus sau khi thu thêm cọc.
func resolveDepositStatusAfterCollect(depositAmount, depositPaid float64) models.DepositStatus {
	switch {
	case depositPaid <= 0:
		return models.DepositStatusUnpaid
	case depositPaid < depositAmount:
		return models.DepositStatusPartial
	default:
		return models.DepositStatusHeld
	}
}

// ===================== Create =====================

type createContractRequest struct {
	RoomID    string   `json:"room_id" binding:"required"`
	TenantIDs []string `json:"tenant_ids" binding:"required,min=1"`
	StartDate string   `json:"start_date" binding:"required"` // RFC3339, vd "2025-07-01T00:00:00Z"
	EndDate   string   `json:"end_date" binding:"required"`

	// MonthlyRent: để trống -> lấy theo giá niêm yết hiện tại của phòng.
	MonthlyRent   float64 `json:"monthly_rent"`
	DepositAmount float64 `json:"deposit_amount" binding:"min=0"`
	Note          string  `json:"note"`
}

// CreateContract godoc
// @Summary Tạo hợp đồng thuê mới (checkin)
// @Description Manager tạo hợp đồng thuê cho 1 nhóm tenant vào 1 phòng: gán
// @Description tenant vào phòng, khởi tạo thông tin cọc/thời hạn. Phòng đích
// @Description không được có hợp đồng "active" khác, và tenant không được
// @Description đang thuộc phòng nào khác.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body createContractRequest true "Thông tin hợp đồng"
// @Success 201 {object} map[string]interface{}
// @Router /api/contracts [post]
func (h *ContractHandler) CreateContract(c *gin.Context) {
	var req createContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	roomID, err := primitive.ObjectIDFromHex(req.RoomID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Room ID không hợp lệ")
		return
	}

	startDate, err := time.Parse(time.RFC3339, req.StartDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "start_date không hợp lệ (định dạng RFC3339)")
		return
	}
	endDate, err := time.Parse(time.RFC3339, req.EndDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "end_date không hợp lệ (định dạng RFC3339)")
		return
	}
	if !endDate.After(startDate) {
		utils.Error(c, http.StatusBadRequest, "end_date phải sau start_date")
		return
	}

	tenantIDs := make([]primitive.ObjectID, 0, len(req.TenantIDs))
	seen := map[primitive.ObjectID]bool{}
	for _, s := range req.TenantIDs {
		tid, err := primitive.ObjectIDFromHex(s)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "tenant_ids chứa ID không hợp lệ")
			return
		}
		if seen[tid] {
			continue
		}
		seen[tid] = true
		tenantIDs = append(tenantIDs, tid)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	roomsCol := config.GetCollection("rooms")
	usersCol := config.GetCollection("users")
	contractsCol := config.GetCollection("contracts")

	var room models.Room
	if err := roomsCol.FindOne(ctx, bson.M{"_id": roomID}).Decode(&room); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	// Không cho tạo hợp đồng mới nếu phòng đang có hợp đồng active khác.
	activeCount, err := contractsCol.CountDocuments(ctx, bson.M{"room_id": roomID, "status": models.ContractStatusActive})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if activeCount > 0 {
		utils.Error(c, http.StatusConflict, "Phòng đang có hợp đồng hiệu lực khác, không thể tạo hợp đồng mới")
		return
	}

	if room.Capacity > 0 && len(tenantIDs) > room.Capacity {
		utils.Error(c, http.StatusConflict, "Số người thuê vượt quá sức chứa (capacity) của phòng")
		return
	}

	// Validate từng tenant: phải tồn tại, đúng role, đang chưa thuộc phòng nào.
	for _, tid := range tenantIDs {
		var tenant models.User
		if err := usersCol.FindOne(ctx, bson.M{"_id": tid, "role": models.RoleTenant}).Decode(&tenant); err != nil {
			if err == mongo.ErrNoDocuments {
				utils.Error(c, http.StatusNotFound, "Không tìm thấy người thuê với ID: "+tid.Hex())
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
			return
		}
		if tenant.RoomID != nil {
			utils.Error(c, http.StatusConflict, "Người thuê '"+tenant.FullName+"' đang thuộc phòng khác, cần trả phòng trước")
			return
		}
	}

	monthlyRent := req.MonthlyRent
	if monthlyRent <= 0 {
		monthlyRent = room.MonthlyRent
	}

	now := time.Now()
	contract := models.Contract{
		ID:              primitive.NewObjectID(),
		RoomID:          roomID,
		RoomCode:        room.Code,
		TenantIDs:       tenantIDs,
		MonthlyRent:     monthlyRent,
		DepositAmount:   req.DepositAmount,
		DepositPaid:     0,
		DepositRefunded: 0,
		DepositStatus:   models.DepositStatusUnpaid,
		StartDate:       startDate,
		EndDate:         endDate,
		Status:          models.ContractStatusActive,
		Note:            req.Note,
		CreatedBy:       mustObjectIDFromCtx(c),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	// Thứ tự quan trọng để tránh race condition: insert contract TRƯỚC (được
	// bảo vệ bởi unique partial index room_id+status=active ở tầng DB - xem
	// config/database.go). Nếu 2 request tạo hợp đồng cho cùng 1 phòng chạy
	// gần như đồng thời, cả 2 đều có thể pass qua activeCount check ở trên,
	// nhưng insert sẽ chỉ có đúng 1 request thành công (request thua bị DB
	// từ chối do trùng unique index) - trước khi bất kỳ dữ liệu room/user nào
	// bị mutate, tránh để lại trạng thái "gán tenant vào phòng nhưng không có
	// hợp đồng nào đứng sau" như cách làm cũ.
	if _, err := contractsCol.InsertOne(ctx, contract); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			utils.Error(c, http.StatusConflict, "Phòng vừa được tạo hợp đồng active bởi request khác, vui lòng thử lại")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo hợp đồng")
		return
	}

	// Gán từng tenant vào phòng (idempotent, tự check capacity lần nữa để an toàn).
	// Nếu 1 bước gán thất bại giữa chừng, rollback: gỡ các tenant đã lỡ gán +
	// xóa contract vừa tạo, để không để lại trạng thái nửa vời.
	assigned := make([]primitive.ObjectID, 0, len(tenantIDs))
	rollback := func() {
		for _, tid := range assigned {
			_, _ = removeTenantFromRoom(ctx, roomsCol, roomID, tid)
			_, _ = usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{"$unset": bson.M{"room_id": ""}})
		}
		_, _ = contractsCol.DeleteOne(ctx, bson.M{"_id": contract.ID})
	}

	for _, tid := range tenantIDs {
		if _, err := addTenantToRoom(ctx, roomsCol, roomID, tid); err != nil {
			rollback()
			if err == ErrRoomFull {
				utils.Error(c, http.StatusConflict, "Phòng đã đủ số người tối đa (capacity)")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Không thể gán người thuê vào phòng, đã hủy hợp đồng vừa tạo")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set": bson.M{"room_id": roomID, "updated_at": now},
		}); err != nil {
			assigned = append(assigned, tid) // đã addTenantToRoom thành công, cần gỡ khi rollback
			rollback()
			utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật tài khoản người thuê, đã hủy hợp đồng vừa tạo")
			return
		}
		assigned = append(assigned, tid)
	}

	utils.Success(c, http.StatusCreated, "Tạo hợp đồng thành công", contract)
}

// mustObjectIDFromCtx đọc user_id từ context (đã qua AuthRequired) -> ObjectID.
// Trả về ObjectID rỗng nếu parse lỗi (không nên xảy ra vì middleware đã validate token).
func mustObjectIDFromCtx(c *gin.Context) primitive.ObjectID {
	id, err := primitive.ObjectIDFromHex(c.GetString("user_id"))
	if err != nil {
		return primitive.NilObjectID
	}
	return id
}

// ===================== List / Get =====================

// ListContracts godoc
// @Summary Danh sách hợp đồng
// @Description Manager xem tất cả (lọc được theo room_id, status). Tenant chỉ xem hợp đồng của mình.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param room_id query string false "Lọc theo phòng"
// @Param status query string false "Lọc theo trạng thái (active|ended|terminated|cancelled)"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts [get]
func (h *ContractHandler) ListContracts(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	filter := bson.M{}

	role := c.GetString("role")
	if role == string(models.RoleTenant) {
		userID, err := primitive.ObjectIDFromHex(c.GetString("user_id"))
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "User ID không hợp lệ")
			return
		}
		filter["tenant_ids"] = userID
	}

	if roomIDStr := c.Query("room_id"); roomIDStr != "" {
		roomID, err := primitive.ObjectIDFromHex(roomIDStr)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "room_id không hợp lệ")
			return
		}
		filter["room_id"] = roomID
	}

	if statusStr := c.Query("status"); statusStr != "" {
		status := models.ContractStatus(statusStr)
		if !status.IsValid() {
			utils.Error(c, http.StatusBadRequest, "status không hợp lệ")
			return
		}
		filter["status"] = status
	}

	cursor, err := contractsCol.Find(ctx, filter, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	contracts := make([]models.Contract, 0)
	if err := cursor.All(ctx, &contracts); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy danh sách hợp đồng thành công", contracts)
}

// GetContract godoc
// @Summary Xem chi tiết hợp đồng
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id} [get]
func (h *ContractHandler) GetContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}

	if !canAccessContract(c, &contract) {
		utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hợp đồng này")
		return
	}

	utils.Success(c, http.StatusOK, "OK", contract)
}

// ===================== Update (chỉnh sửa thông tin, không đổi trạng thái) =====================

type updateContractRequest struct {
	Note        *string  `json:"note"`
	MonthlyRent *float64 `json:"monthly_rent" binding:"omitempty,gt=0"`
	// DepositAmount: chỉ cho sửa khi CHƯA thu đồng cọc nào (deposit_paid == 0),
	// tránh làm sai lệch deposit_status đã tính dựa trên số đã thu.
	DepositAmount *float64 `json:"deposit_amount" binding:"omitempty,min=0"`
}

// UpdateContract godoc
// @Summary Cập nhật thông tin hợp đồng
// @Description Sửa ghi chú/giá thuê/tiền cọc thỏa thuận (deposit_amount chỉ sửa được khi chưa thu cọc).
// @Description Để đổi ngày hết hạn, dùng endpoint gia hạn (/extend). Để kết thúc hợp đồng, dùng /checkout.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param request body updateContractRequest true "Dữ liệu cập nhật"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id} [put]
func (h *ContractHandler) UpdateContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req updateContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status.IsClosed() {
		utils.Error(c, http.StatusConflict, "Hợp đồng đã đóng, không thể chỉnh sửa")
		return
	}

	update := bson.M{"updated_at": time.Now()}
	if req.Note != nil {
		update["note"] = *req.Note
	}
	if req.MonthlyRent != nil {
		update["monthly_rent"] = *req.MonthlyRent
	}
	if req.DepositAmount != nil {
		if contract.DepositPaid > 0 {
			utils.Error(c, http.StatusConflict, "Đã thu cọc, không thể sửa deposit_amount (nếu cần đổi, hãy dùng ghi chú và điều chỉnh khi checkout)")
			return
		}
		update["deposit_amount"] = *req.DepositAmount
	}

	res, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if res.MatchedCount == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật hợp đồng thành công", nil)
}

// ===================== Extend (gia hạn) =====================

type extendContractRequest struct {
	NewEndDate     string  `json:"new_end_date" binding:"required"` // RFC3339
	NewMonthlyRent float64 `json:"new_monthly_rent"`                // để trống = giữ nguyên giá cũ
	Note           string  `json:"note"`
}

// ExtendContract godoc
// @Summary Gia hạn hợp đồng
// @Description Manager gia hạn hợp đồng đang active, đẩy end_date ra xa hơn.
// @Description Lịch sử gia hạn được lưu lại trong renewals để tra cứu.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param request body extendContractRequest true "Thông tin gia hạn"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/extend [post]
func (h *ContractHandler) ExtendContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req extendContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	newEndDate, err := time.Parse(time.RFC3339, req.NewEndDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "new_end_date không hợp lệ (định dạng RFC3339)")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể gia hạn hợp đồng đang active")
		return
	}
	if !newEndDate.After(contract.EndDate) {
		utils.Error(c, http.StatusBadRequest, "new_end_date phải sau end_date hiện tại")
		return
	}

	now := time.Now()
	renewal := models.RenewalRecord{
		OldEndDate:     contract.EndDate,
		NewEndDate:     newEndDate,
		OldMonthlyRent: contract.MonthlyRent,
		Note:           req.Note,
		CreatedBy:      mustObjectIDFromCtx(c),
		CreatedAt:      now,
	}

	update := bson.M{"end_date": newEndDate, "updated_at": now}
	if req.NewMonthlyRent > 0 {
		update["monthly_rent"] = req.NewMonthlyRent
		renewal.NewMonthlyRent = req.NewMonthlyRent
	}

	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set":  update,
		"$push": bson.M{"renewals": renewal},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể gia hạn hợp đồng")
		return
	}

	utils.Success(c, http.StatusOK, "Gia hạn hợp đồng thành công", gin.H{
		"contract_id": id,
		"end_date":    newEndDate,
	})
}

// ===================== Thu cọc =====================

type collectDepositRequest struct {
	Amount float64              `json:"amount" binding:"required,gt=0"`
	Method models.PaymentMethod `json:"method" binding:"required"`
	Note   string               `json:"note"`
}

// CollectDeposit godoc
// @Summary Thu tiền cọc
// @Description Manager ghi nhận 1 lần thu cọc (có thể thu nhiều lần cho tới khi đủ deposit_amount).
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param request body collectDepositRequest true "Thông tin thu cọc"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/collect-deposit [post]
func (h *ContractHandler) CollectDeposit(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req collectDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}
	if !req.Method.IsValid() {
		utils.Error(c, http.StatusBadRequest, "method không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	depositTxCol := config.GetCollection("deposit_transactions")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Hợp đồng không còn hiệu lực")
		return
	}

	// Chặn thu cọc vượt quá deposit_amount đã thỏa thuận trong hợp đồng, nhất
	// quán với cách CreatePayment chặn thanh toán vượt quá số tiền còn lại của
	// hóa đơn. Nếu thực sự cần thu thêm cọc (đổi thỏa thuận), manager phải sửa
	// deposit_amount trước (chỉ sửa được khi chưa thu đồng nào - xem UpdateContract).
	if contract.DepositPaid+req.Amount > contract.DepositAmount {
		utils.Error(c, http.StatusBadRequest, "Số tiền thu vượt quá deposit_amount đã thỏa thuận trong hợp đồng")
		return
	}

	now := time.Now()
	managerID := mustObjectIDFromCtx(c)

	tx := models.DepositTransaction{
		ID:          primitive.NewObjectID(),
		ContractID:  contract.ID,
		RoomID:      contract.RoomID,
		Type:        models.DepositTxCollect,
		Amount:      req.Amount,
		Method:      req.Method,
		Note:        req.Note,
		ConfirmedBy: managerID,
		CreatedAt:   now,
	}
	if _, err := depositTxCol.InsertOne(ctx, tx); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận thu cọc")
		return
	}

	newDepositPaid := contract.DepositPaid + req.Amount
	newStatus := resolveDepositStatusAfterCollect(contract.DepositAmount, newDepositPaid)

	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{
			"deposit_paid":   newDepositPaid,
			"deposit_status": newStatus,
			"updated_at":     now,
		},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã ghi nhận giao dịch nhưng cập nhật hợp đồng thất bại, cần kiểm tra lại thủ công")
		return
	}

	utils.Success(c, http.StatusOK, "Thu cọc thành công", gin.H{
		"contract_id":    id,
		"deposit_paid":   newDepositPaid,
		"deposit_status": newStatus,
		"transaction_id": tx.ID,
	})
}

// ===================== Checkout / Chấm dứt hợp đồng =====================

type checkoutContractRequest struct {
	// ActualEndDate: để trống -> dùng thời điểm hiện tại.
	ActualEndDate string `json:"actual_end_date"`

	// RefundAmount: số tiền cọc thực trả lại cho tenant (có thể < số đã thu
	// nếu trừ phí hư hỏng/nợ tiền phòng...). Bắt buộc khai báo rõ ràng,
	// không tự động suy luận, để manager luôn kiểm soát số tiền hoàn.
	RefundAmount float64              `json:"refund_amount" binding:"min=0"`
	RefundMethod models.PaymentMethod `json:"refund_method"`

	// DeductionNote: lý do trừ cọc (nếu refund_amount < số cọc đang giữ).
	DeductionNote string `json:"deduction_note"`

	// Reason: lý do chấm dứt hợp đồng nói chung (hết hạn, vi phạm, thỏa thuận...).
	Reason string `json:"reason"`
}

// CheckoutContract godoc
// @Summary Checkout / kết thúc hợp đồng
// @Description Manager xác nhận tenant trả phòng: đóng hợp đồng (ended nếu tới/qua
// @Description hạn, terminated nếu chấm dứt sớm), hoàn cọc theo refund_amount (có thể
// @Description giữ lại 1 phần), gỡ toàn bộ tenant của hợp đồng khỏi phòng.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param request body checkoutContractRequest true "Thông tin checkout"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/checkout [post]
func (h *ContractHandler) CheckoutContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req checkoutContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}

	actualEndDate := time.Now()
	if req.ActualEndDate != "" {
		parsed, err := time.Parse(time.RFC3339, req.ActualEndDate)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "actual_end_date không hợp lệ (định dạng RFC3339)")
			return
		}
		actualEndDate = parsed
	}

	if req.RefundMethod != "" && !req.RefundMethod.IsValid() {
		utils.Error(c, http.StatusBadRequest, "refund_method không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	depositTxCol := config.GetCollection("deposit_transactions")
	roomsCol := config.GetCollection("rooms")
	usersCol := config.GetCollection("users")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Hợp đồng không còn hiệu lực (đã đóng trước đó)")
		return
	}

	remainingHeld := contract.DepositPaid - contract.DepositRefunded
	if req.RefundAmount > remainingHeld {
		utils.Error(c, http.StatusBadRequest, "refund_amount vượt quá số cọc còn đang giữ")
		return
	}
	if req.RefundAmount < remainingHeld && req.DeductionNote == "" {
		utils.Error(c, http.StatusBadRequest, "Cần nhập deduction_note khi hoàn cọc ít hơn số đang giữ")
		return
	}

	now := time.Now()
	managerID := mustObjectIDFromCtx(c)

	// Ghi nhận giao dịch hoàn cọc (nếu có hoàn) và/hoặc giữ cọc (phần chênh lệch).
	kept := remainingHeld - req.RefundAmount
	if req.RefundAmount > 0 {
		refundTx := models.DepositTransaction{
			ID:          primitive.NewObjectID(),
			ContractID:  contract.ID,
			RoomID:      contract.RoomID,
			Type:        models.DepositTxRefund,
			Amount:      req.RefundAmount,
			Method:      req.RefundMethod,
			Note:        req.DeductionNote,
			ConfirmedBy: managerID,
			CreatedAt:   now,
		}
		if _, err := depositTxCol.InsertOne(ctx, refundTx); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận hoàn cọc")
			return
		}
	}
	if kept > 0 {
		forfeitTx := models.DepositTransaction{
			ID:          primitive.NewObjectID(),
			ContractID:  contract.ID,
			RoomID:      contract.RoomID,
			Type:        models.DepositTxForfeit,
			Amount:      kept,
			Note:        req.DeductionNote,
			ConfirmedBy: managerID,
			CreatedAt:   now,
		}
		if _, err := depositTxCol.InsertOne(ctx, forfeitTx); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận giữ cọc")
			return
		}
	}

	newDepositRefunded := contract.DepositRefunded + req.RefundAmount
	var newDepositStatus models.DepositStatus
	switch {
	case contract.DepositPaid <= 0:
		newDepositStatus = models.DepositStatusUnpaid
	case newDepositRefunded >= contract.DepositPaid:
		newDepositStatus = models.DepositStatusRefunded
	case req.RefundAmount == 0:
		newDepositStatus = models.DepositStatusForfeited
	default:
		newDepositStatus = models.DepositStatusPartialRefunded
	}

	newStatus := models.ContractStatusEnded
	if actualEndDate.Before(contract.EndDate) {
		newStatus = models.ContractStatusTerminated
	}

	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{
			"status":             newStatus,
			"actual_end_date":    actualEndDate,
			"deposit_refunded":   newDepositRefunded,
			"deposit_status":     newDepositStatus,
			"termination_reason": req.Reason,
			"updated_at":         now,
		},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật hợp đồng")
		return
	}

	// Gỡ toàn bộ tenant của hợp đồng khỏi phòng (chỉ những tenant thuộc hợp
	// đồng này - phòng lẽ ra không có tenant nào khác nhờ ràng buộc 1
	// hợp đồng active/phòng, nhưng vẫn lặp theo tenant_ids để chắc chắn).
	for _, tid := range contract.TenantIDs {
		if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tid); err != nil && err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Đã đóng hợp đồng nhưng gỡ tenant khỏi phòng thất bại, cần kiểm tra lại thủ công")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set":   bson.M{"updated_at": now},
			"$unset": bson.M{"room_id": ""},
		}); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Đã đóng hợp đồng nhưng cập nhật tài khoản tenant thất bại, cần kiểm tra lại thủ công")
			return
		}
	}

	utils.Success(c, http.StatusOK, "Checkout hợp đồng thành công", gin.H{
		"contract_id":     id,
		"status":          newStatus,
		"deposit_status":  newDepositStatus,
		"refunded_amount": req.RefundAmount,
		"kept_amount":     kept,
		"actual_end_date": actualEndDate,
	})
}

// ===================== Cancel (hủy trước khi phát sinh cọc) =====================

// CancelContract godoc
// @Summary Hủy hợp đồng (ký nhầm / đổi ý, chưa thu cọc)
// @Description Chỉ hủy được khi hợp đồng đang active VÀ chưa thu đồng cọc nào.
// @Description Nếu đã thu cọc, phải dùng /checkout để hoàn/giữ cọc đúng quy trình.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/cancel [post]
func (h *ContractHandler) CancelContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	roomsCol := config.GetCollection("rooms")
	usersCol := config.GetCollection("users")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể hủy hợp đồng đang active")
		return
	}
	if contract.DepositPaid > 0 {
		utils.Error(c, http.StatusConflict, "Đã thu cọc, không thể hủy trực tiếp; hãy dùng /checkout để hoàn/giữ cọc")
		return
	}

	now := time.Now()
	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{
			"status":          models.ContractStatusCancelled,
			"actual_end_date": now,
			"updated_at":      now,
		},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể hủy hợp đồng")
		return
	}

	for _, tid := range contract.TenantIDs {
		if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tid); err != nil && err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Đã hủy hợp đồng nhưng gỡ tenant khỏi phòng thất bại, cần kiểm tra lại thủ công")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set":   bson.M{"updated_at": now},
			"$unset": bson.M{"room_id": ""},
		}); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Đã hủy hợp đồng nhưng cập nhật tài khoản tenant thất bại, cần kiểm tra lại thủ công")
			return
		}
	}

	utils.Success(c, http.StatusOK, "Hủy hợp đồng thành công", nil)
}

// ===================== Quản lý người ở ghép giữa hợp đồng =====================
//
// Cho phép thêm/gỡ 1 tenant vào/khỏi hợp đồng đang active mà KHÔNG cần
// checkout toàn bộ hợp đồng rồi tạo lại (hữu ích khi phòng ở ghép có người
// chuyển vào/ra giữa chừng). contract.tenant_ids vẫn là nguồn sự thật duy
// nhất; 2 endpoint này tự đồng bộ sang room.tenant_ids và user.room_id.
//
// Lưu ý: không tự động chia/điều chỉnh lại deposit_amount/deposit_paid khi
// đổi người - tiền cọc vẫn tính theo cả hợp đồng như 1 khoản chung. Nếu cần
// điều chỉnh theo số người, quản lý (manager) tự xử lý qua note/collect-deposit.

type addTenantToContractRequest struct {
	TenantID string `json:"tenant_id" binding:"required"`
}

// AddTenantToContract godoc
// @Summary Thêm 1 người ở ghép vào hợp đồng đang active
// @Description Manager thêm 1 tenant có sẵn vào hợp đồng đang active (ở ghép
// @Description giữa chừng), gán luôn tenant đó vào phòng của hợp đồng. Tenant
// @Description phải chưa thuộc phòng nào khác và chưa đứng tên hợp đồng active nào khác.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param request body addTenantToContractRequest true "Tenant cần thêm"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/tenants [post]
func (h *ContractHandler) AddTenantToContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	var req addTenantToContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Dữ liệu đầu vào không hợp lệ")
		return
	}
	tenantID, err := primitive.ObjectIDFromHex(req.TenantID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "tenant_id không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	roomsCol := config.GetCollection("rooms")
	usersCol := config.GetCollection("users")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể thêm người vào hợp đồng đang active")
		return
	}
	if containsObjectID(contract.TenantIDs, tenantID) {
		utils.Error(c, http.StatusConflict, "Người thuê đã đứng tên trong hợp đồng này rồi")
		return
	}

	var tenant models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": tenantID, "role": models.RoleTenant}).Decode(&tenant); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy người thuê")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if tenant.RoomID != nil {
		utils.Error(c, http.StatusConflict, "Người thuê '"+tenant.FullName+"' đang thuộc phòng khác, cần trả phòng trước")
		return
	}
	tenantHasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if tenantHasActive {
		utils.Error(c, http.StatusConflict, "Người thuê đang đứng tên trong 1 hợp đồng hiệu lực khác")
		return
	}

	now := time.Now()
	if _, err := addTenantToRoom(ctx, roomsCol, contract.RoomID, tenantID); err != nil {
		if err == ErrRoomFull {
			utils.Error(c, http.StatusConflict, "Phòng đã đủ số người tối đa (capacity), không thể thêm")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể gán người thuê vào phòng")
		return
	}
	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set": bson.M{"room_id": contract.RoomID, "updated_at": now},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gán phòng nhưng cập nhật tài khoản người thuê thất bại, cần kiểm tra lại thủ công")
		return
	}
	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$addToSet": bson.M{"tenant_ids": tenantID},
		"$set":      bson.M{"updated_at": now},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gán phòng nhưng cập nhật hợp đồng thất bại, cần kiểm tra lại thủ công")
		return
	}

	utils.Success(c, http.StatusOK, "Thêm người ở ghép vào hợp đồng thành công", gin.H{
		"contract_id": id,
		"tenant_id":   tenantID,
		"room_id":     contract.RoomID,
	})
}

// RemoveTenantFromContract godoc
// @Summary Gỡ 1 người ở ghép khỏi hợp đồng đang active
// @Description Manager gỡ 1 tenant khỏi hợp đồng đang active (những tenant
// @Description khác vẫn ở lại, hợp đồng vẫn active). Không cho gỡ nếu đây là
// @Description tenant cuối cùng của hợp đồng - trường hợp đó phải dùng
// @Description /checkout hoặc /cancel để đóng hẳn hợp đồng.
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Param tenantId path string true "Tenant ID cần gỡ"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/tenants/{tenantId} [delete]
func (h *ContractHandler) RemoveTenantFromContract(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}
	tenantID, err := primitive.ObjectIDFromHex(c.Param("tenantId"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "tenantId không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	roomsCol := config.GetCollection("rooms")
	usersCol := config.GetCollection("users")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể gỡ người khỏi hợp đồng đang active")
		return
	}
	if !containsObjectID(contract.TenantIDs, tenantID) {
		utils.Error(c, http.StatusNotFound, "Người thuê không đứng tên trong hợp đồng này")
		return
	}
	if len(contract.TenantIDs) <= 1 {
		utils.Error(c, http.StatusConflict, "Đây là người thuê cuối cùng trong hợp đồng; hãy dùng /checkout hoặc /cancel để đóng hợp đồng thay vì gỡ từng người")
		return
	}

	now := time.Now()
	if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tenantID); err != nil && err != mongo.ErrNoDocuments {
		utils.Error(c, http.StatusInternalServerError, "Không thể gỡ người thuê khỏi phòng")
		return
	}
	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set":   bson.M{"updated_at": now},
		"$unset": bson.M{"room_id": ""},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gỡ khỏi phòng nhưng cập nhật tài khoản người thuê thất bại, cần kiểm tra lại thủ công")
		return
	}
	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$pull": bson.M{"tenant_ids": tenantID},
		"$set":  bson.M{"updated_at": now},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gỡ khỏi phòng nhưng cập nhật hợp đồng thất bại, cần kiểm tra lại thủ công")
		return
	}

	utils.Success(c, http.StatusOK, "Gỡ người ở ghép khỏi hợp đồng thành công", gin.H{
		"contract_id": id,
		"tenant_id":   tenantID,
		"room_id":     contract.RoomID,
	})
}

// ===================== Lịch sử giao dịch cọc =====================

// ListDepositTransactions godoc
// @Summary Lịch sử thu/hoàn/giữ cọc của 1 hợp đồng
// @Tags Contracts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Contract ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/contracts/{id}/deposit-transactions [get]
func (h *ContractHandler) ListDepositTransactions(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "ID không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	if !canAccessContract(c, &contract) {
		utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hợp đồng này")
		return
	}

	depositTxCol := config.GetCollection("deposit_transactions")
	cursor, err := depositTxCol.Find(ctx, bson.M{"contract_id": id}, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi hệ thống")
		return
	}
	defer cursor.Close(ctx)

	txs := make([]models.DepositTransaction, 0)
	if err := cursor.All(ctx, &txs); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}

	utils.Success(c, http.StatusOK, "OK", txs)
}
