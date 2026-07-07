package handlers

import (
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"rental-app/internal/services"
	"rental-app/internal/utils"
)

// folderSanitizer chỉ cho phép chữ, số, gạch dưới, gạch ngang, dấu "/" (để tạo
// subfolder) trong tên folder - tránh truyền chuỗi linh tinh vào request ký với Cloudinary.
var folderSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_/-]`)

type UploadHandler struct {
	cloudinary *services.CloudinaryService
}

func NewUploadHandler(cloudinary *services.CloudinaryService) *UploadHandler {
	return &UploadHandler{cloudinary: cloudinary}
}

// UploadImage godoc
// @Summary Upload 1 ảnh lên Cloudinary
// @Description Upload ảnh bất kỳ (ảnh phòng, ảnh đính kèm...) lên Cloudinary và trả về imageUrl để lưu vào field tương ứng ở nơi khác.
// @Tags Uploads
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param image formData file true "File ảnh (jpeg/png/webp, tối đa 5MB)"
// @Param folder query string false "Thư mục lưu trên Cloudinary, mặc định 'general'"
// @Success 200 {object} map[string]interface{}
// @Router /api/uploads/image [post]
func (h *UploadHandler) UploadImage(c *gin.Context) {
	if h.cloudinary == nil || !h.cloudinary.IsConfigured() {
		utils.Error(c, http.StatusServiceUnavailable, "Chức năng upload ảnh chưa được cấu hình (thiếu CLOUDINARY_API_KEY/CLOUDINARY_API_SECRET)")
		return
	}

	fileHeader, err := c.FormFile("image")
	if err != nil {
		utils.Error(c, http.StatusBadRequest, "Vui lòng chọn file ảnh (field 'image')")
		return
	}

	if fileHeader.Size > maxAvatarSizeBytes {
		utils.Error(c, http.StatusBadRequest, "File ảnh vượt quá dung lượng cho phép (tối đa 5MB)")
		return
	}

	contentType := fileHeader.Header.Get("Content-Type")
	if !allowedAvatarContentTypes[contentType] {
		utils.Error(c, http.StatusBadRequest, "Chỉ hỗ trợ file ảnh JPEG, PNG hoặc WEBP")
		return
	}

	folder := c.DefaultQuery("folder", "general")
	folder = folderSanitizer.ReplaceAllString(folder, "")
	if folder == "" {
		folder = "general"
	}
	if len(folder) > 60 {
		folder = folder[:60]
	}

	file, err := fileHeader.Open()
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Không thể đọc file ảnh")
		return
	}
	defer file.Close()

	result, err := h.cloudinary.UploadImage(file, fileHeader.Filename, "ruma/"+folder)
	if err != nil {
		utils.Error(c, http.StatusInternalServerError, "Upload ảnh thất bại: "+err.Error())
		return
	}

	utils.Success(c, http.StatusOK, "Upload ảnh thành công", gin.H{
		"image_url": result.SecureURL,
		"public_id": result.PublicID,
	})
}
