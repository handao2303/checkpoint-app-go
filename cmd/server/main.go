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
	"golang.org/x/sync/errgroup"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/internal/cloudsync"
	"github.com/quanluon/checkpoint-app/internal/gamification"
	"github.com/quanluon/checkpoint-app/internal/gdrive"
	"github.com/quanluon/checkpoint-app/internal/gphotos"
	"github.com/quanluon/checkpoint-app/internal/identity"
	"github.com/quanluon/checkpoint-app/internal/journey"
	"github.com/quanluon/checkpoint-app/internal/locationiq"
	"github.com/quanluon/checkpoint-app/internal/place"
	"github.com/quanluon/checkpoint-app/internal/reaction"
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

type startupTask struct {
	name string
	run  func() error
}

func runStartupTasks(tasks ...startupTask) error {
	started := time.Now()
	var group errgroup.Group
	for _, task := range tasks {
		group.Go(func() error {
			taskStarted := time.Now()
			err := task.run()
			if err != nil {
				log.Printf("startup dependency %s failed after %s", task.name, time.Since(taskStarted))
				return fmt.Errorf("%s: %w", task.name, err)
			}
			log.Printf("startup dependency %s ready in %s", task.name, time.Since(taskStarted))
			return nil
		})
	}
	err := group.Wait()
	if err != nil {
		log.Printf("startup dependencies failed after %s", time.Since(started))
		return err
	}
	log.Printf("startup dependencies initialized in %s", time.Since(started))
	return nil
}

func run() error {
	_ = godotenv.Load()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisClient, err := appmw.NewRedisClient(cfg.redisURL)
	if err != nil {
		return err
	}
	if redisClient != nil {
		defer func() { _ = redisClient.Close() }()
	}

	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	var verifier *supabase.Verifier
	if err := runStartupTasks(
		startupTask{
			name: "postgres",
			run:  func() error { return pool.Ping(ctx) },
		},
		startupTask{
			name: "supabase jwks",
			run: func() error {
				var err error
				verifier, err = supabase.NewVerifier(ctx, supabase.JWKSURL(cfg.supabaseURL))
				return err
			},
		},
	); err != nil {
		return err
	}

	// Wire the identity domain.
	tokens := identity.NewTokenManager(cfg.jwtSecret, cfg.accessTTL, cfg.refreshTTL)
	r2 := storage.NewR2(cfg.r2)
	identitySvc := identity.NewService(identity.NewRepository(pool, cfg.r2.PublicURL, redisClient), tokens, verifier, r2)
	identityHandler := identity.NewHandler(identitySvc)

	placeRepo := place.NewRepository(pool, cfg.r2.PublicURL)
	gamificationRepo := gamification.NewRepository(pool, cfg.r2.PublicURL)
	gamificationHandler := gamification.NewHandler(gamification.NewService(gamificationRepo))
	placeHandler := place.NewHandler(place.NewService(placeRepo))
	journeyRepo := journey.NewRepository(pool, cfg.r2.PublicURL)
	journeyHandler := journey.NewHandler(journey.NewService(journeyRepo))
	cpHandler := checkpoint.NewHandler(
		checkpoint.NewService(checkpoint.NewRepository(pool, cfg.r2.PublicURL, placeRepo, gamificationRepo, redisClient), r2, journeyRepo))
	var driveHandler *gdrive.Handler
	var driveSyncer *gdrive.Syncer
	if cfg.drive.clientID != "" {
		driveRepo := gdrive.NewRepository(pool)
		driveClient := gdrive.NewClient(cfg.drive.clientID, cfg.drive.clientSecret)
		driveSyncer = gdrive.NewSyncer(driveRepo, driveClient)
		driveHandler = gdrive.NewHandler(driveRepo, driveClient, []byte(cfg.jwtSecret), cfg.drive.frontendURL, cfg.drive.redirectURI)
	}
	var photosHandler *gphotos.Handler
	var photosSyncer *gphotos.Syncer
	if cfg.photos.clientID != "" {
		photosRepo := gphotos.NewRepository(pool)
		photosClient := gphotos.NewClient(cfg.photos.clientID, cfg.photos.clientSecret)
		photosSyncer = gphotos.NewSyncer(photosRepo, photosClient)
		photosHandler = gphotos.NewHandler(photosRepo, photosClient, []byte(cfg.jwtSecret), cfg.drive.frontendURL, cfg.photos.redirectURI)
	}
	cpHandler.WithCloudSyncer(cloudsync.New(driveSyncer, photosSyncer))
	checkpointHandler := cpHandler
	reactionHandler := reaction.NewHandler(reaction.NewService(reaction.NewRepository(pool)))
	googlePlacesHandler := locationiq.NewHandler(locationiq.NewClient(cfg.placesAPIKey))

	corsOpts := cors.Options{
		AllowedMethods: []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Authorization", "Content-Type"},
		ExposedHeaders: []string{"Retry-After"},
		MaxAge:         300,
	}
	if cfg.env == "production" {
		corsOpts.AllowedOrigins = cfg.corsOrigins
	} else {
		// Dev: allow any localhost / 127.0.0.1 origin (any port) plus configured ones,
		// so it doesn't matter which Vite picks or whether you use localhost vs 127.0.0.1.
		allowed := make(map[string]bool, len(cfg.corsOrigins))
		for _, o := range cfg.corsOrigins {
			allowed[o] = true
		}
		corsOpts.AllowOriginFunc = func(_ *http.Request, origin string) bool {
			return allowed[origin] ||
				strings.HasPrefix(origin, "http://localhost:") ||
				strings.HasPrefix(origin, "http://127.0.0.1:")
		}
	}

	r := chi.NewRouter()
	r.Use(cors.Handler(corsOpts))
	r.Use(chimw.RequestID, chimw.Logger, chimw.Recoverer)
	var ipLimiterOpts, userLimiterOpts, checkinLimiterOpts []appmw.RateLimiterOption
	if redisClient != nil {
		store := appmw.NewRedisRateLimitStore(redisClient, cfg.rateLimit)
		ipLimiterOpts = append(ipLimiterOpts, appmw.WithRateLimitStore(store, "checkpoint:rate-limit:ip:"))
		userLimiterOpts = append(userLimiterOpts, appmw.WithRateLimitStore(store, "checkpoint:rate-limit:user:"))
		checkinStore := appmw.NewRedisRateLimitStore(redisClient, appmw.RateLimitConfig{
			Enabled:           cfg.rateLimit.Enabled,
			RequestsPerMinute: 5,
			Burst:             5,
		})
		checkinLimiterOpts = append(checkinLimiterOpts, appmw.WithRateLimitStore(checkinStore, "checkpoint:rate-limit:checkin:"))
	}
	ipLimiter := appmw.NewRateLimiter(cfg.rateLimit, ipLimiterOpts...)
	userLimiter := appmw.NewRateLimiter(cfg.rateLimit, userLimiterOpts...)
	// POST /checkpoints: 5 requests per minute per user.
	checkinLimiter := appmw.NewRateLimiter(appmw.RateLimitConfig{
		Enabled:           cfg.rateLimit.Enabled,
		RequestsPerMinute: 5,
		Burst:             5,
	}, checkinLimiterOpts...)
	r.Use(ipLimiter.ByIP)
	r.Use(chimw.Timeout(30 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	auth := appmw.Auth(tokens)
	authenticated := func(next http.Handler) http.Handler {
		return auth(userLimiter.ByUser(next))
	}
	// optionalAuth attaches the principal if a token is present but allows
	// anonymous access — used for public share/discover read endpoints.
	optionalAuth := appmw.OptionalAuth(tokens)
	identityHandler.Register(r, authenticated, optionalAuth)
	checkpointHandler.Register(r, authenticated, optionalAuth, checkinLimiter.ByUser)
	reactionHandler.Register(r, authenticated, optionalAuth)
	placeHandler.Register(r, authenticated, optionalAuth)
	journeyHandler.Register(r, authenticated, optionalAuth)
	gamificationHandler.Register(r, authenticated)
	googlePlacesHandler.Register(r, authenticated)
	if driveHandler != nil {
		driveHandler.Register(r, authenticated)
	}
	if photosHandler != nil {
		photosHandler.Register(r, authenticated)
	}

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

type driveConfig struct {
	clientID     string
	clientSecret string
	redirectURI  string
	frontendURL  string
}

type photosConfig struct {
	clientID     string
	clientSecret string
	redirectURI  string
}

type config struct {
	port         string
	env          string
	databaseURL  string
	redisURL     string
	jwtSecret    string
	supabaseURL  string
	corsOrigins  []string
	accessTTL    time.Duration
	refreshTTL   time.Duration
	r2           storage.R2Config
	rateLimit    appmw.RateLimitConfig
	placesAPIKey string
	drive        driveConfig
	photos       photosConfig
}

func loadConfig() (config, error) {
	cfg := config{
		port:        getEnv("PORT", "8080"),
		env:         getEnv("ENV", "development"),
		databaseURL: os.Getenv("DATABASE_URL"),
		redisURL:    os.Getenv("REDIS_URL"),
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
		rateLimit: appmw.RateLimitConfig{
			Enabled: boolEnv("RATE_LIMIT_ENABLED", true),
		},
		placesAPIKey: os.Getenv("LOCATIONIQ_API_KEY"),
		drive: driveConfig{
			clientID:     os.Getenv("GOOGLE_DRIVE_CLIENT_ID"),
			clientSecret: os.Getenv("GOOGLE_DRIVE_CLIENT_SECRET"),
			redirectURI:  os.Getenv("GOOGLE_DRIVE_REDIRECT_URI"),
			frontendURL:  getEnv("FRONTEND_URL", "http://localhost:5173"),
		},
		photos: photosConfig{
			// Falls back to Drive credentials so users don't need extra env vars.
			clientID:     firstNonEmpty("GOOGLE_PHOTOS_CLIENT_ID", "GOOGLE_DRIVE_CLIENT_ID"),
			clientSecret: firstNonEmpty("GOOGLE_PHOTOS_CLIENT_SECRET", "GOOGLE_DRIVE_CLIENT_SECRET"),
			redirectURI:  os.Getenv("GOOGLE_PHOTOS_REDIRECT_URI"),
		},
	}

	var err error
	if cfg.accessTTL, err = durationEnv("JWT_ACCESS_TTL", 15*time.Minute); err != nil {
		return cfg, err
	}
	if cfg.refreshTTL, err = durationEnv("JWT_REFRESH_TTL", 168*time.Hour); err != nil {
		return cfg, err
	}
	if cfg.rateLimit.RequestsPerMinute, err = positiveIntEnv("RATE_LIMIT_REQUESTS_PER_MINUTE", 120); err != nil {
		return cfg, err
	}
	if cfg.rateLimit.Burst, err = positiveIntEnv("RATE_LIMIT_BURST", 30); err != nil {
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
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// firstNonEmpty returns the value of the first key whose env var is non-empty.
func firstNonEmpty(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
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

func positiveIntEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
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
