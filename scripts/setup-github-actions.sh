#!/usr/bin/env bash

set -euo pipefail

REPO="${GITHUB_REPOSITORY:-uitbestteam/checkpoint-app-go}"
ENV_FILE="${ENV_FILE:-.env}"
PROJECT_ID="${PROJECT_ID:-${GCP_PROJECT_ID:-}}"
REGION="${REGION:-us-central1}"
SERVICE="${SERVICE:-checkpoint-api}"
DEPLOY_SA_NAME="${DEPLOY_SA_NAME:-github-backend-deploy}"
POOL_ID="${POOL_ID:-github-actions}"
PROVIDER_ID="${PROVIDER_ID:-github}"

if ! command -v gh >/dev/null 2>&1; then
  echo "Missing gh CLI. Install it with: brew install gh" >&2
  exit 1
fi

if ! command -v gcloud >/dev/null 2>&1; then
  echo "Missing gcloud CLI." >&2
  exit 1
fi

# GitHub sets GITHUB_TOKEN inside Actions. Locally, an inherited value prevents
# `gh auth login` from storing and using the developer's own credentials.
if [[ -n "${GITHUB_TOKEN:-}" && -z "${GITHUB_ACTIONS:-}" ]]; then
  echo "Ignoring inherited GITHUB_TOKEN for local GitHub CLI authentication."
  unset GITHUB_TOKEN
fi

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing environment file: $ENV_FILE" >&2
  exit 1
fi

gh auth status >/dev/null

if [[ -z "$PROJECT_ID" ]]; then
  PROJECT_ID="$(gcloud config get-value project 2>/dev/null)"
fi

if [[ -z "$PROJECT_ID" || "$PROJECT_ID" == "(unset)" ]]; then
  echo "Missing GCP project. Export PROJECT_ID or run gcloud config set project." >&2
  exit 1
fi

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
DEPLOY_SA="${DEPLOY_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
WIF_PROVIDER="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/providers/${PROVIDER_ID}"

dotenv_value() {
  local key="$1"
  awk -v prefix="${key}=" '
    index($0, prefix) == 1 {
      value = substr($0, length(prefix) + 1)
      sub(/[[:space:]]+#.*$/, "", value)
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)

      if ((substr(value, 1, 1) == "\"" && substr(value, length(value), 1) == "\"") ||
          (substr(value, 1, 1) == "\047" && substr(value, length(value), 1) == "\047")) {
        value = substr(value, 2, length(value) - 2)
      }

      print value
      exit
    }
  ' "$ENV_FILE"
}

value_or_default() {
  local key="$1"
  local fallback="$2"
  local value
  value="$(dotenv_value "$key")"
  printf '%s' "${value:-$fallback}"
}

require_value() {
  local name="$1"
  local value="$2"

  if [[ -z "$value" ]]; then
    echo "Missing required value: $name" >&2
    exit 1
  fi
}

set_variable() {
  local name="$1"
  local value="$2"

  if [[ -z "$value" ]]; then
    echo "Skipping empty optional variable: $name"
    return
  fi

  printf '%s' "$value" | gh variable set "$name" --repo "$REPO"
  echo "Set variable: $name"
}

set_secret() {
  local name="$1"
  local value="$2"

  if [[ -z "$value" ]]; then
    echo "Skipping empty optional secret: $name"
    return
  fi

  printf '%s' "$value" | gh secret set "$name" --repo "$REPO"
  echo "Set secret: $name"
}

CORS_ALLOWED_ORIGINS="$(dotenv_value CORS_ALLOWED_ORIGINS)"
JWT_ACCESS_TTL="$(value_or_default JWT_ACCESS_TTL "15m")"
JWT_REFRESH_TTL="$(value_or_default JWT_REFRESH_TTL "720h")"
SUPABASE_URL="$(dotenv_value SUPABASE_URL)"
R2_ACCOUNT_ID="$(dotenv_value R2_ACCOUNT_ID)"
R2_BUCKET_NAME="$(dotenv_value R2_BUCKET_NAME)"
R2_PUBLIC_URL="$(dotenv_value R2_PUBLIC_URL)"
GEOCODER_ENABLED="$(value_or_default GEOCODER_ENABLED "true")"
NOMINATIM_BASE_URL="$(value_or_default NOMINATIM_BASE_URL "https://nominatim.openstreetmap.org")"
NOMINATIM_USER_AGENT="$(value_or_default NOMINATIM_USER_AGENT "checkpoint-app/0.1")"
RATE_LIMIT_ENABLED="$(value_or_default RATE_LIMIT_ENABLED "true")"
RATE_LIMIT_REQUESTS_PER_MINUTE="$(value_or_default RATE_LIMIT_REQUESTS_PER_MINUTE "120")"
RATE_LIMIT_BURST="$(value_or_default RATE_LIMIT_BURST "30")"

DATABASE_URL="$(dotenv_value DATABASE_URL)"
JWT_SECRET="$(dotenv_value JWT_SECRET)"
R2_ACCESS_KEY_ID="$(dotenv_value R2_ACCESS_KEY_ID)"
R2_ACCESS_KEY_SECRET="$(dotenv_value R2_ACCESS_KEY_SECRET)"
REDIS_URL="$(dotenv_value REDIS_URL)"

require_value CORS_ALLOWED_ORIGINS "$CORS_ALLOWED_ORIGINS"
require_value SUPABASE_URL "$SUPABASE_URL"
require_value DATABASE_URL "$DATABASE_URL"
require_value JWT_SECRET "$JWT_SECRET"
require_value REDIS_URL "$REDIS_URL"

echo "Configuring GitHub Actions for $REPO"

set_variable GCP_PROJECT_ID "$PROJECT_ID"
set_variable GCP_REGION "$REGION"
set_variable CLOUD_RUN_SERVICE "$SERVICE"
set_variable GCP_WORKLOAD_IDENTITY_PROVIDER "$WIF_PROVIDER"
set_variable GCP_SERVICE_ACCOUNT "$DEPLOY_SA"
set_variable CORS_ALLOWED_ORIGINS "$CORS_ALLOWED_ORIGINS"
set_variable JWT_ACCESS_TTL "$JWT_ACCESS_TTL"
set_variable JWT_REFRESH_TTL "$JWT_REFRESH_TTL"
set_variable SUPABASE_URL "$SUPABASE_URL"
set_variable R2_ACCOUNT_ID "$R2_ACCOUNT_ID"
set_variable R2_BUCKET_NAME "$R2_BUCKET_NAME"
set_variable R2_PUBLIC_URL "$R2_PUBLIC_URL"
set_variable GEOCODER_ENABLED "$GEOCODER_ENABLED"
set_variable NOMINATIM_BASE_URL "$NOMINATIM_BASE_URL"
set_variable NOMINATIM_USER_AGENT "$NOMINATIM_USER_AGENT"
set_variable RATE_LIMIT_ENABLED "$RATE_LIMIT_ENABLED"
set_variable RATE_LIMIT_REQUESTS_PER_MINUTE "$RATE_LIMIT_REQUESTS_PER_MINUTE"
set_variable RATE_LIMIT_BURST "$RATE_LIMIT_BURST"

set_secret DATABASE_URL "$DATABASE_URL"
set_secret JWT_SECRET "$JWT_SECRET"
set_secret R2_ACCESS_KEY_ID "$R2_ACCESS_KEY_ID"
set_secret R2_ACCESS_KEY_SECRET "$R2_ACCESS_KEY_SECRET"
set_secret REDIS_URL "$REDIS_URL"

echo "GitHub Actions variables and secrets are configured."
