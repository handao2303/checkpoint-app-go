// Command backfill-thumbdims measures every checkpoint thumbnail that has no
// recorded pixel size and writes it to checkpoint_images.thumb_width/height.
//
// Share pages emit those as og:image:width / og:image:height. Messenger — and
// Facebook on a first, uncached scrape — often render no preview image without
// them, because neither blocks on fetching the image to find out.
//
// Rows uploaded before migration 000034 all start unmeasured, hence this
// one-off pass. It is safe to re-run and safe to interrupt: measured rows drop
// out of the scan, so a later run only retries what failed or arrived since.
//
// Usage:
//
//	go run ./cmd/backfill-thumbdims            # measure everything
//	go run ./cmd/backfill-thumbdims -dry-run   # report how many rows need it
//	go run ./cmd/backfill-thumbdims -limit 500 # stop after 500 rows
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/pkg/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("backfill-thumbdims: %v", err)
	}
}

func run() error {
	var (
		batchSize = flag.Int("batch", 200, "rows to fetch per page")
		limit     = flag.Int("limit", 0, "stop after this many rows (0 = no limit)")
		dryRun    = flag.Bool("dry-run", false, "only report how many rows need measuring")
	)
	flag.Parse()

	_ = godotenv.Load()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("missing required env var: DATABASE_URL")
	}

	// Ctrl-C stops after the row in flight, leaving everything measured so far
	// committed — the next run resumes from there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	r2 := storage.NewR2(storage.R2Config{
		AccountID: os.Getenv("R2_ACCOUNT_ID"),
		AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("R2_ACCESS_KEY_SECRET"),
		Bucket:    os.Getenv("R2_BUCKET_NAME"),
		PublicURL: os.Getenv("R2_PUBLIC_URL"),
	})

	// Only media queries and object storage are used here, so the place/xp/redis
	// dependencies of the repository are not needed.
	repo := checkpoint.NewRepository(pool, os.Getenv("R2_PUBLIC_URL"), nil, nil, nil)

	if *dryRun {
		return reportPending(ctx, repo)
	}

	measurer := checkpoint.NewMeasurer(repo, r2)

	start := time.Now()
	stats, err := measurer.Backfill(ctx, *batchSize, *limit, func(s checkpoint.BackfillStats) {
		log.Printf("progress: scanned=%d measured=%d failed=%d (%s)",
			s.Scanned, s.Measured, s.Failed, time.Since(start).Round(time.Second))
	})

	log.Printf("done: scanned=%d measured=%d failed=%d in %s",
		stats.Scanned, stats.Measured, stats.Failed, time.Since(start).Round(time.Second))

	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		log.Print("interrupted — re-run to continue where this left off")
	}
	if stats.Failed > 0 {
		log.Printf("%d row(s) could not be measured; see the errors above", stats.Failed)
	}
	return nil
}

// reportPending counts the rows a real run would touch, without writing.
func reportPending(ctx context.Context, repo *checkpoint.Repository) error {
	var total int
	var afterID string
	for {
		batch, err := repo.ThumbnailsMissingDimensions(ctx, afterID, 1000)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		total += len(batch)
		afterID = batch[len(batch)-1].ImageID
	}
	log.Printf("dry run: %d thumbnail(s) need measuring", total)
	return nil
}
