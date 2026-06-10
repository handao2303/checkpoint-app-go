// Command server is the Checkpoint App HTTP backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/internal/geocode"
	"github.com/quanluon/checkpoint-app/internal/identity"
	"github.com/quanluon/checkpoint-app/internal/place"
	appmw "github.com/quanluon/checkpoint-app/pkg/middleware"
	"github.com/quanluon/checkpoint-app/pkg/respond"
	"github.com/quanluon/checkpoint-app/pkg/storage"
	"github.com/quanluon/checkpoint-app/pkg/supabase"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("startup: %v", err)
	}
}

func run() error {
	_ = godotenv.Load()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	// Wire the identity domain.
	tokens := identity.NewTokenManager(cfg.jwtSecret, cfg.accessTTL, cfg.refreshTTL)
	verifier, err := supabase.NewVerifier(ctx, supabase.JWKSURL(cfg.supabaseURL))
	if err != nil {
		return fmt.Errorf("supabase verifier: %w", err)
	}
	r2 := storage.NewR2(cfg.r2)
	identitySvc := identity.NewService(identity.NewRepository(pool, cfg.r2.PublicURL), tokens, verifier, r2)
	identityHandler := identity.NewHandler(identitySvc)

	geocoder := geocode.NewClient(cfg.geocoder)
	placeRepo := place.NewRepository(pool, cfg.r2.PublicURL)
	placeHandler := place.NewHandler(place.NewService(placeRepo))
	checkpointHandler := checkpoint.NewHandler(
		checkpoint.NewService(checkpoint.NewRepository(pool, cfg.r2.PublicURL, placeRepo), r2, geocoder))

	r := chi.NewRouter()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: cfg.corsOrigins,
		AllowedMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		MaxAge:         300,
	}))
	r.Use(chimw.RequestID, chimw.Logger, chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	identityHandler.Register(r, appmw.Auth(tokens))
	checkpointHandler.Register(r, appmw.Auth(tokens))
	placeHandler.Register(r, appmw.Auth(tokens))

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("checkpoint server listening on :%s (env=%s)", cfg.port, cfg.env)
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
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

type config struct {
	port        string
	env         string
	databaseURL string
	jwtSecret   string
	supabaseURL string
	corsOrigins []string
	accessTTL   time.Duration
	refreshTTL  time.Duration
	r2          storage.R2Config
	geocoder    geocode.Config
}

func loadConfig() (config, error) {
	cfg := config{
		port:        getEnv("PORT", "8080"),
		env:         getEnv("ENV", "development"),
		databaseURL: os.Getenv("DATABASE_URL"),
		jwtSecret:   os.Getenv("JWT_SECRET"),
		supabaseURL: os.Getenv("SUPABASE_URL"),
		corsOrigins: splitCSV(getEnv("CORS_ALLOWED_ORIGINS", "http://localhost:5173")),
		r2: storage.R2Config{
			AccountID: os.Getenv("R2_ACCOUNT_ID"),
			AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
			SecretKey: os.Getenv("R2_ACCESS_KEY_SECRET"),
			Bucket:    os.Getenv("R2_BUCKET_NAME"),
			PublicURL: os.Getenv("R2_PUBLIC_URL"),
		},
		geocoder: geocode.Config{
			Enabled:   boolEnv("GEOCODER_ENABLED", true),
			BaseURL:   getEnv("NOMINATIM_BASE_URL", "https://nominatim.openstreetmap.org"),
			UserAgent: getEnv("NOMINATIM_USER_AGENT", "checkpoint-app/0.1"),
		},
	}

	var err error
	if cfg.accessTTL, err = durationEnv("JWT_ACCESS_TTL", 15*time.Minute); err != nil {
		return cfg, err
	}
	if cfg.refreshTTL, err = durationEnv("JWT_REFRESH_TTL", 168*time.Hour); err != nil {
		return cfg, err
	}

	for k, v := range map[string]string{
		"DATABASE_URL": cfg.databaseURL,
		"JWT_SECRET":   cfg.jwtSecret,
		"SUPABASE_URL": cfg.supabaseURL,
	} {
		if v == "" {
			return cfg, fmt.Errorf("missing required env var: %s", k)
		}
	}
	return cfg, nil
}

// splitCSV splits a comma-separated env value, trimming blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

// durationEnv parses a duration env var, ignoring any trailing inline comment
// (e.g. "168h   # 7 days") and falling back to def when unset.
func durationEnv(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	if fields := strings.Fields(raw); len(fields) > 0 {
		raw = fields[0]
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return d, nil
}
