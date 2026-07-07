package main

import (
	"context"
	"flag"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"rental-app/internal/config"
	"rental-app/internal/models"
	"rental-app/internal/utils"
)

// managerAvatarURL là avatar mặc định gán cho tài khoản manager khi seed.
const managerAvatarURL = "https://res.cloudinary.com/dxm8oe8w1/image/upload/v1783434258/ruma/general/xigegm6jvfmmsxpv9wif.jpg"

func main() {
	phone := flag.String("phone", "0984739739", "Số điện thoại tài khoản manager mặc định")
	password := flag.String("password", "admin123", "Mật khẩu tài khoản manager mặc định")
	fullName := flag.String("name", "Manager", "Họ tên tài khoản manager mặc định")

	flag.Parse()

	cfg := config.Load()
	config.ConnectMongo(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	usersCol := config.GetCollection("users")
	roomsCol := config.GetCollection("rooms")
	contractsCol := config.GetCollection("contracts")
	depositTxCol := config.GetCollection("deposit_transactions")
	invoicesCol := config.GetCollection("invoices")
	paymentsCol := config.GetCollection("payments")

	managerID := seedManager(ctx, usersCol, *phone, *password, *fullName, managerAvatarURL)

	roomIDs := seedRooms(ctx, roomsCol)
	tenantIDs := seedTenants(ctx, usersCol, roomsCol, roomIDs)

	contracts := seedContracts(ctx, contractsCol, roomsCol, roomIDs, tenantIDs, managerID)
	seedDepositTransactions(ctx, depositTxCol, contracts, managerID)
	seedInvoicesAndPayments(ctx, invoicesCol, paymentsCol, roomsCol, contracts, managerID)
}

// ===================================================================
// Manager
// ===================================================================

// seedManager tạo tài khoản manager mặc định nếu chưa tồn tại, và luôn trả
// về ID của tài khoản manager (mới tạo hoặc đã tồn tại từ trước) để các hàm
// seed khác dùng làm created_by/confirmed_by, đảm bảo dữ liệu liên kết chặt.
// Nếu tài khoản đã tồn tại từ trước, vẫn cập nhật avatarURL (nếu có truyền vào
// và tài khoản đang chưa có avatar) để không phải xóa DB seed lại từ đầu.
func seedManager(ctx context.Context, usersCol *mongo.Collection, phone, password, fullName, avatarURL string) primitive.ObjectID {
	var existing models.User
	err := usersCol.FindOne(ctx, bson.M{"phone": phone}).Decode(&existing)
	if err == nil {
		log.Printf("⚠️  Tài khoản manager với số điện thoại %s đã tồn tại, bỏ qua seed.", phone)
		if avatarURL != "" && existing.AvatarURL == "" {
			if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": existing.ID}, bson.M{
				"$set": bson.M{"avatar_url": avatarURL, "updated_at": time.Now()},
			}); err != nil {
				log.Printf("⚠️  Không thể cập nhật avatar cho manager %s: %v", phone, err)
			} else {
				log.Printf("✅ Đã cập nhật avatar cho manager %s", phone)
			}
		}
		return existing.ID
	}

	hash, err := utils.HashPassword(password)
	if err != nil {
		log.Fatalf("Không thể mã hóa mật khẩu: %v", err)
	}

	now := time.Now()
	manager := models.User{
		ID:           primitive.NewObjectID(),
		FullName:     fullName,
		Phone:        phone,
		PasswordHash: hash,
		Role:         models.RoleManager,
		IsActive:     true,
		AvatarURL:    avatarURL,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := usersCol.InsertOne(ctx, manager); err != nil {
		log.Fatalf("Không thể tạo tài khoản manager: %v", err)
	}

	log.Println("✅ Seed thành công tài khoản manager:")
	log.Printf("   Số điện thoại: %s", phone)
	log.Printf("   Mật khẩu:      %s", password)
	log.Println("   ⚠️  Hãy đổi mật khẩu ngay sau khi đăng nhập lần đầu (PUT /api/auth/change-password).")

	return manager.ID
}

// ===================================================================
// Rooms
// ===================================================================

type roomSeed struct {
	code  string
	floor int
}

func seedRooms(ctx context.Context, roomsCol *mongo.Collection) map[string]primitive.ObjectID {
	rooms := []roomSeed{
		{code: "101", floor: 1},
		{code: "102", floor: 1},
		{code: "201", floor: 2},
		{code: "202", floor: 2},
		{code: "301", floor: 3},
		{code: "302", floor: 3},
	}

	roomIDs := make(map[string]primitive.ObjectID)
	now := time.Now()

	for _, r := range rooms {
		existing := roomsCol.FindOne(ctx, bson.M{"code": r.code})
		var found models.Room
		if err := existing.Decode(&found); err == nil {
			log.Printf("⚠️  Phòng %s đã tồn tại, bỏ qua seed.", r.code)
			roomIDs[r.code] = found.ID
			continue
		}

		room := models.Room{
			ID:                     primitive.NewObjectID(),
			Code:                   r.code,
			Name:                   "Phòng " + r.code,
			Floor:                  r.floor,
			Capacity:               2,
			MonthlyRent:            2500000,
			ElectricPrice:          3800,
			WaterPrice:             25000,
			Occupants:              0,
			ManagementFeePerPerson: 50000,
			Status:                 models.RoomStatusAvailable,
			Note:                   "",
			CreatedAt:              now,
			UpdatedAt:              now,
		}

		if _, err := roomsCol.InsertOne(ctx, room); err != nil {
			log.Fatalf("Không thể tạo phòng %s: %v", r.code, err)
		}

		roomIDs[r.code] = room.ID
		log.Printf("✅ Seed thành công phòng %s", r.code)
	}

	return roomIDs
}

// ===================================================================
// Tenants
// ===================================================================

type tenantSeed struct {
	fullName  string
	phone     string
	password  string
	roomCode  string
	isHead    bool
	email     string
	avatarURL string
}

// tenantSeeds được khai báo ở scope package để seedContracts/seedInvoices...
// có thể tra cứu lại thông tin roomCode/isHead theo phone khi cần.
var tenantSeeds = []tenantSeed{
	{
		fullName:  "Vũ Thảo Minh",
		phone:     "0963797589",
		password:  "minh123",
		roomCode:  "101",
		isHead:    false,
		email:     "vuthaominh14@gmail.com",
		avatarURL: "https://res.cloudinary.com/dxm8oe8w1/image/upload/v1783434003/ruma/general/zjmtytm2mtbqpcekczo0.jpg",
	},
	{
		fullName:  "Tăng Thị Bình",
		phone:     "0983123425",
		password:  "binh123",
		roomCode:  "202",
		isHead:    true,
		email:     "binhtang05@yahoo.com",
		avatarURL: "https://res.cloudinary.com/dxm8oe8w1/image/upload/v1783434155/ruma/general/pmq4lbrddskk5gledqkt.jpg",
	},
	{fullName: "Nguyễn Tăng Tài Phát", phone: "0932000035", password: "phat123", roomCode: "101", isHead: true},
}

// seedTenants tạo các tài khoản tenant và trả về map phone -> ObjectID
// (dù tenant vừa được tạo mới hay đã tồn tại từ trước), để các hàm seed
// contract/invoice/payment phía sau luôn có ID chính xác để liên kết.
func seedTenants(ctx context.Context, usersCol, roomsCol *mongo.Collection, roomIDs map[string]primitive.ObjectID) map[string]primitive.ObjectID {
	tenantIDs := make(map[string]primitive.ObjectID)
	now := time.Now()

	for _, t := range tenantSeeds {
		var existing models.User
		err := usersCol.FindOne(ctx, bson.M{"phone": t.phone}).Decode(&existing)
		if err == nil {
			log.Printf("⚠️  Tài khoản tenant với số điện thoại %s đã tồn tại, bỏ qua seed.", t.phone)
			tenantIDs[t.phone] = existing.ID

			// Vẫn cập nhật email/avatar nếu seed data có mà tài khoản hiện đang chưa có,
			// để không phải xóa DB seed lại từ đầu chỉ vì muốn bổ sung 2 field này.
			set := bson.M{}
			if t.email != "" && existing.Email == "" {
				set["email"] = t.email
			}
			if t.avatarURL != "" && existing.AvatarURL == "" {
				set["avatar_url"] = t.avatarURL
			}
			if len(set) > 0 {
				set["updated_at"] = time.Now()
				if _, err := usersCol.UpdateOne(ctx, bson.M{"_id": existing.ID}, bson.M{"$set": set}); err != nil {
					log.Printf("⚠️  Không thể cập nhật email/avatar cho tenant %s: %v", t.fullName, err)
				} else {
					log.Printf("✅ Đã cập nhật email/avatar cho tenant %s", t.fullName)
				}
			}
			continue
		}

		roomID, ok := roomIDs[t.roomCode]
		if !ok {
			log.Fatalf("Không tìm thấy phòng %s để gán cho tenant %s", t.roomCode, t.fullName)
		}

		hash, err := utils.HashPassword(t.password)
		if err != nil {
			log.Fatalf("Không thể mã hóa mật khẩu cho %s: %v", t.fullName, err)
		}

		user := models.User{
			ID:           primitive.NewObjectID(),
			FullName:     t.fullName,
			Phone:        t.phone,
			Email:        t.email,
			PasswordHash: hash,
			Role:         models.RoleTenant,
			RoomID:       &roomID,
			IsActive:     true,
			AvatarURL:    t.avatarURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		if _, err := usersCol.InsertOne(ctx, user); err != nil {
			log.Fatalf("Không thể tạo tài khoản tenant %s: %v", t.fullName, err)
		}
		tenantIDs[t.phone] = user.ID

		var update bson.M
		if t.isHead {
			update = bson.M{
				"$push": bson.M{
					"tenant_ids": bson.M{"$each": bson.A{user.ID}, "$position": 0},
				},
				"$inc": bson.M{"occupants": 1},
				"$set": bson.M{"status": models.RoomStatusOccupied, "updated_at": now},
			}
		} else {
			update = bson.M{
				"$addToSet": bson.M{"tenant_ids": user.ID},
				"$inc":      bson.M{"occupants": 1},
				"$set":      bson.M{"status": models.RoomStatusOccupied, "updated_at": now},
			}
		}

		if _, err := roomsCol.UpdateOne(ctx, bson.M{"_id": roomID}, update); err != nil {
			log.Fatalf("Không thể cập nhật phòng %s cho tenant %s: %v", t.roomCode, t.fullName, err)
		}

		headNote := ""
		if t.isHead {
			headNote = " (chủ hộ)"
		}
		log.Printf("✅ Seed thành công tenant: %s - %s - phòng %s%s", t.fullName, t.phone, t.roomCode, headNote)
	}

	return tenantIDs
}

// ===================================================================
// Contracts
// ===================================================================

// contractSeedResult gói lại thông tin 1 hợp đồng đã seed (hoặc đã tồn tại
// từ trước), dùng làm input cho bước seed deposit_transactions/invoices/payments
// phía sau, đảm bảo mọi thứ trỏ đúng room_id/contract_id/tenant_ids.
type contractSeedResult struct {
	ID              primitive.ObjectID
	RoomCode        string
	RoomID          primitive.ObjectID
	TenantIDs       []primitive.ObjectID
	HeadTenantID    primitive.ObjectID
	MonthlyRent     float64
	Occupants       int
	DepositAmount   float64
	DepositPaid     float64
	DepositStatus   models.DepositStatus
	StartDate       time.Time
	ElectricPrice   float64
	WaterPrice      float64
	ManagementFeePP float64
}

// firstOfMonthsAgo trả về ngày 01 của tháng cách hiện tại "months" tháng
// (dùng làm ngày bắt đầu hợp đồng cho dữ liệu seed trông tự nhiên, luôn
// tương đối theo thời điểm chạy seed thay vì hard-code ngày cố định).
func firstOfMonthsAgo(months int) time.Time {
	t := time.Now().AddDate(0, -months, 0)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
}

func seedContracts(
	ctx context.Context,
	contractsCol, roomsCol *mongo.Collection,
	roomIDs map[string]primitive.ObjectID,
	tenantIDs map[string]primitive.ObjectID,
	managerID primitive.ObjectID,
) []contractSeedResult {
	type contractSeed struct {
		roomCode        string
		headPhone       string
		memberPhones    []string // các tenant còn lại cùng phòng (không tính head)
		startMonthsAgo  int
		termMonths      int
		monthlyRent     float64
		depositAmount   float64
		depositPaid     float64
		depositStatus   models.DepositStatus
		electricPrice   float64
		waterPrice      float64
		managementFeePP float64
		note            string
	}

	seeds := []contractSeed{
		{
			roomCode:        "101",
			headPhone:       "0932000035",           // Nguyễn Tăng Tài Phát
			memberPhones:    []string{"0963797589"}, // Vũ Thảo Minh
			startMonthsAgo:  3,
			termMonths:      12,
			monthlyRent:     2500000,
			depositAmount:   2500000,
			depositPaid:     2500000, // đã thu đủ cọc
			depositStatus:   models.DepositStatusHeld,
			electricPrice:   3800,
			waterPrice:      25000,
			managementFeePP: 50000,
			note:            "Hợp đồng ở ghép 2 người, đã thu đủ cọc.",
		},
		{
			roomCode:        "202",
			headPhone:       "0983123425", // Tăng Thị Bình
			memberPhones:    nil,
			startMonthsAgo:  2,
			termMonths:      6,
			monthlyRent:     2500000,
			depositAmount:   2500000,
			depositPaid:     1500000, // mới thu 1 phần cọc
			depositStatus:   models.DepositStatusPartial,
			electricPrice:   3800,
			waterPrice:      25000,
			managementFeePP: 50000,
			note:            "Hợp đồng ở 1 người, còn thiếu 1 phần cọc.",
		},
	}

	results := make([]contractSeedResult, 0, len(seeds))
	now := time.Now()

	for _, s := range seeds {
		roomID, ok := roomIDs[s.roomCode]
		if !ok {
			log.Fatalf("Không tìm thấy phòng %s để tạo hợp đồng", s.roomCode)
		}

		headID, ok := tenantIDs[s.headPhone]
		if !ok {
			log.Fatalf("Không tìm thấy tenant %s để làm chủ hộ hợp đồng phòng %s", s.headPhone, s.roomCode)
		}

		tenantObjIDs := []primitive.ObjectID{headID}
		for _, p := range s.memberPhones {
			id, ok := tenantIDs[p]
			if !ok {
				log.Fatalf("Không tìm thấy tenant %s để thêm vào hợp đồng phòng %s", p, s.roomCode)
			}
			tenantObjIDs = append(tenantObjIDs, id)
		}

		// Idempotent: nếu phòng đã có hợp đồng active thì bỏ qua, chỉ nạp lại
		// thông tin cần thiết để các bước seed sau (invoice/payment) vẫn chạy được.
		var existing models.Contract
		err := contractsCol.FindOne(ctx, bson.M{"room_id": roomID, "status": models.ContractStatusActive}).Decode(&existing)
		if err == nil {
			log.Printf("⚠️  Phòng %s đã có hợp đồng active (id=%s), bỏ qua seed hợp đồng.", s.roomCode, existing.ID.Hex())
			results = append(results, contractSeedResult{
				ID:              existing.ID,
				RoomCode:        s.roomCode,
				RoomID:          roomID,
				TenantIDs:       existing.TenantIDs,
				HeadTenantID:    headID,
				MonthlyRent:     existing.MonthlyRent,
				Occupants:       len(existing.TenantIDs),
				DepositAmount:   existing.DepositAmount,
				DepositPaid:     existing.DepositPaid,
				DepositStatus:   existing.DepositStatus,
				StartDate:       existing.StartDate,
				ElectricPrice:   s.electricPrice,
				WaterPrice:      s.waterPrice,
				ManagementFeePP: s.managementFeePP,
			})
			continue
		}

		startDate := firstOfMonthsAgo(s.startMonthsAgo)
		endDate := startDate.AddDate(0, s.termMonths, 0)

		contract := models.Contract{
			ID:              primitive.NewObjectID(),
			RoomID:          roomID,
			RoomCode:        s.roomCode,
			TenantIDs:       tenantObjIDs,
			MonthlyRent:     s.monthlyRent,
			DepositAmount:   s.depositAmount,
			DepositPaid:     s.depositPaid,
			DepositRefunded: 0,
			DepositStatus:   s.depositStatus,
			StartDate:       startDate,
			EndDate:         endDate,
			Status:          models.ContractStatusActive,
			Note:            s.note,
			CreatedBy:       managerID,
			CreatedAt:       startDate,
			UpdatedAt:       now,
		}

		if _, err := contractsCol.InsertOne(ctx, contract); err != nil {
			log.Fatalf("Không thể tạo hợp đồng cho phòng %s: %v", s.roomCode, err)
		}

		log.Printf("✅ Seed thành công hợp đồng phòng %s (id=%s), %d người thuê.", s.roomCode, contract.ID.Hex(), len(tenantObjIDs))

		results = append(results, contractSeedResult{
			ID:              contract.ID,
			RoomCode:        s.roomCode,
			RoomID:          roomID,
			TenantIDs:       tenantObjIDs,
			HeadTenantID:    headID,
			MonthlyRent:     s.monthlyRent,
			Occupants:       len(tenantObjIDs),
			DepositAmount:   s.depositAmount,
			DepositPaid:     s.depositPaid,
			DepositStatus:   s.depositStatus,
			StartDate:       startDate,
			ElectricPrice:   s.electricPrice,
			WaterPrice:      s.waterPrice,
			ManagementFeePP: s.managementFeePP,
		})
	}

	return results
}

// ===================================================================
// Deposit Transactions
// ===================================================================

// seedDepositTransactions ghi nhận giao dịch thu cọc cho từng hợp đồng, số
// tiền khớp chính xác với contract.DepositPaid để dữ liệu đối soát nhất quán
// (tổng deposit_transactions loại "collect" của 1 contract = contract.DepositPaid).
func seedDepositTransactions(ctx context.Context, depositTxCol *mongo.Collection, contracts []contractSeedResult, managerID primitive.ObjectID) {
	for _, c := range contracts {
		if c.DepositPaid <= 0 {
			continue
		}

		count, err := depositTxCol.CountDocuments(ctx, bson.M{"contract_id": c.ID, "type": models.DepositTxCollect})
		if err != nil {
			log.Fatalf("Lỗi kiểm tra deposit_transactions cho hợp đồng phòng %s: %v", c.RoomCode, err)
		}
		if count > 0 {
			log.Printf("⚠️  Hợp đồng phòng %s đã có giao dịch thu cọc, bỏ qua seed.", c.RoomCode)
			continue
		}

		tx := models.DepositTransaction{
			ID:          primitive.NewObjectID(),
			ContractID:  c.ID,
			RoomID:      c.RoomID,
			Type:        models.DepositTxCollect,
			Amount:      c.DepositPaid,
			Method:      models.PaymentMethodCash,
			Note:        "Thu cọc lúc ký hợp đồng",
			ConfirmedBy: managerID,
			CreatedAt:   c.StartDate,
		}

		if _, err := depositTxCol.InsertOne(ctx, tx); err != nil {
			log.Fatalf("Không thể tạo giao dịch thu cọc cho phòng %s: %v", c.RoomCode, err)
		}

		log.Printf("✅ Seed thành công giao dịch thu cọc %.0fđ cho hợp đồng phòng %s.", tx.Amount, c.RoomCode)
	}
}

// ===================================================================
// Invoices + Payments
// ===================================================================

// invoicePlan mô tả 1 hóa đơn cần seed cho 1 hợp đồng: chỉ số điện/nước và
// danh sách các lần thanh toán tương ứng (có thể rỗng = chưa thanh toán,
// 1 phần, hoặc nhiều lần cộng đủ = thanh toán hết).
type invoicePlan struct {
	monthsAgo   int // 0 = tháng hiện tại, 1 = tháng trước, ...
	electricOld float64
	electricNew float64
	waterOld    float64
	waterNew    float64
	payments    []paymentPlan
}

type paymentPlan struct {
	amount float64
	method models.PaymentMethod
	note   string
}

func seedInvoicesAndPayments(
	ctx context.Context,
	invoicesCol, paymentsCol, roomsCol *mongo.Collection,
	contracts []contractSeedResult,
	managerID primitive.ObjectID,
) {
	plans := map[string][]invoicePlan{
		"101": {
			{ // 2 tháng trước: đã thanh toán đủ
				monthsAgo: 2, electricOld: 0, electricNew: 80, waterOld: 0, waterNew: 10,
				payments: []paymentPlan{
					{amount: 3154000, method: models.PaymentMethodCash, note: "Thanh toán đủ tiền phòng tháng"},
				},
			},
			{ // Tháng trước: thanh toán 1 phần
				monthsAgo: 1, electricOld: 80, electricNew: 165, waterOld: 10, waterNew: 21,
				payments: []paymentPlan{
					{amount: 2000000, method: models.PaymentMethodTransfer, note: "Thanh toán 1 phần qua chuyển khoản"},
				},
			},
			{ // Tháng hiện tại: chưa thanh toán
				monthsAgo: 0, electricOld: 165, electricNew: 250, waterOld: 21, waterNew: 32,
				payments: nil,
			},
		},
		"202": {
			{ // Tháng trước: thanh toán đủ, chia 2 lần
				monthsAgo: 1, electricOld: 0, electricNew: 40, waterOld: 0, waterNew: 5,
				payments: []paymentPlan{
					{amount: 1500000, method: models.PaymentMethodCash, note: "Thanh toán lần 1 bằng tiền mặt"},
					{amount: 1327000, method: models.PaymentMethodTransfer, note: "Thanh toán lần 2 (phần còn lại) qua chuyển khoản"},
				},
			},
			{ // Tháng hiện tại: chưa thanh toán
				monthsAgo: 0, electricOld: 40, electricNew: 78, waterOld: 5, waterNew: 9,
				payments: nil,
			},
		},
	}

	for _, c := range contracts {
		roomPlans, ok := plans[c.RoomCode]
		if !ok {
			continue
		}

		for _, p := range roomPlans {
			t := time.Now().AddDate(0, -p.monthsAgo, 0)
			month := int(t.Month())
			year := t.Year()

			// Idempotent: bỏ qua nếu phòng đã có hóa đơn cho đúng tháng/năm này.
			count, err := invoicesCol.CountDocuments(ctx, bson.M{"room_id": c.RoomID, "month": month, "year": year})
			if err != nil {
				log.Fatalf("Lỗi kiểm tra hóa đơn phòng %s tháng %d/%d: %v", c.RoomCode, month, year, err)
			}
			if count > 0 {
				log.Printf("⚠️  Phòng %s đã có hóa đơn tháng %d/%d, bỏ qua seed.", c.RoomCode, month, year)
				continue
			}

			electricAmount := (p.electricNew - p.electricOld) * c.ElectricPrice
			waterAmount := (p.waterNew - p.waterOld) * c.WaterPrice
			managementFeeAmount := float64(c.Occupants) * c.ManagementFeePP
			totalAmount := c.MonthlyRent + electricAmount + waterAmount + managementFeeAmount

			var paidAmount float64
			for _, pay := range p.payments {
				paidAmount += pay.amount
			}

			status := models.InvoiceStatusUnpaid
			switch {
			case paidAmount <= 0:
				status = models.InvoiceStatusUnpaid
			case paidAmount >= totalAmount:
				status = models.InvoiceStatusPaid
			default:
				status = models.InvoiceStatusPartial
			}

			createdAt := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
			dueDate := createdAt.AddDate(0, 0, 9) // hạn thanh toán: ngày 10 hàng tháng

			invoice := models.Invoice{
				ID:                     primitive.NewObjectID(),
				RoomID:                 c.RoomID,
				TenantID:               c.HeadTenantID,
				TenantIDs:              c.TenantIDs,
				Month:                  month,
				Year:                   year,
				RentAmount:             c.MonthlyRent,
				ElectricOld:            p.electricOld,
				ElectricNew:            p.electricNew,
				ElectricPrice:          c.ElectricPrice,
				ElectricAmount:         electricAmount,
				WaterOld:               p.waterOld,
				WaterNew:               p.waterNew,
				WaterPrice:             c.WaterPrice,
				WaterAmount:            waterAmount,
				Occupants:              c.Occupants,
				ManagementFeePerPerson: c.ManagementFeePP,
				ManagementFeeAmount:    managementFeeAmount,
				TotalAmount:            totalAmount,
				PaidAmount:             paidAmount,
				Status:                 status,
				DueDate:                dueDate,
				CreatedAt:              createdAt,
				UpdatedAt:              createdAt,
			}

			if _, err := invoicesCol.InsertOne(ctx, invoice); err != nil {
				log.Fatalf("Không thể tạo hóa đơn phòng %s tháng %d/%d: %v", c.RoomCode, month, year, err)
			}
			log.Printf("✅ Seed thành công hóa đơn phòng %s tháng %d/%d - tổng %.0fđ - trạng thái %s", c.RoomCode, month, year, totalAmount, status)

			paidAt := createdAt.AddDate(0, 0, 2)
			for i, pay := range p.payments {
				payment := models.Payment{
					ID:              primitive.NewObjectID(),
					InvoiceID:       invoice.ID,
					RoomID:          c.RoomID,
					TenantID:        c.HeadTenantID,
					TenantIDs:       c.TenantIDs,
					Amount:          pay.amount,
					Method:          pay.method,
					Note:            pay.note,
					ConfirmedBy:     managerID,
					IsAutoConfirmed: false,
					PaidAt:          paidAt.AddDate(0, 0, i), // các lần thanh toán cách nhau vài ngày
					CreatedAt:       paidAt.AddDate(0, 0, i),
				}

				if _, err := paymentsCol.InsertOne(ctx, payment); err != nil {
					log.Fatalf("Không thể tạo thanh toán cho hóa đơn phòng %s tháng %d/%d: %v", c.RoomCode, month, year, err)
				}
				log.Printf("   ✅ Seed thành công thanh toán %.0fđ (%s) cho hóa đơn phòng %s tháng %d/%d", payment.Amount, payment.Method, c.RoomCode, month, year)
			}
		}
	}
}
