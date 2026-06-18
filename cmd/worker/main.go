// Command worker processes checkpoint media asynchronously. It receives Pub/Sub
// push deliveries, transcodes raw videos to MP4 and generates thumbnails, then
// marks each media row done. Deploy as a separate Cloud Run service (same image,
// container command /worker) behind an authenticated Pub/Sub push subscription.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/pkg/pubsub"
	"github.com/quanluon/checkpoint-app/pkg/respond"
	"github.com/quanluon/checkpoint-app/pkg/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("worker startup: %v", err)
	}
}

func run() error {
	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("missing required env var: DATABASE_URL")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	// Verify DB connectivity in the background so the HTTP server can bind to
	// $PORT immediately — Cloud Run kills the container if it doesn't start
	// listening within the startup timeout. A bad DATABASE_URL surfaces as a
	// logged error and failing jobs, not a startup hang.
	go func() {
		pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			log.Printf("worker: db ping failed (jobs will error until DB is reachable): %v", err)
			return
		}
		log.Print("worker: db connection ok")
	}()

	r2 := storage.NewR2(storage.R2Config{
		AccountID: os.Getenv("R2_ACCOUNT_ID"),
		AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("R2_ACCESS_KEY_SECRET"),
		Bucket:    os.Getenv("R2_BUCKET_NAME"),
		PublicURL: os.Getenv("R2_PUBLIC_URL"),
	})

	// The processor only uses media queries + object storage, so the place/xp/redis
	// dependencies of the repository are not needed here.
	repo := checkpoint.NewRepository(pool, os.Getenv("R2_PUBLIC_URL"), nil, nil, nil)
	processor := checkpoint.NewProcessor(repo, r2)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/internal/process", func(w http.ResponseWriter, r *http.Request) {
		handleProcess(w, r, processor, r2)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Transcoding can take a while; allow a generous write timeout.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("checkpoint media worker listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serverErr:
		return fmt.Errorf("serve: %w", err)
	case <-stop:
		log.Println("worker shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

type mediaCleaner interface {
	Delete(ctx context.Context, key string) error
}

// pushRequest is the Pub/Sub push envelope.
type pushRequest struct {
	Message struct {
		Data      string `json:"data"` // base64-encoded job JSON
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// handleProcess handles a Pub/Sub push delivery. The endpoint is secured by
// Cloud Run IAM (only the Pub/Sub push service account can invoke it), so no
// app-level auth is needed here.
func handleProcess(w http.ResponseWriter, r *http.Request, processor *checkpoint.Processor, cleaner mediaCleaner) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var push pushRequest
	if err := json.Unmarshal(body, &push); err != nil {
		http.Error(w, "invalid push body", http.StatusBadRequest)
		return
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(push.Message.Data))
	if err != nil {
		// Malformed payload will never succeed — ACK so Pub/Sub stops retrying.
		log.Printf("worker: decode message %s: %v", push.Message.MessageID, err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := dispatchJob(r.Context(), raw, push.Message.MessageID, processor, cleaner); err != nil {
		var invalid invalidJobError
		if errors.As(err, &invalid) {
			log.Printf("worker: invalid job %s: %v", push.Message.MessageID, err)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		log.Printf("worker: job %s failed: %v", push.Message.MessageID, err)
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type invalidJobError struct {
	err error
}

func (e invalidJobError) Error() string {
	return e.err.Error()
}

func dispatchJob(ctx context.Context, raw []byte, messageID string, processor *checkpoint.Processor, cleaner mediaCleaner) error {
	var envelope pubsub.JobEnvelope
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Type != "" {
		switch envelope.Type {
		case pubsub.JobTypeProcessCheckpoint:
			if envelope.ProcessCheckpoint == nil || envelope.ProcessCheckpoint.CheckpointID == "" {
				return invalidJobError{err: errors.New("missing checkpoint job")}
			}
			return processCheckpointJob(ctx, messageID, processor, *envelope.ProcessCheckpoint)
		case pubsub.JobTypeMediaCleanup:
			if envelope.MediaCleanup == nil {
				return invalidJobError{err: errors.New("missing media cleanup job")}
			}
			return cleanupMediaJob(ctx, messageID, cleaner, envelope.MediaCleanup.Keys)
		default:
			return invalidJobError{err: fmt.Errorf("unknown job type %q", envelope.Type)}
		}
	}

	var job pubsub.MediaJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return invalidJobError{err: err}
	}
	if job.CheckpointID == "" {
		return invalidJobError{err: errors.New("missing checkpoint id")}
	}
	return processCheckpointJob(ctx, messageID, processor, job)
}

func processCheckpointJob(ctx context.Context, messageID string, processor *checkpoint.Processor, job pubsub.MediaJob) error {
	if processor == nil {
		return errors.New("missing checkpoint processor")
	}
	log.Printf("worker: received job for checkpoint %s (msg %s)", job.CheckpointID, messageID)
	if err := processor.ProcessCheckpoint(ctx, job.CheckpointID); err != nil {
		// Transient — return 5xx so Pub/Sub redelivers (rows still pending, so the
		// retry is idempotent). Persistent failures drain to the dead-letter topic.
		log.Printf("worker: process checkpoint %s: %v", job.CheckpointID, err)
		return err
	}
	log.Printf("worker: done checkpoint %s", job.CheckpointID)
	return nil
}

func cleanupMediaJob(ctx context.Context, messageID string, cleaner mediaCleaner, keys []string) error {
	if cleaner == nil {
		return errors.New("missing media cleaner")
	}
	log.Printf("worker: received media cleanup job with %d keys (msg %s)", len(keys), messageID)
	for _, key := range keys {
		if key == "" || strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
			continue
		}
		if err := cleaner.Delete(ctx, key); err != nil {
			log.Printf("worker: delete media key %q: %v", key, err)
		}
	}
	log.Printf("worker: done media cleanup job (msg %s)", messageID)
	return nil
}
