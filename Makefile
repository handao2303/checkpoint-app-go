# Checkpoint backend — developer & deploy tasks.
# Run `make` or `make help` to see all targets.

# ---- Config (override on the CLI: `make deploy PROJECT_ID=my-proj`) ----
SERVICE     ?= checkpoint-api
REGION      ?= asia-southeast1
PROJECT_ID  ?= $(shell gcloud config get-value project 2>/dev/null)
BIN         ?= bin/server
PORT        ?= 8080

# DATABASE_URL pulled from .env (value after the first '='), used by migrate.
DB_URL := $(shell [ -f .env ] && grep -E '^DATABASE_URL=' .env | cut -d '=' -f2- | sed 's/[[:space:]]*\#.*//')

# Secrets that the Cloud Run service reads (mapped to Secret Manager).
SECRET_KEYS := DATABASE_URL JWT_SECRET SUPABASE_URL \
	R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_ACCESS_KEY_SECRET R2_BUCKET_NAME R2_PUBLIC_URL

MIGRATE_VERSION ?= v4.18.1
# Resolve the migrate binary: prefer PATH, fall back to $GOPATH/bin.
MIGRATE := $(shell command -v migrate 2>/dev/null || echo "$(shell go env GOPATH)/bin/migrate")

.DEFAULT_GOAL := help
.PHONY: help setup env tidy fmt vet lint test test-cover run dev build clean \
	docker-build docker-run migrate-install migrate-up migrate-down migrate-create \
	migrate-force gcp-enable gcp-secrets deploy logs

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
		secretmanager.googleapis.com artifactregistry.googleapis.com \
		--project $(PROJECT_ID)

gcp-secrets: ## Push secrets from .env into Secret Manager (+ grant Cloud Run access)
	@PROJECT_NUMBER=$$(gcloud projects describe $(PROJECT_ID) --format='value(projectNumber)'); \
	SA="$$PROJECT_NUMBER-compute@developer.gserviceaccount.com"; \
	for key in $(SECRET_KEYS); do \
	  val=$$(grep -E "^$$key=" .env | cut -d '=' -f2- | sed 's/[[:space:]]*#.*//' | xargs); \
	  if [ -z "$$val" ]; then echo "skip $$key (empty)"; continue; fi; \
	  if gcloud secrets describe $$key --project $(PROJECT_ID) >/dev/null 2>&1; then \
	    printf '%s' "$$val" | gcloud secrets versions add $$key --data-file=- --project $(PROJECT_ID) >/dev/null; \
	    echo "updated $$key"; \
	  else \
	    printf '%s' "$$val" | gcloud secrets create $$key --data-file=- --replication-policy=automatic --project $(PROJECT_ID) >/dev/null; \
	    echo "created $$key"; \
	  fi; \
	  gcloud secrets add-iam-policy-binding $$key --member="serviceAccount:$$SA" \
	    --role=roles/secretmanager.secretAccessor --project $(PROJECT_ID) >/dev/null 2>&1 || true; \
	done

deploy: ## Build & deploy to Cloud Run (uses Secret Manager)
	gcloud run deploy $(SERVICE) \
		--project $(PROJECT_ID) --region $(REGION) \
		--source . --allow-unauthenticated --port 8080 \
		--set-env-vars ENV=production,JWT_ACCESS_TTL=15m,JWT_REFRESH_TTL=168h \
		--set-secrets "$$(echo $(SECRET_KEYS) | tr ' ' '\n' | sed 's/.*/&=&:latest/' | paste -sd, -)"

logs: ## Tail Cloud Run logs
	gcloud run services logs read $(SERVICE) --project $(PROJECT_ID) --region $(REGION) --limit 50
