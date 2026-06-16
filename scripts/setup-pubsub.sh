#!/usr/bin/env bash
#
# Create the async media pipeline's Pub/Sub infrastructure from scratch:
#   - topic + dead-letter topic
#   - push service account (OIDC) allowed to invoke the worker
#   - push subscription → worker /internal/process (with dead-letter)
#   - the IAM grants Pub/Sub needs (token creation, dead-letter publish/subscribe)
#   - lets the API publish (pubsub.publisher on the topic)
#
# Idempotent: safe to re-run. Requires the worker Cloud Run service to exist
# already (deploy it first: `make deploy-worker`).
#
# Usage:
#   ./scripts/setup-pubsub.sh
#   PROJECT_ID=cert-learn REGION=us-central1 ./scripts/setup-pubsub.sh
#   (the worker endpoint is secured by Cloud Run IAM — no shared token needed)

set -euo pipefail

# ── Config (override via env) ─────────────────────────────────────
PROJECT_ID="${PROJECT_ID:-$(gcloud config get-value project 2>/dev/null)}"
REGION="${REGION:-us-central1}"
TOPIC="${TOPIC:-checkpoint-media}"
DLQ_TOPIC="${DLQ_TOPIC:-${TOPIC}-dlq}"
SUBSCRIPTION="${SUBSCRIPTION:-${TOPIC}-push}"
WORKER_SERVICE="${WORKER_SERVICE:-checkpoint-worker}"
API_SERVICE="${API_SERVICE:-checkpoint-api}"
PUSH_SA_NAME="${PUSH_SA_NAME:-pubsub-pusher}"
ACK_DEADLINE="${ACK_DEADLINE:-120}"
MAX_DELIVERY_ATTEMPTS="${MAX_DELIVERY_ATTEMPTS:-5}"

if [[ -z "$PROJECT_ID" ]]; then
  echo "::error:: PROJECT_ID is not set and no gcloud default project is configured." >&2
  exit 1
fi

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
PUSH_SA="${PUSH_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
PUBSUB_SA="service-${PROJECT_NUMBER}@gcp-sa-pubsub.iam.gserviceaccount.com"

# Retry a command a few times — IAM bindings that reference a freshly-created
# service account fail until it propagates (a few seconds).
retry() {
  local n=0 max=8 delay=3
  until "$@"; do
    n=$((n + 1))
    if ((n >= max)); then
      echo "::error:: command failed after $max attempts: $*" >&2
      return 1
    fi
    echo "    retry $n/$max in ${delay}s..." >&2
    sleep "$delay"
  done
}

# Block until a service account is visible to the API (post-create propagation).
wait_for_sa() {
  local sa="$1" n=0 max=20
  until gcloud iam service-accounts describe "$sa" --project "$PROJECT_ID" >/dev/null 2>&1; do
    n=$((n + 1))
    if ((n >= max)); then
      echo "::error:: service account $sa not visible after $((max * 3))s" >&2
      return 1
    fi
    sleep 3
  done
}

echo "Project:        $PROJECT_ID ($PROJECT_NUMBER)"
echo "Region:         $REGION"
echo "Topic:          $TOPIC (dead-letter: $DLQ_TOPIC)"
echo "Subscription:   $SUBSCRIPTION"
echo "Worker service: $WORKER_SERVICE"
echo

# ── 0. Enable APIs + ensure the Pub/Sub service agent exists ──────
echo "==> Enabling APIs"
gcloud services enable pubsub.googleapis.com iamcredentials.googleapis.com \
  --project "$PROJECT_ID"
# Creates the service-...@gcp-sa-pubsub service agent if it doesn't exist yet, so
# the IAM bindings below succeed. Best-effort (needs the gcloud beta component).
gcloud beta services identity create --service=pubsub.googleapis.com \
  --project "$PROJECT_ID" >/dev/null 2>&1 || true

# ── 1. Worker URL (must already be deployed) ──────────────────────
STATUS_URL="$(gcloud run services describe "$WORKER_SERVICE" \
  --project "$PROJECT_ID" --region "$REGION" \
  --format='value(status.url)' 2>/dev/null || true)"
if [[ -z "$STATUS_URL" ]]; then
  echo "::error:: Worker service '$WORKER_SERVICE' not found in $REGION." >&2
  echo "          Deploy it first: make deploy-worker" >&2
  exit 1
fi
# Cloud Run validates the OIDC token audience against the project-number URL form
# (https://SERVICE-PROJECTNUMBER.REGION.run.app), even when status.url reports the
# legacy hash URL (…-<hash>-<regioncode>.a.run.app) — using the hash URL as the
# audience yields 401. Use the project-number form for both endpoint and audience.
WORKER_URL="https://${WORKER_SERVICE}-${PROJECT_NUMBER}.${REGION}.run.app"
PUSH_ENDPOINT="${WORKER_URL}/internal/process"
echo "Worker URL:     $WORKER_URL"
echo

# ── 2. Topics ─────────────────────────────────────────────────────
ensure_topic() {
  local t="$1"
  if gcloud pubsub topics describe "$t" --project "$PROJECT_ID" >/dev/null 2>&1; then
    echo "    topic $t already exists"
  else
    gcloud pubsub topics create "$t" --project "$PROJECT_ID"
  fi
}
echo "==> Topics"
ensure_topic "$TOPIC"
ensure_topic "$DLQ_TOPIC"

# ── 3. Push service account ───────────────────────────────────────
echo "==> Push service account"
if gcloud iam service-accounts describe "$PUSH_SA" --project "$PROJECT_ID" >/dev/null 2>&1; then
  echo "    $PUSH_SA already exists"
else
  gcloud iam service-accounts create "$PUSH_SA_NAME" \
    --project "$PROJECT_ID" --display-name="Pub/Sub push to media worker"
fi

# A freshly-created SA takes a few seconds to propagate before it can be used in
# IAM bindings — wait for it, then retry the bindings to be safe.
wait_for_sa "$PUSH_SA"

# Allow the push SA to invoke the (private) worker.
retry gcloud run services add-iam-policy-binding "$WORKER_SERVICE" \
  --project "$PROJECT_ID" --region "$REGION" \
  --member="serviceAccount:$PUSH_SA" --role="roles/run.invoker" >/dev/null
echo "    granted run.invoker on $WORKER_SERVICE"

# Let the Pub/Sub service agent mint OIDC tokens as the push SA.
retry gcloud iam service-accounts add-iam-policy-binding "$PUSH_SA" \
  --project "$PROJECT_ID" \
  --member="serviceAccount:$PUBSUB_SA" \
  --role="roles/iam.serviceAccountTokenCreator" >/dev/null
echo "    granted serviceAccountTokenCreator to Pub/Sub agent"

# ── 4. Push subscription (with dead-letter) ───────────────────────
echo "==> Subscription"
if gcloud pubsub subscriptions describe "$SUBSCRIPTION" --project "$PROJECT_ID" >/dev/null 2>&1; then
  retry gcloud pubsub subscriptions update "$SUBSCRIPTION" \
    --project "$PROJECT_ID" \
    --push-endpoint="$PUSH_ENDPOINT" \
    --push-auth-service-account="$PUSH_SA" \
    --push-auth-token-audience="$WORKER_URL" \
    --ack-deadline="$ACK_DEADLINE" \
    --dead-letter-topic="$DLQ_TOPIC" \
    --max-delivery-attempts="$MAX_DELIVERY_ATTEMPTS" \
    --expiration-period=never
  echo "    updated $SUBSCRIPTION"
else
  retry gcloud pubsub subscriptions create "$SUBSCRIPTION" \
    --project "$PROJECT_ID" \
    --topic="$TOPIC" \
    --push-endpoint="$PUSH_ENDPOINT" \
    --push-auth-service-account="$PUSH_SA" \
    --push-auth-token-audience="$WORKER_URL" \
    --ack-deadline="$ACK_DEADLINE" \
    --dead-letter-topic="$DLQ_TOPIC" \
    --max-delivery-attempts="$MAX_DELIVERY_ATTEMPTS" \
    --expiration-period=never
fi

# Dead-letter forwarding needs the Pub/Sub agent to publish to the DLQ topic and
# to subscribe (ack) on this subscription.
retry gcloud pubsub topics add-iam-policy-binding "$DLQ_TOPIC" \
  --project "$PROJECT_ID" \
  --member="serviceAccount:$PUBSUB_SA" --role="roles/pubsub.publisher" >/dev/null
retry gcloud pubsub subscriptions add-iam-policy-binding "$SUBSCRIPTION" \
  --project "$PROJECT_ID" \
  --member="serviceAccount:$PUBSUB_SA" --role="roles/pubsub.subscriber" >/dev/null
echo "    granted dead-letter publish/subscribe to Pub/Sub agent"

# ── 5. Let the API publish ────────────────────────────────────────
echo "==> API publish permission"
API_SA="$(gcloud run services describe "$API_SERVICE" \
  --project "$PROJECT_ID" --region "$REGION" \
  --format='value(spec.template.spec.serviceAccountName)' 2>/dev/null || true)"
if [[ -z "$API_SA" ]]; then
  # Cloud Run default runtime SA when none is explicitly set.
  API_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
fi
gcloud pubsub topics add-iam-policy-binding "$TOPIC" \
  --project "$PROJECT_ID" \
  --member="serviceAccount:$API_SA" --role="roles/pubsub.publisher" >/dev/null
echo "    granted pubsub.publisher on $TOPIC to $API_SA"

echo
echo "Done. Set these on the API service ($API_SERVICE) so it publishes:"
echo "    GCP_PROJECT_ID=$PROJECT_ID"
echo "    PUBSUB_TOPIC=$TOPIC"
echo "(GitHub Actions: set repo vars GCP_PROJECT_ID + PUBSUB_TOPIC, then redeploy.)"
