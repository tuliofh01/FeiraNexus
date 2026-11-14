package controllers

import (
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/crypto"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/middleware"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/views"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/worker"
)

// AuthController owns the full session lifecycle: register → login → refresh
// (with rotation + theft detection) → logout → session management.
type AuthController struct {
	store  *models.Store
	tokens *crypto.TokenManager
	disp   *worker.Dispatcher
	logger *slog.Logger
}

func NewAuthController(store *models.Store, tm *crypto.TokenManager, disp *worker.Dispatcher, logger *slog.Logger) *AuthController {
	return &AuthController{store: store, tokens: tm, disp: disp, logger: logger}
}

// Register creates a customer account.
// POST /api/v1/auth/register
func (ac *AuthController) Register(c *gin.Context) {
	var req views.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// SUGGESTION: translate validator errors to friendly per-field messages
		// ({"field":"email","issue":"invalid format"}) instead of echoing err.
		views.RenderError(c, 400, "invalid request: "+err.Error())
		return
	}

	// Normalize: emails are case-insensitive for uniqueness in practice.
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Argon2id hash with a fresh random salt (see internal/crypto/password.go).
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		ac.logger.Error("register: hash password", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	user, err := ac.store.CreateUser(c.Request.Context(), db.CreateUserParams{
		Email:        email,
		PasswordHash: hash,
		Role:         "customer", // self-registration is never admin — SUGGESTION: seed the first admin via a CLI/flag
	})
	if err != nil {
		// Postgres unique_violation = duplicate email. We detect it by error
		// CODE (23505), not by string matching — locale-proof and stable.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			views.RenderError(c, 409, "email already registered")
			return
		}
		ac.logger.Error("register: create user", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	views.RenderCreated(c, views.ToUserView(user))
}

// Login verifies credentials and starts a session:
// returns a 15-min access JWT + a rotating refresh token.
// POST /api/v1/auth/login
func (ac *AuthController) Login(c *gin.Context) {
	var req views.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		views.RenderError(c, 400, "invalid request: "+err.Error())
		return
	}
	ctx := c.Request.Context()
	email := strings.ToLower(strings.TrimSpace(req.Email))

	user, err := ac.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			ac.failedLogin(c) // same message as wrong password (no user enumeration!)
			return
		}
		ac.logger.Error("login: lookup user", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	// Constant-time Argon2id verification (hash contains its own salt).
	if err := crypto.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		ac.failedLogin(c)
		return
	}

	// Session creation: refresh token is generated ONCE, only its SHA-256
	// digest touches the DB (see internal/crypto/token.go).
	refreshPlain, refreshHash, err := crypto.GenerateRefreshToken()
	if err != nil {
		ac.logger.Error("login: generate refresh", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	session, err := ac.store.CreateSession(ctx, db.CreateSessionParams{
		UserID:           user.ID,
		RefreshTokenHash: refreshHash,
		UserAgent:        nullString(c.GetHeader("User-Agent")),
		ClientIP:         nullString(c.ClientIP()),
		ExpiresAt:        time.Now().Add(crypto.RefreshTokenTTL),
	})
	if err != nil {
		ac.logger.Error("login: create session", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	// Access token carries sid → logout/refresh can target this session.
	access, err := ac.tokens.GenerateAccessToken(user.ID.String(), session.ID.String(), user.Role)
	if err != nil {
		ac.logger.Error("login: sign jwt", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	views.RenderSuccess(c, views.NewAuthResponse(access, refreshPlain, int64(crypto.AccessTokenTTL.Seconds()), user))
}

// Refresh rotates the refresh token and issues a new access token.
//
// THEFT DETECTION (the security-critical part):
//   A refresh token is SINGLE-USE (rotated on every refresh). If we see a
//   token that is already revoked, someone is replaying a stolen token —
//   we revoke EVERY session for that user and answer 401. The actual
//   revocation runs async on the worker (fast response, durable work).
//
// POST /api/v1/auth/refresh
func (ac *AuthController) Refresh(c *gin.Context) {
	var req views.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		views.RenderError(c, 400, "invalid request: "+err.Error())
		return
	}
	ctx := c.Request.Context()

	// Lookup by DIGEST — the DB never stores the plaintext token.
	session, err := ac.store.GetSessionByRefreshHash(ctx, crypto.HashRefreshToken(req.RefreshToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			views.RenderError(c, 401, "invalid refresh token")
			return
		}
		ac.logger.Error("refresh: lookup session", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	// Replay of a revoked token → assume theft, kill all sessions.
	if session.IsRevoked {
		ac.logger.Warn("refresh: revoked token replayed (possible theft)", "user_id", session.UserID)
		// Non-blocking for the response path: worker handles bulk revocation.
		if err := ac.disp.Enqueue(worker.Job{
			Type:    worker.JobRevokeCompromisedUser,
			Payload: map[string]string{"user_id": session.UserID.String()},
		}); err != nil {
			// Queue full/closed: revoke synchronously as fallback (fail SECURE).
			ac.logger.Error("refresh: enqueue revoke fallback", "err", err)
			if rerr := ac.store.RevokeAllUserSessions(ctx, session.UserID); rerr != nil {
				ac.logger.Error("refresh: sync revoke failed", "err", rerr)
			}
		}
		views.RenderError(c, 401, "invalid refresh token")
		return
	}
	if session.ExpiresAt.Before(time.Now()) {
		views.RenderError(c, 401, "invalid refresh token")
		return
	}

	// Rotate: new refresh token replaces the old (old becomes unusable).
	newPlain, newHash, err := crypto.GenerateRefreshToken()
	if err != nil {
		ac.logger.Error("refresh: generate token", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	rotated, err := ac.store.RotateSessionToken(ctx, db.RotateSessionTokenParams{
		ID:               session.ID,
		RefreshTokenHash: newHash,
		ExpiresAt:        time.Now().Add(crypto.RefreshTokenTTL),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// WHERE clause failed: revoked/expired BETWEEN our read and write
			// (concurrent refresh). Treat as invalid.
			views.RenderError(c, 401, "invalid refresh token")
			return
		}
		ac.logger.Error("refresh: rotate", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	user, err := ac.store.GetUserByID(ctx, session.UserID)
	if err != nil {
		ac.logger.Error("refresh: load user", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	access, err := ac.tokens.GenerateAccessToken(user.ID.String(), rotated.ID.String(), user.Role)
	if err != nil {
		ac.logger.Error("refresh: sign jwt", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	views.RenderSuccess(c, views.NewAuthResponse(access, newPlain, int64(crypto.AccessTokenTTL.Seconds()), user))
}

// Logout revokes the CURRENT session (sid comes from the validated JWT).
// The access token itself stays technically valid until expiry — see the
// SUGGESTION in middleware/auth.go about denylists.
// POST /api/v1/auth/logout
func (ac *AuthController) Logout(c *gin.Context) {
	uid, sid, ok := ac.identity(c)
	if !ok {
		return
	}
	if err := ac.store.RevokeSession(c.Request.Context(), db.RevokeSessionParams{ID: sid, UserID: uid}); err != nil {
		ac.logger.Error("logout: revoke", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	views.RenderSuccess(c, gin.H{"logged_out": true})
}

// ListSessions returns the user's ACTIVE sessions (device management view).
// GET /api/v1/auth/sessions
func (ac *AuthController) ListSessions(c *gin.Context) {
	uid, _, ok := ac.identity(c)
	if !ok {
		return
	}
	sessions, err := ac.store.ListUserSessions(c.Request.Context(), uid)
	if err != nil {
		ac.logger.Error("sessions: list", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	out := make([]views.SessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, views.ToSessionView(s)) // refresh_token_hash stripped
	}
	views.RenderSuccess(c, out)
}

// RevokeSession revokes ONE session by id — but only if it belongs to the
// caller (user_id is part of the WHERE clause: no IDOR vulnerability).
// DELETE /api/v1/auth/sessions/:id
func (ac *AuthController) RevokeSession(c *gin.Context) {
	uid, _, ok := ac.identity(c)
	if !ok {
		return
	}
	sid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		views.RenderError(c, 400, "invalid session id")
		return
	}
	if err := ac.store.RevokeSession(c.Request.Context(), db.RevokeSessionParams{ID: sid, UserID: uid}); err != nil {
		ac.logger.Error("sessions: revoke", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	views.RenderSuccess(c, gin.H{"revoked": true})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// failedLogin always answers the same generic message — username oracle
// prevention (attackers can't probe which emails are registered).
func (ac *AuthController) failedLogin(c *gin.Context) {
	views.RenderError(c, 401, "invalid email or password")
}

// identity extracts user_id/sid set by middleware.Auth. On missing/invalid
// values it renders 401 and returns ok=false so callers just `return`.
func (ac *AuthController) identity(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	uid, err := uuid.Parse(c.GetString(middleware.CtxUserID))
	if err != nil {
		views.RenderError(c, 401, "unauthenticated")
		return uuid.Nil, uuid.Nil, false
	}
	sid, err := uuid.Parse(c.GetString(middleware.CtxSessionID))
	if err != nil {
		views.RenderError(c, 401, "unauthenticated")
		return uuid.Nil, uuid.Nil, false
	}
	return uid, sid, true
}

// nullString converts "" → NULL for nullable TEXT columns.
func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
