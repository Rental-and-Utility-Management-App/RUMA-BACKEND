package config

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var Client *mongo.Client
var DB *mongo.Database

func ConnectMongo(cfg *Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(cfg.MongoURI)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		log.Fatalf("Không thể kết nối MongoDB: %v", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("Ping MongoDB thất bại: %v", err)
	}

	Client = client
	DB = client.Database(cfg.MongoDBName)

	log.Println("✅ Đã kết nối MongoDB thành công:", cfg.MongoDBName)

	ensureIndexes(ctx)
}

// Tạo các index cần thiết (unique phone, v.v.)
func ensureIndexes(ctx context.Context) {
	usersCol := DB.Collection("users")
	_, err := usersCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    map[string]interface{}{"phone": 1},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		log.Println("Cảnh báo tạo index users.phone:", err)
	}

	roomsCol := DB.Collection("rooms")
	_, err = roomsCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    map[string]interface{}{"code": 1},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		log.Println("Cảnh báo tạo index rooms.code:", err)
	}

	// Sparse: chỉ áp dụng cho document có field này -> không xung đột với hóa đơn/thanh toán
	// cũ tạo trước khi có tính năng đối soát tự động.
	invoicesCol := DB.Collection("invoices")
	_, err = invoicesCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    map[string]interface{}{"payment_ref_code": 1},
		Options: options.Index().SetUnique(true).SetSparse(true),
	})
	if err != nil {
		log.Println("Cảnh báo tạo index invoices.payment_ref_code:", err)
	}

	paymentsCol := DB.Collection("payments")
	_, err = paymentsCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    map[string]interface{}{"external_transaction_id": 1},
		Options: options.Index().SetUnique(true).SetSparse(true),
	})
	if err != nil {
		log.Println("Cảnh báo tạo index payments.external_transaction_id:", err)
	}

	// Partial unique index: đảm bảo ở tầng DB rằng 1 phòng chỉ có tối đa 1
	// hợp đồng "active" tại 1 thời điểm (chặn race-condition, không chỉ dựa
	// vào check ở tầng handler). Các hợp đồng ended/terminated/cancelled
	// không bị ràng buộc unique này nên 1 phòng vẫn có thể có nhiều hợp
	// đồng lịch sử theo thời gian.
	contractsCol := DB.Collection("contracts")
	_, err = contractsCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: map[string]interface{}{"room_id": 1},
		Options: options.Index().
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"status": "active"}),
	})
	if err != nil {
		log.Println("Cảnh báo tạo index contracts.room_id (partial active):", err)
	}
	_, err = contractsCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: map[string]interface{}{"tenant_ids": 1},
	})
	if err != nil {
		log.Println("Cảnh báo tạo index contracts.tenant_ids:", err)
	}

	depositTxCol := DB.Collection("deposit_transactions")
	_, err = depositTxCol.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: map[string]interface{}{"contract_id": 1},
	})
	if err != nil {
		log.Println("Cảnh báo tạo index deposit_transactions.contract_id:", err)
	}
}

func GetCollection(name string) *mongo.Collection {
	return DB.Collection(name)
}
