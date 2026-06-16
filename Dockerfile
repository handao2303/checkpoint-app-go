# syntax=docker/dockerfile:1

# Stage 1: Build binary của Go
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server
# Worker xử lý media nền (transcode video → MP4 bằng ffmpeg).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/worker ./cmd/worker

# Stage 2: Runtime (Bắt buộc phải giữ lại)
FROM alpine:3.20

# ca-certificates để Go gọi API HTTPS bên ngoài (Pub/Sub, DB...).
# ffmpeg để worker transcode video sang MP4.
# Tạo appuser để đảm bảo bảo mật (không chạy bằng quyền root).
RUN apk add --no-cache ca-certificates ffmpeg \
  && adduser -D -u 10001 appuser

# Một image chứa cả 2 binary: API (/server, mặc định) và worker (/worker).
# Cloud Run service của worker override container command thành /worker.
COPY --from=build /app/server /server
COPY --from=build /app/worker /worker

USER appuser
EXPOSE 8080
ENTRYPOINT ["/server"]