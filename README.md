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
internal/identity/        auth + profile (handler/service/repository/token)
pkg/                      authctx · middleware · respond · storage(R2) · supabase(JWKS)
migrations/               users, refresh_tokens
```
