package handlers

import (
	"context"
	"errors"
	"log"
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

// dateOnlyLayout: format ngày-tháng-năm dùng cho các trường ngày tháng nhận
// từ client (start_date, end_date, new_end_date, actual_end_date...). Chỉ
// cần "YYYY-MM-DD", không yêu cầu giờ/phút/giây/timezone như RFC3339.
// Khi parse thành công, giờ được gán mặc định 00:00:00 UTC.
const dateOnlyLayout = "2006-01-02"

// parseDateOnly parse 1 chuỗi ngày dạng "YYYY-MM-DD" thành time.Time (UTC, 00:00:00).
func parseDateOnly(s string) (time.Time, error) {
	return time.Parse(dateOnlyLayout, s)
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

// populateContractTenants gộp toàn bộ tenant_ids từ danh sách hợp đồng, query
// 1 lần duy nhất sang users collection, rồi gán ngược lại Tenants cho từng
// contract (tránh N+1 query khi list nhiều hợp đồng). Nhận slice con trỏ để
// sửa trực tiếp trên các struct gốc, dùng chung được cho cả ListContracts
// (nhiều hợp đồng) lẫn GetContract (1 hợp đồng, truyền []*models.Contract{&contract}).
//
// Lỗi ở đây không nên chặn cả response chính (tenant_ids vẫn còn để FE
// fallback hiển thị), nên các handler gọi hàm này chỉ log/bỏ qua lỗi thay vì
// trả StatusInternalServerError.
func populateContractTenants(ctx context.Context, contracts []*models.Contract) error {
	idSet := map[primitive.ObjectID]bool{}
	for _, ct := range contracts {
		for _, tid := range ct.TenantIDs {
			idSet[tid] = true
		}
	}
	if len(idSet) == 0 {
		return nil
	}

	ids := make([]primitive.ObjectID, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}

	usersCol := config.GetCollection("users")
	cursor, err := usersCol.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)

	var users []models.User
	if err := cursor.All(ctx, &users); err != nil {
		return err
	}

	userMap := make(map[primitive.ObjectID]models.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	for _, ct := range contracts {
		tenants := make([]models.TenantBrief, 0, len(ct.TenantIDs))
		for _, tid := range ct.TenantIDs {
			if u, ok := userMap[tid]; ok {
				tenants = append(tenants, models.TenantBrief{
					ID:       u.ID,
					FullName: u.FullName,
					Phone:    u.Phone,
				})
			}
		}
		ct.Tenants = tenants
	}
	return nil
}

// ===================== Create =====================

type createContractRequest struct {
	RoomID    string   `json:"room_id" binding:"required"`
	TenantIDs []string `json:"tenant_ids" binding:"required,min=1"`
	StartDate string   `json:"start_date" binding:"required"` // "YYYY-MM-DD", vd "2025-07-01"
	EndDate   string   `json:"end_date" binding:"required"`   // "YYYY-MM-DD"

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
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}

	roomID, err := primitive.ObjectIDFromHex(req.RoomID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Phòng bạn chọn không hợp lệ")
		return
	}

	startDate, err := parseDateOnly(req.StartDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Ngày bắt đầu không hợp lệ, vui lòng nhập theo định dạng ngày/tháng/năm")
		return
	}
	endDate, err := parseDateOnly(req.EndDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Ngày kết thúc không hợp lệ, vui lòng nhập theo định dạng ngày/tháng/năm")
		return
	}
	if !endDate.After(startDate) {
		utils.Error(c, http.StatusBadRequest, "Ngày kết thúc phải sau ngày bắt đầu")
		return
	}

	tenantIDs := make([]primitive.ObjectID, 0, len(req.TenantIDs))
	seen := map[primitive.ObjectID]bool{}
	for _, s := range req.TenantIDs {
		tid, err := primitive.ObjectIDFromHex(s)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "Danh sách người thuê có thông tin không hợp lệ")
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
			utils.Error(c, http.StatusNotFound, "Không tìm thấy phòng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}

	// Không cho tạo hợp đồng mới nếu phòng đang có hợp đồng active khác.
	activeCount, err := contractsCol.CountDocuments(ctx, bson.M{"room_id": roomID, "status": models.ContractStatusActive})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if activeCount > 0 {
		utils.Error(c, http.StatusConflict, "Phòng này đang có hợp đồng còn hiệu lực, không thể tạo hợp đồng mới")
		return
	}

	if room.Capacity > 0 && len(tenantIDs) > room.Capacity {
		utils.Error(c, http.StatusConflict, "Số người thuê đã chọn vượt quá sức chứa tối đa của phòng")
		return
	}

	// Validate từng tenant: phải tồn tại, đúng role, đang chưa thuộc phòng nào.
	for _, tid := range tenantIDs {
		var tenant models.User
		if err := usersCol.FindOne(ctx, bson.M{"_id": tid, "role": models.RoleTenant}).Decode(&tenant); err != nil {
			if err == mongo.ErrNoDocuments {
				utils.Error(c, http.StatusNotFound, "Có một người thuê trong danh sách không tồn tại")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
			return
		}
		if tenant.RoomID != nil {
			utils.Error(c, http.StatusConflict, "Người thuê '"+tenant.FullName+"' đang ở phòng khác, cần trả phòng đó trước")
			return
		}
	}

	monthlyRent := req.MonthlyRent
	if monthlyRent <= 0 {
		monthlyRent = room.MonthlyRent
	}

	now := time.Now()
	contractID := primitive.NewObjectID()
	contract := models.Contract{
		ID:              contractID,
		RoomID:          roomID,
		RoomCode:        room.Code,
		TenantIDs:       tenantIDs,
		MonthlyRent:     monthlyRent,
		DepositAmount:   req.DepositAmount,
		DepositPaid:     0,
		DepositRefunded: 0,
		DepositStatus:   models.DepositStatusUnpaid,
		DepositRefCode:  utils.GenerateDepositRefCode(contractID),
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
			utils.Error(c, http.StatusConflict, "Phòng này vừa được tạo hợp đồng bởi người khác, vui lòng thử lại")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể tạo hợp đồng, vui lòng thử lại")
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
				utils.Error(c, http.StatusConflict, "Phòng đã đủ số người tối đa, không thể xếp thêm")
				return
			}
			utils.Error(c, http.StatusInternalServerError, "Không thể xếp người thuê vào phòng, hợp đồng vừa tạo đã được hủy")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set": bson.M{"room_id": roomID, "updated_at": now},
		}); err != nil {
			assigned = append(assigned, tid) // đã addTenantToRoom thành công, cần gỡ khi rollback
			rollback()
			utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật thông tin người thuê, hợp đồng vừa tạo đã được hủy")
			return
		}
		assigned = append(assigned, tid)
	}

	_ = populateContractTenants(ctx, []*models.Contract{&contract})

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
			utils.Error(c, http.StatusBadRequest, "Không xác định được tài khoản của bạn, vui lòng đăng nhập lại")
			return
		}
		filter["tenant_ids"] = userID
	}

	if roomIDStr := c.Query("room_id"); roomIDStr != "" {
		roomID, err := primitive.ObjectIDFromHex(roomIDStr)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "Phòng bạn muốn lọc không hợp lệ")
			return
		}
		filter["room_id"] = roomID
	}

	if statusStr := c.Query("status"); statusStr != "" {
		status := models.ContractStatus(statusStr)
		if !status.IsValid() {
			utils.Error(c, http.StatusBadRequest, "Trạng thái lọc không hợp lệ")
			return
		}
		filter["status"] = status
	}

	// expiring_soon=true: chỉ lấy hợp đồng active có end_date trong vòng 15
	// ngày tới (kể cả đã trễ tới hôm nay), để manager biết sớm mà nhắc gia hạn
	// hoặc chuẩn bị checkout. Không tự động đổi status - hợp đồng vẫn "active"
	// cho tới khi manager thao tác checkout/extend.
	if c.Query("expiring_soon") == "true" {
		filter["status"] = models.ContractStatusActive
		filter["end_date"] = bson.M{"$lte": time.Now().AddDate(0, 0, 15)}
	}

	cursor, err := contractsCol.Find(ctx, filter, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	defer cursor.Close(ctx)

	contracts := make([]models.Contract, 0)
	if err := cursor.All(ctx, &contracts); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tải danh sách hợp đồng, vui lòng thử lại")
		return
	}

	// Populate tên tenant cho toàn bộ danh sách trong 1 lần query users.
	// Build slice con trỏ trỏ vào từng phần tử thật trong `contracts` để
	// populateContractTenants sửa trực tiếp, không cần gán lại thủ công.
	ptrs := make([]*models.Contract, len(contracts))
	for i := range contracts {
		ptrs[i] = &contracts[i]
	}
	_ = populateContractTenants(ctx, ptrs) // lỗi populate không chặn response chính

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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}

	if !canAccessContract(c, &contract) {
		utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hợp đồng này")
		return
	}

	_ = populateContractTenants(ctx, []*models.Contract{&contract})

	utils.Success(c, http.StatusOK, "Lấy thông tin hợp đồng thành công", contract)
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	var req updateContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status.IsClosed() {
		utils.Error(c, http.StatusConflict, "Hợp đồng đã kết thúc nên không thể chỉnh sửa")
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
			utils.Error(c, http.StatusConflict, "Hợp đồng đã thu cọc nên không thể sửa số tiền cọc; nếu cần điều chỉnh, vui lòng ghi chú lại và xử lý khi trả phòng")
			return
		}
		update["deposit_amount"] = *req.DepositAmount
	}

	res, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": update})
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if res.MatchedCount == 0 {
		utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
		return
	}

	utils.Success(c, http.StatusOK, "Cập nhật hợp đồng thành công", nil)
}

// ===================== Extend (gia hạn) =====================

type extendContractRequest struct {
	NewEndDate     string  `json:"new_end_date" binding:"required"` // "YYYY-MM-DD"
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	var req extendContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}

	newEndDate, err := parseDateOnly(req.NewEndDate)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Ngày gia hạn không hợp lệ, vui lòng nhập theo định dạng ngày/tháng/năm")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể gia hạn hợp đồng đang còn hiệu lực")
		return
	}
	if !newEndDate.After(contract.EndDate) {
		utils.Error(c, http.StatusBadRequest, "Ngày gia hạn phải sau ngày kết thúc hiện tại của hợp đồng")
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
		"$set":   update,
		"$push":  bson.M{"renewals": renewal},
		"$unset": bson.M{"expiry_reminder_sent_at": ""},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể gia hạn hợp đồng, vui lòng thử lại")
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	var req collectDepositRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}
	if !req.Method.IsValid() {
		utils.Error(c, http.StatusBadRequest, "Phương thức thanh toán không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	depositTxCol := config.GetCollection("deposit_transactions")

	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Hợp đồng này không còn hiệu lực")
		return
	}

	newDepositPaid, newStatus, txID, err := collectDepositForContract(
		ctx, contractsCol, depositTxCol, contract, req.Amount, req.Method, req.Note, mustObjectIDFromCtx(c), false, "",
	)
	if err != nil {
		if err == ErrDepositExceedsAgreed {
			utils.Error(c, http.StatusBadRequest, "Số tiền thu vượt quá số tiền cọc đã thỏa thuận trong hợp đồng")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận thu cọc, vui lòng thử lại")
		return
	}

	utils.Success(c, http.StatusOK, "Thu cọc thành công", gin.H{
		"contract_id":    id,
		"deposit_paid":   newDepositPaid,
		"deposit_status": newStatus,
		"transaction_id": txID,
	})
}

// ===================== Logic dùng chung: thu cọc =====================

// ErrDepositExceedsAgreed trả về khi số tiền thu cọc (cộng dồn) vượt quá
// deposit_amount đã thỏa thuận trong hợp đồng.
var ErrDepositExceedsAgreed = errors.New("deposit amount exceeds agreed amount")

// collectDepositForContract ghi nhận 1 lần thu cọc cho hợp đồng: tạo
// DepositTransaction + cập nhật deposit_paid/deposit_status của contract.
// Dùng chung cho cả CollectDeposit (manager nhập tay) và SepayWebhook (tự
// động nhận diện giao dịch chuyển khoản cọc qua deposit_ref_code).
//
// isAutoConfirmed=true kèm externalTxnID dùng cho trường hợp webhook tự động
// ghi nhận (confirmedBy sẽ là NilObjectID, note sẽ có tiền tố phù hợp).
func collectDepositForContract(
	ctx context.Context,
	contractsCol, depositTxCol *mongo.Collection,
	contract models.Contract,
	amount float64,
	method models.PaymentMethod,
	note string,
	confirmedBy primitive.ObjectID,
	isAutoConfirmed bool,
	externalTxnID string,
) (newDepositPaid float64, newStatus models.DepositStatus, txID primitive.ObjectID, err error) {
	// Nếu thu vượt quá số cọc thỏa thuận: với thu tay thì chặn hẳn (manager
	// phải sửa deposit_amount trước); với webhook tự động thì VẪN lưu lại giao
	// dịch để không mất dấu vết dòng tiền, nhưng chỉ cộng dồn tối đa tới đúng
	// deposit_amount (phần dư coi như tenant chuyển thừa, manager tự xử lý tay).
	cappedAmount := amount
	if contract.DepositPaid+amount > contract.DepositAmount {
		if !isAutoConfirmed {
			return 0, "", primitive.NilObjectID, ErrDepositExceedsAgreed
		}
		cappedAmount = contract.DepositAmount - contract.DepositPaid
		if cappedAmount < 0 {
			cappedAmount = 0
		}
	}

	now := time.Now()
	tx := models.DepositTransaction{
		ID:                    primitive.NewObjectID(),
		ContractID:            contract.ID,
		RoomID:                contract.RoomID,
		Type:                  models.DepositTxCollect,
		Amount:                amount, // lưu đúng số tiền thực chuyển để không mất dấu vết dòng tiền
		Method:                method,
		Note:                  note,
		ConfirmedBy:           confirmedBy,
		IsAutoConfirmed:       isAutoConfirmed,
		ExternalTransactionID: externalTxnID,
		CreatedAt:             now,
	}
	if _, err := depositTxCol.InsertOne(ctx, tx); err != nil {
		return 0, "", primitive.NilObjectID, err
	}

	newDepositPaid = contract.DepositPaid + cappedAmount
	newStatus = resolveDepositStatusAfterCollect(contract.DepositAmount, newDepositPaid)

	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": contract.ID}, bson.M{
		"$set": bson.M{
			"deposit_paid":   newDepositPaid,
			"deposit_status": newStatus,
			"updated_at":     now,
		},
	}); err != nil {
		return 0, "", primitive.NilObjectID, err
	}

	return newDepositPaid, newStatus, tx.ID, nil
}

// ===================== Checkout / Chấm dứt hợp đồng =====================

type checkoutContractRequest struct {
	// ActualEndDate: để trống -> dùng thời điểm hiện tại. Nếu có, định dạng "YYYY-MM-DD".
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	var req checkoutContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}

	actualEndDate := time.Now()
	if req.ActualEndDate != "" {
		parsed, err := parseDateOnly(req.ActualEndDate)
		if err != nil {
			utils.Error(c, http.StatusBadRequest, "Ngày kết thúc thực tế không hợp lệ, vui lòng nhập theo định dạng ngày/tháng/năm")
			return
		}
		actualEndDate = parsed
	}

	if req.RefundMethod != "" && !req.RefundMethod.IsValid() {
		utils.Error(c, http.StatusBadRequest, "Phương thức hoàn cọc không hợp lệ")
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
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Hợp đồng này đã kết thúc từ trước")
		return
	}

	remainingHeld := contract.DepositPaid - contract.DepositRefunded
	if req.RefundAmount > remainingHeld {
		utils.Error(c, http.StatusBadRequest, "Số tiền hoàn cọc không được vượt quá số tiền cọc hiện đang giữ")
		return
	}
	if req.RefundAmount < remainingHeld && req.DeductionNote == "" {
		utils.Error(c, http.StatusBadRequest, "Vui lòng nhập lý do khi hoàn cọc ít hơn số tiền đang giữ")
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
			utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận hoàn cọc, vui lòng thử lại")
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
			utils.Error(c, http.StatusInternalServerError, "Không thể ghi nhận phần cọc bị giữ lại, vui lòng thử lại")
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
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật hợp đồng, vui lòng thử lại")
		return
	}

	// Gỡ toàn bộ tenant của hợp đồng khỏi phòng (chỉ những tenant thuộc hợp
	// đồng này - phòng lẽ ra không có tenant nào khác nhờ ràng buộc 1
	// hợp đồng active/phòng, nhưng vẫn lặp theo tenant_ids để chắc chắn).
	for _, tid := range contract.TenantIDs {
		if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tid); err != nil && err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Đã đóng hợp đồng nhưng gỡ người thuê khỏi phòng thất bại, cần kiểm tra lại thủ công")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set":   bson.M{"updated_at": now},
			"$unset": bson.M{"room_id": ""},
		}); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Đã đóng hợp đồng nhưng cập nhật thông tin người thuê thất bại, cần kiểm tra lại thủ công")
			return
		}
	}

	utils.Success(c, http.StatusOK, "Kết thúc hợp đồng và trả phòng thành công", gin.H{
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
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
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể hủy hợp đồng đang còn hiệu lực")
		return
	}
	if contract.DepositPaid > 0 {
		utils.Error(c, http.StatusConflict, "Hợp đồng đã thu cọc nên không thể hủy trực tiếp; vui lòng dùng chức năng trả phòng/kết thúc hợp đồng để hoàn hoặc giữ cọc")
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
		utils.Error(c, http.StatusInternalServerError, "Không thể hủy hợp đồng, vui lòng thử lại")
		return
	}

	for _, tid := range contract.TenantIDs {
		if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tid); err != nil && err != mongo.ErrNoDocuments {
			utils.Error(c, http.StatusInternalServerError, "Đã hủy hợp đồng nhưng gỡ người thuê khỏi phòng thất bại, cần kiểm tra lại thủ công")
			return
		}
		if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tid}, bson.M{
			"$set":   bson.M{"updated_at": now},
			"$unset": bson.M{"room_id": ""},
		}); err != nil {
			utils.Error(c, http.StatusInternalServerError, "Đã hủy hợp đồng nhưng cập nhật thông tin người thuê thất bại, cần kiểm tra lại thủ công")
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	var req addTenantToContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.Error(c, http.StatusBadRequest, "Thông tin bạn nhập chưa hợp lệ, vui lòng kiểm tra lại")
		return
	}
	tenantID, err := primitive.ObjectIDFromHex(req.TenantID)
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Người thuê bạn chọn không hợp lệ")
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
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể thêm người vào hợp đồng đang còn hiệu lực")
		return
	}
	if containsObjectID(contract.TenantIDs, tenantID) {
		utils.Error(c, http.StatusConflict, "Người thuê này đã có tên trong hợp đồng rồi")
		return
	}

	var tenant models.User
	if err := usersCol.FindOne(ctx, bson.M{"_id": tenantID, "role": models.RoleTenant}).Decode(&tenant); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy người thuê này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if tenant.RoomID != nil {
		utils.Error(c, http.StatusConflict, "Người thuê '"+tenant.FullName+"' đang ở phòng khác, cần trả phòng đó trước")
		return
	}
	tenantHasActive, err := hasActiveContractForTenant(ctx, contractsCol, tenantID)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if tenantHasActive {
		utils.Error(c, http.StatusConflict, "Người thuê này đang có tên trong 1 hợp đồng khác còn hiệu lực")
		return
	}

	now := time.Now()

	// Thứ tự: cập nhật contract.tenant_ids (nguồn sự thật) TRƯỚC, rồi mới đồng
	// bộ sang room/user; nếu bước đồng bộ lỗi giữa chừng thì rollback lại
	// contract.tenant_ids, tránh để lại trạng thái "hợp đồng có tenant nhưng
	// room/user chưa được gán" hoặc ngược lại.
	if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$addToSet": bson.M{"tenant_ids": tenantID},
		"$set":      bson.M{"updated_at": now},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể thêm người thuê vào hợp đồng, vui lòng thử lại")
		return
	}

	if _, err := addTenantToRoom(ctx, roomsCol, contract.RoomID, tenantID); err != nil {
		// Rollback contract.tenant_ids vì chưa gán được vào phòng thực tế.
		_, _ = contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
			"$pull": bson.M{"tenant_ids": tenantID},
			"$set":  bson.M{"updated_at": now},
		})
		if err == ErrRoomFull {
			utils.Error(c, http.StatusConflict, "Phòng đã đủ số người tối đa, không thể thêm")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Không thể xếp người thuê vào phòng, vui lòng thử lại")
		return
	}
	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set": bson.M{"room_id": contract.RoomID, "updated_at": now},
	}); err != nil {
		// Rollback cả contract.tenant_ids lẫn room.tenant_ids vì chưa đồng bộ được sang user.
		_, _ = contractsCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
			"$pull": bson.M{"tenant_ids": tenantID},
			"$set":  bson.M{"updated_at": now},
		})
		_, _ = removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tenantID)
		utils.Error(c, http.StatusInternalServerError, "Không thể cập nhật thông tin người thuê, thao tác thêm người đã được hoàn tác")
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}
	tenantID, err := primitive.ObjectIDFromHex(c.Param("tenantId"))
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Người thuê bạn chọn không hợp lệ")
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
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if contract.Status != models.ContractStatusActive {
		utils.Error(c, http.StatusConflict, "Chỉ có thể gỡ người khỏi hợp đồng đang còn hiệu lực")
		return
	}
	if !containsObjectID(contract.TenantIDs, tenantID) {
		utils.Error(c, http.StatusNotFound, "Người thuê này không có tên trong hợp đồng")
		return
	}
	if len(contract.TenantIDs) <= 1 {
		utils.Error(c, http.StatusConflict, "Đây là người thuê cuối cùng trong hợp đồng, vui lòng dùng chức năng kết thúc hoặc hủy hợp đồng thay vì gỡ từng người")
		return
	}

	now := time.Now()
	if _, err := removeTenantFromRoom(ctx, roomsCol, contract.RoomID, tenantID); err != nil && err != mongo.ErrNoDocuments {
		utils.Error(c, http.StatusInternalServerError, "Không thể gỡ người thuê khỏi phòng, vui lòng thử lại")
		return
	}
	if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": tenantID}, bson.M{
		"$set":   bson.M{"updated_at": now},
		"$unset": bson.M{"room_id": ""},
	}); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã gỡ khỏi phòng nhưng cập nhật thông tin người thuê thất bại, cần kiểm tra lại thủ công")
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
		utils.Error(c, http.StatusBadRequest, "Hợp đồng không hợp lệ")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contractsCol := config.GetCollection("contracts")
	var contract models.Contract
	if err := contractsCol.FindOne(ctx, bson.M{"_id": id}).Decode(&contract); err != nil {
		if err == mongo.ErrNoDocuments {
			utils.Error(c, http.StatusNotFound, "Không tìm thấy hợp đồng này")
			return
		}
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	if !canAccessContract(c, &contract) {
		utils.Error(c, http.StatusForbidden, "Bạn không có quyền xem hợp đồng này")
		return
	}

	depositTxCol := config.GetCollection("deposit_transactions")
	cursor, err := depositTxCol.Find(ctx, bson.M{"contract_id": id}, options_findSortByCreatedDesc())
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Đã có lỗi xảy ra, vui lòng thử lại sau")
		return
	}
	defer cursor.Close(ctx)

	txs := make([]models.DepositTransaction, 0)
	if err := cursor.All(ctx, &txs); err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể tải lịch sử giao dịch cọc, vui lòng thử lại")
		return
	}

	utils.Success(c, http.StatusOK, "Lấy lịch sử giao dịch cọc thành công", txs)
}

// ===================== Cron: nhắc hợp đồng sắp hết hạn =====================

// RunContractExpiryCheck quét toàn bộ hợp đồng "active" có end_date còn trong
// khoảng 7-15 ngày tới, đánh dấu expiry_reminder_sent_at (chỉ nhắc 1 lần cho
// mỗi hợp đồng trong cửa sổ này, tránh log/nhắc lặp lại mỗi ngày).
// Được gọi bởi internal/scheduler (cron hàng ngày) hoặc endpoint chạy tay
// /api/system/run-daily-jobs.
//
// Hiện tại hệ thống CHƯA có kênh gửi email/SMS, nên bước "nhắc" ở đây là log
// lại + đánh dấu để FE có thể query (GET /api/contracts?expiring_soon=true)
// hiển thị badge nhắc nhở cho manager. Khi có kênh thông báo, chỉ cần cắm
// thêm bước gửi vào ngay dưới log.Printf.
func RunContractExpiryCheck(ctx context.Context) (reminded int, err error) {
	contractsCol := config.GetCollection("contracts")

	now := time.Now()
	windowStart := now.AddDate(0, 0, 7)
	windowEnd := now.AddDate(0, 0, 15)

	cursor, err := contractsCol.Find(ctx, bson.M{
		"status": models.ContractStatusActive,
		"end_date": bson.M{
			"$gte": windowStart,
			"$lte": windowEnd,
		},
		"expiry_reminder_sent_at": bson.M{"$exists": false},
	})
	if err != nil {
		return 0, err
	}
	defer cursor.Close(ctx)

	var contracts []models.Contract
	if err := cursor.All(ctx, &contracts); err != nil {
		return 0, err
	}

	for _, ct := range contracts {
		log.Printf("⏰ Hợp đồng %s (phòng %s) sắp hết hạn vào %s - cần nhắc gia hạn/checkout\n",
			ct.ID.Hex(), ct.RoomCode, ct.EndDate.Format("2006-01-02"))

		if _, err := contractsCol.UpdateOne(ctx, bson.M{"_id": ct.ID}, bson.M{
			"$set": bson.M{"expiry_reminder_sent_at": now},
		}); err != nil {
			log.Printf("❌ Không thể đánh dấu đã nhắc hợp đồng %s: %v\n", ct.ID.Hex(), err)
			continue
		}
		reminded++
	}

	return reminded, nil
}
