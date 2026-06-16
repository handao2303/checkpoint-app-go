# syntax=docker/dockerfile:1

# Stage 1: Build the API binary.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Stage 2: Runtime (API only — no ffmpeg; that lives in Dockerfile.worker).
FROM alpine:3.20

# ca-certificates để Go gọi API HTTPS bên ngoài (Pub/Sub, DB...).
# Tạo appuser để không chạy bằng quyền root.
RUN apk add --no-cache ca-certificates \
  && adduser -D -u 10001 appuser

COPY --from=build /app/server /server

USER appuser
EXPOSE 8080
ENTRYPOINT ["/server"]
