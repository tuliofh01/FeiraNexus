// Package controllers is the CONTROLLER layer: HTTP routing + orchestration.
//
// DIDACTIC — what a controller method does, in order:
//  1. Bind/validate input (views.*Request with binding tags)
//  2. Call the MODEL (store methods) — never raw SQL here
//  3. Map domain errors → HTTP status codes
//  4. Hand a VIEW DTO to views.RenderXxx (never db.* structs, never c.JSON)
//
// router.go = the ONE place where URL → handler binding lives. Adding an
// endpoint is: write controller method + add a line here.
package controllers

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/config"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/crypto"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/middleware"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/worker"
)

// Setup builds the Gin engine with all routes and middleware.
// Called ONCE from cmd/api/main.go.
//
// Middleware ordering matters:
//   global:  Logger (observability) → Recovery (panic → 500, not crash)
//   auth:    attached PER GROUP so register/login/webhook stay public
//   ratelimit: attached to /auth only (brute-force protection where it matters)
//
// SUGGESTION: add these when you have a browser client —
//   CORS (github.com/gin-contrib/cors), request IDs (X-Request-Id → slog),
//   gzip (github.com/gin-contrib/gzip), Prometheus /metrics.
func Setup(
	store *models.Store,
	tm *crypto.TokenManager,
	disp *worker.Dispatcher,
	limiter *middleware.RateLimiter,
	cfg *config.Config,
	logger *slog.Logger,
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	// Controllers (constructor injection — no globals, easy to fake in tests).
	authC := NewAuthController(store, tm, disp, logger)
	prodC := NewProductController(store, logger)
	coC := NewCheckoutController(store, logger)
	whC := NewWebhookController(disp, cfg.STRIPE_WEBHOOK_SECRET, logger)
	hc := NewHealthController(store, disp)

	// --- Infrastructure (public, used by Docker HEALTHCHECK + DO platform) ---
	r.GET("/healthz", hc.Healthz)

	// --- API v1 ---------------------------------------------------------------
	api := r.Group("/api/v1")

	// Auth: rate-limited (credential stuffing protection).
	auth := api.Group("/auth")
	auth.Use(middleware.Limit(limiter))
	{
		auth.POST("/register", authC.Register)
		auth.POST("/login", authC.Login)
		auth.POST("/refresh", authC.Refresh)
		// Authenticated endpoints: middleware.Auth sets user_id/sid/role.
		auth.POST("/logout", middleware.Auth(tm), authC.Logout)
		auth.GET("/sessions", middleware.Auth(tm), authC.ListSessions)
		auth.DELETE("/sessions/:id", middleware.Auth(tm), authC.RevokeSession)
	}

	// Products: public reads, admin-only write.
	products := api.Group("/products")
	{
		products.GET("", prodC.List)
		products.GET("/:id", prodC.Get)
		products.POST("", middleware.Auth(tm), middleware.RequireAdmin(), prodC.Create)
	}

	// Checkout: must be an authenticated user.
	api.POST("/checkout", middleware.Auth(tm), coC.Checkout)

	// Stripe webhook: NO Auth middleware — Stripe can't send our JWT.
	// It authenticates via the HMAC signature in Stripe-Signature (verified
	// inside the handler against STRIPE_WEBHOOK_SECRET).
	api.POST("/webhooks/stripe", whC.StripeWebhook)

	return r
}
