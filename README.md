<div align="center">

<img src="https://raw.githubusercontent.com/handao2303/checkpoint-app/main/checkpoint_badge_svg/icons/badge-explorer.svg" width="80" alt="Checkpoint" />

# Checkpoint — Backend (Go)

**API và worker xử lý media cho Checkpoint, mạng xã hội du lịch cho Việt Nam.**
Một nguồn dữ liệu duy nhất cho mọi client: web PWA, React Native và Flutter.

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![chi](https://img.shields.io/badge/chi-router-222?style=for-the-badge)
![PostGIS](https://img.shields.io/badge/Supabase-PostGIS-3FCF8E?style=for-the-badge&logo=supabase&logoColor=white)
![Cloud Run](https://img.shields.io/badge/GCP-Cloud_Run-4285F4?style=for-the-badge&logo=googlecloud&logoColor=white)
<br/>
[![Dùng thử](https://img.shields.io/badge/▶_Dùng_thử-checkpoint--app.space-F26B21?style=for-the-badge)](https://checkpoint-app.space)
[![Tổng quan dự án](https://img.shields.io/badge/Tổng_quan-checkpoint--app-23281F?style=for-the-badge&logo=github)](https://github.com/handao2303/checkpoint-app)

</div>

---

## 🧭 Vai trò trong hệ thống

```mermaid
flowchart LR
    WEB[React PWA] --> API
    RN[React Native] --> API
    FL[Flutter] --> API
    API[⚙️ Go API<br/>repo này] --> DB[(Postgres + PostGIS)]
    API --> R2[(Cloudflare R2)]
    API --> RD[(Upstash Redis)]
    API -- job --> PS[[Pub/Sub]] --> W[🎬 Worker ffmpeg<br/>repo này]
    W --> R2
    API -- verify JWKS --> SB[(Supabase Auth)]
```

Mọi quy tắc nghiệp vụ nằm ở đây: XP được ghi **cùng một giao dịch** với check-in, huy hiệu và con dấu hộ chiếu tính theo luật trong backend, nên ba client luôn ra cùng một kết quả.

---

## ✨ Năng lực chính

| Module | Làm gì |
|---|---|
| `identity` | Đổi token Supabase lấy `app_token` (15 phút) + `refresh_token` (7 ngày, lưu dạng SHA-256, xoay vòng mỗi lần refresh); tài khoản Khách nâng cấp lên Email/Google mà giữ nguyên dữ liệu; hồ sơ, avatar, follow |
| `checkpoint` | Check-in GPS, tải marker theo khung bản đồ (`bbox`), gần tôi, upload ảnh/video qua presigned URL lên R2, lượt xem |
| `journey` | Hành trình: gán check-in, timeline, lộ trình và quãng đường |
| `place` | Tìm kiếm, địa điểm gần (PostGIS `ST_DWithin`), phổ biến, feed khám phá và feed người đang theo dõi |
| `gamification` | XP, level, huy hiệu, hộ chiếu số (mỗi tỉnh thành một con dấu), bảng xếp hạng |
| `reaction` | Thả cảm xúc trên check-in |
| `share` | Trang chia sẻ có ảnh OG 1200×630 cho check-in, hành trình, địa điểm, người dùng |
| `geocode` · `locationiq` | Reverse geocoding có cache + giới hạn 1 req/s; gợi ý địa điểm |
| `gdrive` · `gphotos` · `cloudsync` | Kết nối Google Drive / Google Photos qua OAuth2 |

**66 endpoint** trên 12 module · **35 migration** · **99 test** · rate limit Redis token bucket (fallback in-memory khi Redis lỗi).

---

## 🔌 API

| Nhóm | Endpoint |
|---|---|
| Health | `GET /health` |
| Auth | `POST /auth/exchange` · `/auth/login` · `/auth/refresh` · `/auth/logout` |
| Users | `GET·PATCH /users/me` · `POST /users/me/avatar` · `GET /users/{id}` · `/users/search` · `/users/by-ids` · `POST·DELETE /users/{id}/follow` · followers / following |
| Checkpoints | `GET /checkpoints?bbox=` · `/checkpoints/{id}` · `/checkpoints/nearby` · `/checkpoints/me` · `/checkpoints/by-user` · `POST /checkpoints` · `/upload-slots` · `/{id}/images` · `/{id}/upload-urls` · `/{id}/images/link` · `/{id}/view` · `PATCH·DELETE /checkpoints/{id}` |
| Reactions | `GET /checkpoints/{id}/reactions` · `POST·DELETE /checkpoints/{id}/reaction` |
| Journeys | `POST /journeys` · `GET /journeys/me` · `GET·PATCH·DELETE /journeys/{id}` · `POST /journeys/{id}/checkpoints[/{cpId}]` · `GET /users/{id}/journeys` |
| Places & discover | `GET /places/search` · `/places/nearby` · `/places/popular` · `/places/{id}` · `/places/suggest` · `/discover/feed` · `/discover/feed/following` |
| Gamification | `GET /me/passport` · `/me/badges` · `POST /me/badges/claim-unsent` · `GET /leaderboard` · `/users/{id}/passport` |
| Share | `GET /share/checkpoint/{id}` · `/share/journey/{id}` · `/share/place/{id}` · `/share/user/{id}` |
| Integrations | `/integrations/drive/*` · `/integrations/photos/*` (init · status · callback · disconnect) |

---

## 🚀 Chạy local

```bash
make setup            # tạo .env + cài migrate CLI + go mod tidy
# điền .env: DATABASE_URL (Supabase Session pooler), JWT_SECRET, SUPABASE_URL, R2_*
make migrate-up       # áp dụng migration
make run              # :8080
curl localhost:8080/health
```

`make help` liệt kê mọi target: `test`, `test-cover`, `lint`, `dev` (live reload), `docker-build`, `migrate-create name=...`, `deploy`, `deploy-worker`, `logs`.

### Cấu hình đáng chú ý

```env
# Rate limit — theo IP cho mọi request, thêm giới hạn theo user cho endpoint cần đăng nhập
RATE_LIMIT_ENABLED=true
RATE_LIMIT_REQUESTS_PER_MINUTE=120
RATE_LIMIT_BURST=30
REDIS_URL=rediss://default:password@redis-host:port   # để trống khi local → limiter in-memory

# Reverse geocoding khi check-in không gửi kèm address (cache, timeout, ≤ 1 req/s)
GEOCODER_ENABLED=true
NOMINATIM_BASE_URL=https://nominatim.openstreetmap.org
NOMINATIM_USER_AGENT=checkpoint-app/0.1
```

Vượt hạn mức trả `429` kèm `Retry-After`. Lỗi kết nối Redis lúc chạy không làm API ngừng phục vụ.

---

## 🚢 Deploy

GitHub Actions `deploy.yml`: **Test** (`go vet` + `go test`) → **Migrate** (tùy chọn) → **Deploy** lên Cloud Run, báo từng bước qua Telegram. Worker ffmpeg deploy riêng bằng `make deploy-worker`. Chi tiết: **[DEPLOY.md](DEPLOY.md)**.

---

## 📂 Cấu trúc

```
cmd/
  server/               API: config, wiring, graceful shutdown
  worker/               worker ffmpeg nhận job qua Pub/Sub
  backfill-thumbdims/   job một lần: bù kích thước thumbnail
internal/               12 module nghiệp vụ (handler → service → repository)
pkg/                    authctx · idref · middleware · pubsub · respond · storage (R2) · supabase (JWKS)
migrations/             35 migration up/down
```

<div align="center">
<sub>Một phần của <a href="https://github.com/handao2303/checkpoint-app">Checkpoint</a> · <a href="https://checkpoint-app.space">checkpoint-app.space</a></sub>
</div>
