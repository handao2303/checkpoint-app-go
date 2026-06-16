# syntax=docker/dockerfile:1

# Stage 1: Build binary của Go
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Stage 2: Runtime (Bắt buộc phải giữ lại)
FROM alpine:3.20

# Vẫn cài ca-certificates để Go gọi được API HTTPS bên ngoài (như gọi sang Pub/Sub, DB...)
# Tạo appuser để đảm bảo bảo mật (không chạy bằng quyền root)
RUN apk add --no-cache ca-certificates \
  && adduser -D -u 10001 appuser

# Copy file binary từ Stage build sang
COPY --from=build /app/server /server

USER appuser
EXPOSE 8080
ENTRYPOINT ["/server"]