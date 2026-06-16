# Checkpoint backend — developer & deploy tasks.
# Run `make` or `make help` to see all targets.

# ---- Config (override on the CLI: `make deploy PROJECT_ID=my-proj`) ----
SERVICE     ?= checkpoint-api
WORKER_SERVICE ?= checkpoint-worker
REGION      ?= us-central1
PROJECT_ID  ?= $(shell gcloud config get-value project 2>/dev/null)
BIN         ?= bin/server
PORT        ?= 8080
# Frontend origin(s) allowed by CORS in production (override on `make deploy`).
CORS_ORIGINS ?= http://localhost:5173,https://checkpoint-web.dev-hongquan.workers.dev

# DATABASE_URL pulled from .env (value after the first '='), used by migrate.
DB_URL := $(shell [ -f .env ] && grep -E '^DATABASE_URL=' .env | cut -d '=' -f2- | sed 's/[[:space:]]*\#.*//')

# Config keys read from .env and passed to Cloud Run as plain env vars
# (ENV and CORS_ALLOWED_ORIGINS are set separately by the deploy target).
DEPLOY_KEYS := DATABASE_URL JWT_SECRET JWT_ACCESS_TTL JWT_REFRESH_TTL SUPABASE_URL \
	R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_ACCESS_KEY_SECRET R2_BUCKET_NAME R2_PUBLIC_URL
DEPLOY_KEYS += RATE_LIMIT_ENABLED RATE_LIMIT_REQUESTS_PER_MINUTE RATE_LIMIT_BURST REDIS_URL

# Keys the media worker needs (DB + R2). It does not need JWT/Supabase/Redis/CORS
# — those are API-only. The worker endpoint is secured by Cloud Run IAM.
WORKER_KEYS := DATABASE_URL R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_ACCESS_KEY_SECRET \
	R2_BUCKET_NAME R2_PUBLIC_URL

MIGRATE_VERSION ?= v4.18.1
# Resolve the migrate binary: prefer PATH, fall back to $GOPATH/bin.
MIGRATE := $(shell command -v migrate 2>/dev/null || echo "$(shell go env GOPATH)/bin/migrate")

.DEFAULT_GOAL := help
.PHONY: help setup env tidy fmt vet lint test test-cover run dev build clean \
	docker-build docker-run migrate-install migrate-up migrate-down migrate-create \
	migrate-force gcp-enable deploy deploy-worker secrets-delete logs

## ----------------------------------------------------------------------
## General
## ----------------------------------------------------------------------

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

setup: env tidy migrate-install ## First-time setup: .env, deps, migrate CLI
	@echo "Setup done. Edit .env, then: make migrate-up && make run"

env: ## Create .env from .env.example if missing
	@test -f .env || (cp .env.example .env && echo "created .env — fill in the values")

## ----------------------------------------------------------------------
## Code quality
## ----------------------------------------------------------------------

tidy: ## go mod tidy
	go mod tidy

fmt: ## Format code
	gofmt -w .

vet: ## go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

test: ## Run tests
	go test ./...

test-cover: ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

## ----------------------------------------------------------------------
## Run / build
## ----------------------------------------------------------------------

run: ## Run the server (reads .env)
	go run ./cmd/server

dev: ## Live-reload (needs `air`: go install github.com/air-verse/air@latest)
	air

build: ## Build a static binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/server

clean: ## Remove build artifacts
	rm -rf bin coverage.out

## ----------------------------------------------------------------------
## Docker
## ----------------------------------------------------------------------

docker-build: ## Build the Docker image
	docker build -t $(SERVICE):latest .

docker-run: ## Run the image locally with .env
	docker run --rm -p $(PORT):8080 --env-file .env $(SERVICE):latest

## ----------------------------------------------------------------------
## Database migrations (golang-migrate)
## ----------------------------------------------------------------------

migrate-install: ## Install the migrate CLI
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

migrate-up: ## Apply all up migrations
	$(MIGRATE) -path migrations -database "$(DB_URL)" up

migrate-down: ## Roll back the last migration
	$(MIGRATE) -path migrations -database "$(DB_URL)" down 1

migrate-create: ## Create a migration: make migrate-create name=add_checkpoints
	$(MIGRATE) create -ext sql -dir migrations -seq $(name)

migrate-force: ## Force a dirty migration version: make migrate-force version=1
	$(MIGRATE) -path migrations -database "$(DB_URL)" force $(version)

## ----------------------------------------------------------------------
## Deploy — GCP Cloud Run
## ----------------------------------------------------------------------

gcp-enable: ## Enable required GCP APIs (run once)
	gcloud services enable run.googleapis.com cloudbuild.googleapis.com \
		artifactregistry.googleapis.com --project $(PROJECT_ID)

deploy: ## Build & deploy to Cloud Run (env vars from .env — no Secret Manager)
	@TMP=$$(mktemp); \
	{ \
	  printf 'ENV: "production"\n'; \
	  printf 'CORS_ALLOWED_ORIGINS: "%s"\n' '$(CORS_ORIGINS)'; \
	  for key in $(DEPLOY_KEYS); do \
	    val=$$(grep -E "^$$key=" .env | cut -d '=' -f2- | sed -E 's/[[:space:]]+#.*$$//; s/[[:space:]]+$$//'); \
	    esc=$$(printf '%s' "$$val" | sed -E 's/\\/\\\\/g; s/"/\\"/g'); \
	    printf '%s: "%s"\n' "$$key" "$$esc"; \
	  done; \
	} > $$TMP; \
	gcloud run services update $(SERVICE) --project $(PROJECT_ID) --region $(REGION) \
		--clear-secrets >/dev/null 2>&1 || true; \
	gcloud run deploy $(SERVICE) \
		--project $(PROJECT_ID) --region $(REGION) \
		--source . --allow-unauthenticated --port 8080 \
		--env-vars-file $$TMP; \
	status=$$?; rm -f $$TMP; exit $$status

deploy-worker: ## Build & deploy the media worker to Cloud Run (env vars from .env)
	@TMP=$$(mktemp); \
	cp Dockerfile Dockerfile.api.bak; cp Dockerfile.worker Dockerfile; \
	trap 'mv -f Dockerfile.api.bak Dockerfile 2>/dev/null; rm -f $$TMP' EXIT; \
	{ \
	  for key in $(WORKER_KEYS); do \
	    val=$$(grep -E "^$$key=" .env | cut -d '=' -f2- | sed -E 's/[[:space:]]+#.*$$//; s/[[:space:]]+$$//'); \
	    if [ -n "$$val" ]; then \
	      esc=$$(printf '%s' "$$val" | sed -E 's/\\/\\\\/g; s/"/\\"/g'); \
	      printf '%s: "%s"\n' "$$key" "$$esc"; \
	    fi; \
	  done; \
	} > $$TMP; \
	gcloud run deploy $(WORKER_SERVICE) \
		--project $(PROJECT_ID) --region $(REGION) \
		--source . --no-allow-unauthenticated --port 8080 \
		--cpu 2 --memory 1Gi --timeout 300 --concurrency 4 --min-instances 0 \
		--env-vars-file $$TMP

secrets-delete: ## Delete ALL Secret Manager secrets created earlier (stop incurring cost)
	@for key in DATABASE_URL JWT_SECRET SUPABASE_URL SUPABASE_JWT_SECRET \
		R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_ACCESS_KEY_SECRET R2_BUCKET_NAME R2_PUBLIC_URL; do \
	  if gcloud secrets describe $$key --project $(PROJECT_ID) >/dev/null 2>&1; then \
	    gcloud secrets delete $$key --project $(PROJECT_ID) --quiet && echo "deleted $$key"; \
	  fi; \
	done

logs: ## Tail Cloud Run logs
	gcloud run services logs read $(SERVICE) --project $(PROJECT_ID) --region $(REGION) --limit 50
