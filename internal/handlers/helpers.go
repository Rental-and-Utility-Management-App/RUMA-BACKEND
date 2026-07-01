package handlers

import "go.mongodb.org/mongo-driver/mongo/options"

// options_findSortByCreatedDesc trả về option sắp xếp mới nhất trước
func options_findSortByCreatedDesc() *options.FindOptions {
	return options.Find().SetSort(map[string]interface{}{"created_at": -1})
}
