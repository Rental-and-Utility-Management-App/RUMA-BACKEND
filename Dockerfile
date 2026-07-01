# ---- Build stage ----
# go.mod yêu cầu go 1.25.0 (do gin v1.12.0), nên dùng đúng base image này.
FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

# Copy go.mod/go.sum trước để tận dụng cache layer khi chỉ sửa code
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 -> build binary tĩnh, không phụ thuộc glibc, chạy được trên Alpine/scratch
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/server ./cmd/server

# ---- Runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 appuser

WORKDIR /app

COPY --from=builder /app/server .

USER appuser

EXPOSE 8080

# Dùng chính endpoint /healthz của app để Docker biết container còn "khỏe" hay không
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1

ENTRYPOINT ["./server"]