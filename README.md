# Checkpoint — Backend

Go API cho Checkpoint app. Module `github.com/quanluon/checkpoint-app`.
Stack: Go 1.25 · chi · pgx/Supabase · JWT (verify Supabase qua JWKS/ES256) ·
Cloudflare R2 · GCP Cloud Run.

## Trạng thái

**Phase 1 (Foundation)** — Auth + User Profile chạy thật. Chi tiết & việc còn lại:
[../docs/backend.md](../docs/backend.md) · tổng quan: [../docs/STATUS.md](../docs/STATUS.md).

## Chạy local

```bash
make setup            # tạo .env + cài migrate CLI + go mod tidy
# điền .env: DATABASE_URL (Supabase Session pooler), JWT_SECRET, SUPABASE_URL, R2_*
make migrate-up       # tạo bảng
make run              # :8080
curl localhost:8080/health
```

`make help` để xem tất cả target. Deploy lên Cloud Run: **[DEPLOY.md](DEPLOY.md)**.

## Rate limiting

Backend dùng token bucket in-memory theo từng Cloud Run instance. Mọi request
ngoài `OPTIONS` và `/health` chịu giới hạn theo IP; endpoint cần đăng nhập chịu
thêm giới hạn riêng theo user ID. Khi vượt hạn mức, API trả HTTP `429` cùng
header `Retry-After`.

```env
RATE_LIMIT_ENABLED=true
RATE_LIMIT_REQUESTS_PER_MINUTE=120
RATE_LIMIT_BURST=30
```

Quota không được chia sẻ giữa các Cloud Run instance. Nếu cần giới hạn chính
xác toàn service, thay store in-memory bằng shared store như Redis.

## Reverse geocoding

Khi tạo checkpoint mà request không có `address`, backend tự lấy địa chỉ đầy đủ
từ lat/lng qua OpenStreetMap Nominatim. Client có cache trong memory, timeout,
và giới hạn tối đa 1 request/giây để tuân thủ public API usage policy.

```env
GEOCODER_ENABLED=true
NOMINATIM_BASE_URL=https://nominatim.openstreetmap.org
NOMINATIM_USER_AGENT=checkpoint-app/0.1
```

Map frontend giữ attribution OpenStreetMap/OpenFreeMap. Nếu traffic tăng, đổi
`NOMINATIM_BASE_URL` sang endpoint tương thích tự host hoặc có trả phí.

## API — Phase 1

```
GET    /health
POST   /auth/exchange     supabase_token → app_token + refresh_token
POST   /auth/refresh      refresh_token  → cặp token mới (rotation)
POST   /auth/logout       revoke refresh_token
GET    /users/me          [auth]
PATCH  /users/me          [auth]  display_name / bio
POST   /users/me/avatar   [auth]  multipart → R2
GET    /users/{username}  [auth]
```

## Cấu trúc

```
cmd/server/main.go        config + wiring + graceful shutdown
internal/geocode/         cached + rate-limited Nominatim reverse geocoder
internal/identity/        auth + profile (handler/service/repository/token)
pkg/                      authctx · middleware · respond · storage(R2) · supabase(JWKS)
migrations/               users, refresh_tokens
```
