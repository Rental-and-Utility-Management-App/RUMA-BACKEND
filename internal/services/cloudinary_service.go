package services

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"rental-app/internal/config"
)

// CloudinaryService gọi trực tiếp Cloudinary Upload API qua HTTP (signed request),
// không phụ thuộc vào SDK bên thứ 3 để tránh phải thêm dependency mới vào go.mod.
type CloudinaryService struct {
	cloudName string
	apiKey    string
	apiSecret string
	client    *http.Client
}

func NewCloudinaryService(cfg *config.Config) *CloudinaryService {
	return &CloudinaryService{
		cloudName: cfg.CloudinaryCloudName,
		apiKey:    cfg.CloudinaryAPIKey,
		apiSecret: cfg.CloudinaryAPISecret,
		client:    &http.Client{Timeout: 20 * time.Second},
	}
}

// IsConfigured kiểm tra đã đủ thông tin (cloud_name/api_key/api_secret) để gọi Cloudinary chưa.
func (s *CloudinaryService) IsConfigured() bool {
	return s.cloudName != "" && s.apiKey != "" && s.apiSecret != ""
}

type UploadResult struct {
	SecureURL string `json:"secure_url"`
	PublicID  string `json:"public_id"`
}

// sign tạo chữ ký SHA1 theo đúng thuật toán Cloudinary yêu cầu: sắp xếp params
// theo alphabet (key), nối "key=value" bằng "&", KHÔNG bao gồm file/cloud_name/
// resource_type/api_key, sau đó append api_secret vào cuối rồi hash SHA1.
func (s *CloudinaryService) sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, params[k]))
	}

	toSign := strings.Join(pairs, "&") + s.apiSecret
	h := sha1.Sum([]byte(toSign))
	return hex.EncodeToString(h[:])
}

// UploadAvatar upload file ảnh lên Cloudinary với public_id cố định theo userID
// (dạng "avatars/<userID>") kèm overwrite=true, nhờ vậy lần upload sau tự động
// thay thế ảnh cũ mà không để lại file rác trên Cloudinary.
func (s *CloudinaryService) UploadAvatar(fileReader io.Reader, filename string, userID string) (*UploadResult, error) {
	if !s.IsConfigured() {
		return nil, errors.New("Cloudinary chưa được cấu hình (thiếu CLOUDINARY_API_KEY/CLOUDINARY_API_SECRET)")
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	publicID := "avatars/" + userID

	signParams := map[string]string{
		"folder":    "ruma/avatars",
		"overwrite": "true",
		"public_id": publicID,
		"timestamp": timestamp,
	}
	signature := s.sign(signParams)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fileWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fileWriter, fileReader); err != nil {
		return nil, err
	}

	fields := map[string]string{
		"api_key":   s.apiKey,
		"timestamp": timestamp,
		"folder":    "ruma/avatars",
		"overwrite": "true",
		"public_id": publicID,
		"signature": signature,
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	uploadURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/upload", s.cloudName)
	req, err := http.NewRequest(http.MethodPost, uploadURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var cloudErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &cloudErr)
		if cloudErr.Error.Message != "" {
			return nil, fmt.Errorf("Cloudinary upload thất bại: %s", cloudErr.Error.Message)
		}
		return nil, fmt.Errorf("Cloudinary upload thất bại (status %d)", resp.StatusCode)
	}

	var result UploadResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// UploadImage upload 1 ảnh bất kỳ (không gắn với user cụ thể) vào một folder trên
// Cloudinary, để Cloudinary tự sinh public_id ngẫu nhiên (không overwrite, không
// trùng nhau giữa các lần upload). Dùng cho các trường hợp chung: ảnh phòng, ảnh
// đính kèm hợp đồng, ảnh hóa đơn... chỉ cần lấy imageUrl để lưu vào field tương ứng.
func (s *CloudinaryService) UploadImage(fileReader io.Reader, filename string, folder string) (*UploadResult, error) {
	if !s.IsConfigured() {
		return nil, errors.New("Cloudinary chưa được cấu hình (thiếu CLOUDINARY_API_KEY/CLOUDINARY_API_SECRET)")
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	signParams := map[string]string{
		"folder":    folder,
		"timestamp": timestamp,
	}
	signature := s.sign(signParams)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fileWriter, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fileWriter, fileReader); err != nil {
		return nil, err
	}

	fields := map[string]string{
		"api_key":   s.apiKey,
		"timestamp": timestamp,
		"folder":    folder,
		"signature": signature,
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	uploadURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/upload", s.cloudName)
	req, err := http.NewRequest(http.MethodPost, uploadURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var cloudErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &cloudErr)
		if cloudErr.Error.Message != "" {
			return nil, fmt.Errorf("Cloudinary upload thất bại: %s", cloudErr.Error.Message)
		}
		return nil, fmt.Errorf("Cloudinary upload thất bại (status %d)", resp.StatusCode)
	}

	var result UploadResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// DeleteImage xóa ảnh trên Cloudinary theo public_id (dùng khi user gỡ avatar
// hoàn toàn, không thay bằng ảnh mới). Lỗi ở đây không coi là nghiêm trọng -
// caller có thể log và bỏ qua nếu muốn, vì ảnh mồ côi trên Cloudinary không
// ảnh hưởng tới tính đúng đắn của dữ liệu ứng dụng.
func (s *CloudinaryService) DeleteImage(publicID string) error {
	if !s.IsConfigured() {
		return errors.New("Cloudinary chưa được cấu hình")
	}
	if publicID == "" {
		return nil
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signParams := map[string]string{
		"public_id": publicID,
		"timestamp": timestamp,
	}
	signature := s.sign(signParams)

	form := map[string]string{
		"public_id": publicID,
		"timestamp": timestamp,
		"api_key":   s.apiKey,
		"signature": signature,
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for k, v := range form {
		if err := writer.WriteField(k, v); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}

	destroyURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/destroy", s.cloudName)
	req, err := http.NewRequest(http.MethodPost, destroyURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Cloudinary xóa ảnh thất bại (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
