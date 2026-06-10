# Deploy guide - Checkpoint backend

The backend runs on GCP Cloud Run, uses Supabase Postgres, and stores uploaded
objects in Cloudflare R2. Local deployments use `make deploy`; production
deployments run through GitHub Actions with keyless Workload Identity Federation
(WIF).

## 0. Prerequisites

```bash
go version
gcloud --version
make setup
```

`make setup` creates `.env` from `.env.example`, tidies Go dependencies, and
installs the migration CLI. Never commit `.env`.

## 1. Supabase Postgres

1. Create a Supabase project.
2. Enable the `postgis` extension.
3. Copy the **Session pooler** connection string into `DATABASE_URL`.
4. Set `SUPABASE_URL` to the project URL.
5. Generate a separate backend `JWT_SECRET` with at least 32 characters.

Use the Session pooler on port `5432`. Do not use the IPv6-only direct
connection or the transaction pooler on port `6543`.

Apply migrations locally:

```bash
make migrate-up
```

## 2. Cloudflare R2

1. Create an R2 bucket.
2. Create an Object Read & Write API token.
3. Configure `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_ACCESS_KEY_SECRET`,
   `R2_BUCKET_NAME`, and `R2_PUBLIC_URL`.

R2 is optional for starting the server, but uploads require it.

## 3. Run locally

```bash
make run
curl localhost:8080/health
```

The health endpoint returns `{"status":"ok"}` when the HTTP server is ready.

## 4. Manual Cloud Run deploy

```bash
gcloud auth login
gcloud config set project <GCP_PROJECT_ID>
make gcp-enable
make deploy
```

`make deploy` builds from source and sends runtime values from local `.env` to
Cloud Run as plain environment variables. It does not use Secret Manager.

## 5. GitHub Actions deploy

`.github/workflows/deploy.yml` runs on pushes to `main` and manual dispatches:

1. Run `go vet` and tests with race detection.
2. Optionally apply pending database migrations when selected in a manual run.
3. Authenticate to GCP through WIF and deploy source to Cloud Run.
4. Call the deployed service's `/health` endpoint.

Production runs are serialized. A newer run waits for the current deployment
instead of cancelling it.

Pushes to `main` skip migrations and deploy automatically after tests pass.
To run migrations, start the workflow manually and select
`Run database migrations before deploying`.

### 5.1 Enable GCP APIs

Set the project once in your terminal:

```bash
export PROJECT_ID="<GCP_PROJECT_ID>"
export PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
export REGION="asia-southeast1"
export SERVICE="checkpoint-api"
export REPOSITORY="uitbestteam/checkpoint-app-go"
export DEPLOY_SA_NAME="github-backend-deploy"
export DEPLOY_SA="${DEPLOY_SA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
export POOL_ID="github-actions"
export PROVIDER_ID="github"

gcloud config set project "$PROJECT_ID"

gcloud services enable \
  run.googleapis.com \
  cloudbuild.googleapis.com \
  artifactregistry.googleapis.com \
  iamcredentials.googleapis.com \
  sts.googleapis.com
```

### 5.2 Create the deploy service account

```bash
gcloud iam service-accounts create "$DEPLOY_SA_NAME" \
  --display-name="GitHub backend deploy"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${DEPLOY_SA}" \
  --role="roles/run.sourceDeveloper"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${DEPLOY_SA}" \
  --role="roles/serviceusage.serviceUsageConsumer"
```

Allow the deploy account to attach the default Cloud Run runtime service
account:

```bash
export RUNTIME_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

gcloud iam service-accounts add-iam-policy-binding "$RUNTIME_SA" \
  --member="serviceAccount:${DEPLOY_SA}" \
  --role="roles/iam.serviceAccountUser"
```

Allow the Cloud Build service account used by source deployments to build Cloud
Run images:

```bash
export BUILD_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${BUILD_SA}" \
  --role="roles/run.builder"
```

### 5.3 Configure GitHub Workload Identity Federation

```bash
gcloud iam workload-identity-pools create "$POOL_ID" \
  --location="global" \
  --display-name="GitHub Actions"

gcloud iam workload-identity-pools providers create-oidc "$PROVIDER_ID" \
  --location="global" \
  --workload-identity-pool="$POOL_ID" \
  --display-name="GitHub Actions" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository == '${REPOSITORY}'"

export WIF_PROVIDER="projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/providers/${PROVIDER_ID}"
export WIF_PRINCIPAL="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL_ID}/attribute.repository/${REPOSITORY}"

gcloud iam service-accounts add-iam-policy-binding "$DEPLOY_SA" \
  --member="$WIF_PRINCIPAL" \
  --role="roles/iam.workloadIdentityUser"
```

IAM and WIF changes can take several minutes to propagate.

### 5.4 Configure GitHub repository variables

Install and authenticate GitHub CLI:

```bash
brew install gh
env -u GITHUB_TOKEN gh auth login
```

After completing the GCP and WIF steps above, create all repository variables
and secrets from local `.env` in one run:

```bash
chmod +x scripts/setup-github-actions.sh
./scripts/setup-github-actions.sh
```

The script uses the current `gcloud` project and these defaults:

- Repository: `uitbestteam/checkpoint-app-go`
- Region: `asia-southeast1`
- Cloud Run service: `checkpoint-api`
- WIF pool: `github-actions`
- WIF provider: `github`
- Deploy service account: `github-backend-deploy`

Override a value when needed:

```bash
PROJECT_ID="my-project" \
REGION="asia-southeast1" \
SERVICE="checkpoint-api" \
./scripts/setup-github-actions.sh
```

The script never prints secret values. Empty optional R2 values are skipped.
When run locally, it ignores an inherited `GITHUB_TOKEN` so GitHub CLI uses the
credentials stored by `gh auth login`.

To configure values manually, open:

`GitHub repository -> Settings -> Secrets and variables -> Actions -> Variables`

Add these repository variables:

| Variable | Suggested value |
| --- | --- |
| `GCP_PROJECT_ID` | GCP project ID |
| `GCP_REGION` | `asia-southeast1` |
| `CLOUD_RUN_SERVICE` | `checkpoint-api` |
| `GCP_WORKLOAD_IDENTITY_PROVIDER` | Value of `$WIF_PROVIDER` |
| `GCP_SERVICE_ACCOUNT` | Value of `$DEPLOY_SA` |
| `CORS_ALLOWED_ORIGINS` | Comma-separated frontend origins |
| `JWT_ACCESS_TTL` | `15m` |
| `JWT_REFRESH_TTL` | `720h` |
| `SUPABASE_URL` | Supabase project URL |
| `R2_ACCOUNT_ID` | Cloudflare account ID |
| `R2_BUCKET_NAME` | R2 bucket name |
| `R2_PUBLIC_URL` | Public R2 base URL |
| `GEOCODER_ENABLED` | `true` |
| `NOMINATIM_BASE_URL` | `https://nominatim.openstreetmap.org` |
| `NOMINATIM_USER_AGENT` | App-identifying user agent |

### 5.5 Configure GitHub repository secrets

The setup script above also creates the repository secrets. To configure them
manually, open:

`GitHub repository -> Settings -> Secrets and variables -> Actions -> Secrets`

Add:

- `DATABASE_URL`
- `JWT_SECRET`
- `R2_ACCESS_KEY_ID`
- `R2_ACCESS_KEY_SECRET`

Do not add `GCP_SA_KEY`; WIF replaces long-lived JSON service account keys.

### 5.6 First deployment

1. Ensure the existing Cloud Run service is public.
2. Push the workflow to `main`.
3. Open `Actions -> Deploy Backend`.
4. Select `Run workflow`.
5. Enable `Run database migrations before deploying` for the first deployment
   or whenever the release includes new migrations.
6. Start the workflow.
7. Confirm `Test`, optional `Migrate database`, `Deploy to Cloud Run`, and
   `Health check` all pass.

For ordinary code-only releases, leave the migration checkbox disabled. The
workflow skips the migration job and proceeds to deploy after tests pass.

For automatic deployments caused by pushes to `main`, the migration job is
always skipped. If a release needs a migration, run the workflow manually with
the checkbox enabled instead of relying only on the push-triggered run.

The workflow intentionally does not pass `--allow-unauthenticated`, so it keeps
the service's current public/private access policy unchanged.

## 6. Operations and cost

View logs:

```bash
make logs
```

At personal-project scale, deployments normally remain within Cloud Build and
Cloud Run free tiers. Artifact Registry storage can grow as old images
accumulate, so configure a cleanup policy if deployments become frequent.

Database migrations run before the new revision deploys. Keep migrations
backward-compatible with the currently running revision. The workflow does not
automatically roll back database migrations when a deploy fails.
