// Command api is the application entrypoint (Composition Root).
//
// DIDACTIC — "composition root" pattern: this file is the ONLY place that
// knows how all the pieces connect. It:
//   1. loads config (fail fast on missing secrets)
//   2. opens the DB pool (ping = fail fast if unreachable)
//   3. constructs dependencies bottom-up: store → tokens → dispatcher → routes
//   4. starts goroutines (HTTP server + worker pool)
//   5. waits for SIGINT/SIGTERM, then shuts down GRACEFULLY:
//      stop accepting connections → drain in-flight → drain job queue → close
//
// Everything below main() is deliberately small; complexity lives in the
// internal/ packages where it's testable.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v81"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/config"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/controllers"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/crypto"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/middleware"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/worker"
)

func main() {
	// Structured logs to stdout (12-factor: containers log to stdout only).
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 1) Config — exits if .env / platform env vars are incomplete.
	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	gin.SetMode(cfg.GIN_MODE) // "release" in production (quieter + faster)

	// 2) Graceful-shutdown root context: canceled by FIRST SIGINT/SIGTERM.
	//    (After cancel, a second Ctrl+C uses Go's default: immediate exit.)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 3) DB pool — pgxpool multiplexes connections across request goroutines.
	pool, err := pgxpool.New(ctx, cfg.DATABASE_URL)
	if err != nil {
		logger.Error("db pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		logger.Error("db unreachable", "err", err)
		os.Exit(1)
	}
	logger.Info("database connected")

	// Validate the AES master key NOW so a bad key fails at boot, not on the
	// first PII write. TODO(you): keep this *crypto.AESEncrypter and use it
	// to encrypt sensitive columns at rest (e.g. stripe_customer_id, addresses).
	if _, err := crypto.NewAESEncrypter(cfg.AES_MASTER_KEY_HEX); err != nil {
		logger.Error("aes key invalid", "err", err)
		os.Exit(1)
	}

	// 4) Model + auth primitives.
	store := models.New(pool)
	tm, err := crypto.NewTokenManager(cfg.JWT_SECRET)
	if err != nil {
		logger.Error("jwt secret invalid", "err", err)
		os.Exit(1)
	}

	// 5) Worker pool (4 goroutines + 30-min session janitor).
	//    MUST be started before traffic: webhooks enqueue immediately.
	disp := worker.New(store, logger)
	disp.Start(ctx) // janitor stops when ctx is canceled

	// 6) Stripe global key (used by checkout controller).
	//    Placeholder check → checkout returns 503 until you add a real key.
	if cfg.STRIPE_SECRET_KEY != "" && cfg.STRIPE_SECRET_KEY != "sk_test_placeholder" {
		stripe.Key = cfg.STRIPE_SECRET_KEY
	} else {
		logger.Warn("STRIPE_SECRET_KEY not set — checkout will be unavailable")
	}

	// 7) HTTP server. Route table lives in controllers.Setup.
	limiter := middleware.NewRateLimiter(10, 20) // 10 rps, burst 20 per IP
	engine := controllers.Setup(store, tm, disp, limiter, cfg, logger)

	srv := &http.Server{
		Addr:              ":" + cfg.PORT,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second, // slowloris mitigation
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()
	logger.Info("feiranexus api listening", "port", cfg.PORT, "mode", cfg.GIN_MODE)

	// 8) Block until signal, then unwind in reverse order:
	//    HTTP stop → queue drain → pool close (deferred).
	<-ctx.Done()
	logger.Info("shutdown signal received")

	stop() // restore default handling: 2nd signal = force quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "err", err) // in-flight requests got 10s
	}

	disp.Shutdown() // close channel → workers finish buffered jobs → exit
	logger.Info("shutdown complete")
}
