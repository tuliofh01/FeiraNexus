// Package middleware holds Gin middlewares — functions that run BEFORE your
// controller handler: auth, rate limiting, logging, recovery.
//
// DIDACTIC: middleware chains in Gin:
//   router.Group → .Use(mw) → handler. Order matters: auth must run before
//   the handler reads c.GetString("user_id").
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/crypto"
)

// Context keys set by Auth and read by controllers.
// Use constants so a typo fails at compile time, not at runtime.
const (
	CtxUserID    = "user_id"
	CtxSessionID = "session_id"
	CtxRole      = "role"
)

// Auth validates "Authorization: Bearer <JWT>" and injects identity into the
// request context. Requests WITHOUT a token are rejected — register/login/
// webhook routes intentionally do NOT use this middleware.
//
// SUGGESTION: add "token revocation on logout" for access tokens too —
// JWTs are stateless, so a stolen access token stays valid until expiry.
// Options: short TTL (we use 15 min) + a tiny Redis "denylist" for logged-out sids.
func Auth(tm *crypto.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   gin.H{"code": 401, "message": "missing Authorization header"},
			})
			return
		}
		raw, found := strings.CutPrefix(header, "Bearer ")
		if !found || raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   gin.H{"code": 401, "message": "malformed Authorization header"},
			})
			return
		}

		claims, err := tm.ParseAccessToken(raw)
		if err != nil {
			// Deliberately generic: never tell the client WHY the token
			// failed (expired vs forged) — that's an oracle for attackers.
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"error":   gin.H{"code": 401, "message": "invalid or expired token"},
			})
			return
		}

		// DIDACTIC: context values are how you pass request-scoped data from
		// middleware to handlers without changing handler signatures.
		c.Set(CtxUserID, claims.Subject)
		c.Set(CtxSessionID, claims.SessionID)
		c.Set(CtxRole, claims.Role)
		c.Next() // continue to the next middleware/handler
	}
}

// RequireAdmin aborts with 403 unless the JWT role is "admin".
// MUST run after Auth (it reads the role Auth set).
//
// SUGGESTION: real systems add roles/permissions beyond admin|customer —
// design a small RBAC table and check permissions here instead of one string.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString(CtxRole) != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"error":   gin.H{"code": 403, "message": "admin access required"},
			})
			return
		}
		c.Next()
	}
}
