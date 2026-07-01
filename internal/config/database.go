package config

import (
	"context"
	"log"
	"time"

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
}

func GetCollection(name string) *mongo.Collection {
	return DB.Collection(name)
}
