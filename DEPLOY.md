# Deploy guide — Checkpoint backend

How to take the **current** backend (Phase 1: auth + profile) from zero to a live
URL on GCP Cloud Run, for free. Every step has a `make` shortcut.

```
Supabase (Postgres)  ─┐
Cloudflare R2         ─┼─►  .env  ──►  GCP Secret Manager  ──►  Cloud Run
                       │
   migrations  ────────┘
```

---

## 0. Prerequisites

```bash
go version            # 1.25+
gcloud --version      # Google Cloud SDK
make setup            # creates .env, runs go mod tidy, installs the migrate CLI
```

`make setup` creates `.env` from `.env.example`. Fill it in as you go below.

---

## 1. Supabase — Postgres + JWT secret

1. Create a project at https://supabase.com (free tier).
2. **Enable PostGIS:** Dashboard → Database → Extensions → enable `postgis`
   (also enabled by the first migration, but do it here too).
3. **Connection string** (for `DATABASE_URL`): Project → **Connect** → **Session pooler**.
   Copy that URI — it is IPv4-compatible and supports migrations:
   ```
   DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:5432/postgres?sslmode=require
   ```
   > **Do NOT use the Direct connection** (`db.<ref>.supabase.co:5432`) — it is
   > **IPv6-only**, so on IPv4 networks `migrate`/the app fail with
   > `connect: no route to host`. Use the **Session pooler** (`...pooler...:5432`,
   > username `postgres.<ref>`). Avoid the **Transaction pooler** (port `6543`)
   > with pgx — it breaks prepared statements.
4. **JWKS verification (no secret needed):** the backend verifies Supabase tokens
   against the project's **public JWKS** (asymmetric ES256 signing keys), so just
   set `SUPABASE_URL=https://<ref>.supabase.co`. The app reads
   `{SUPABASE_URL}/auth/v1/.well-known/jwks.json` and handles key rotation
   (current / standby / previous keys) automatically. No `SUPABASE_JWT_SECRET` required.
5. **Generate your own** `JWT_SECRET` (≥32 chars), e.g. `openssl rand -base64 48`
   — this signs *our* backend access tokens, separate from Supabase.

Apply the migrations:

```bash
make migrate-up        # creates users + refresh_tokens, enables uuid-ossp/postgis
```

---

## 2. Cloudflare R2 — avatar storage

1. Cloudflare dashboard → R2 → **Create bucket** named `checkpoint-app`.
2. R2 → **Manage API Tokens** → create a token with **Object Read & Write**.
   Fill `R2_ACCESS_KEY_ID`, `R2_ACCESS_KEY_SECRET`.
3. `R2_ACCOUNT_ID` = your Cloudflare account id (R2 overview page).
4. Enable a public URL: bucket → Settings → **Public access** (r2.dev) or a custom
   domain. Put it in `R2_PUBLIC_URL` (e.g. `https://pub-xxxx.r2.dev`).

> R2 is optional to *start* the server — it's only used by `POST /users/me/avatar`.

---

## 3. Run & test locally

```bash
make run                       # starts on :8080, reads .env
curl localhost:8080/health     # {"status":"ok"}
```

If `/health` returns ok, the DB connection works. Try Docker too:

```bash
make docker-build && make docker-run
```

---

## 4. Deploy to GCP Cloud Run

```bash
gcloud auth login
gcloud config set project <YOUR_PROJECT_ID>

make gcp-enable                # enable run, cloudbuild, artifactregistry
make deploy                    # build from Dockerfile + deploy, env vars from .env
```

`make deploy` prints a `https://checkpoint-api-xxxx.run.app` URL. Test it:

```bash
curl https://checkpoint-api-xxxx.run.app/health
make logs                      # tail logs if something is off
```

**Config injection:** `make deploy` reads every value from `.env`
(`DATABASE_URL`, `JWT_SECRET`, `SUPABASE_URL`, `JWT_*_TTL`, `R2_*`), forces
`ENV=production`, and sets `CORS_ALLOWED_ORIGINS` from the `CORS_ORIGINS` make
var. It passes them via `--env-vars-file` (a temp file, auto-removed) so values
with special characters survive. `PORT` is injected by Cloud Run automatically.

> We pass config as **plain Cloud Run env vars, not Secret Manager** — $0 cost
> for a personal project (trade-off: values are visible in the Cloud Run console).
> If you previously created Secret Manager secrets, delete them with
> `make secrets-delete` to stop the per-secret charge.

> Re-deploy after code or `.env` changes: `make deploy`.
> Override the allowed origins: `make deploy CORS_ORIGINS=https://your.app`.

---

## 5. CI/CD (optional)

`.github/workflows/deploy.yml` runs `go vet` + `go test`, then deploys on push to
`main`. Add these repo secrets: `GCP_PROJECT_ID`, `GCP_REGION`, `GCP_SA_KEY`
(service-account JSON with Cloud Run + Artifact Registry access), `CLOUD_RUN_SERVICE`.

---

## Cost

Supabase free, R2 free (10GB), Cloud Run free (2M req/mo) → **$0/month** at personal
scale. Cloud Run scales to zero, so a cold request takes ~1–2s after idle.
