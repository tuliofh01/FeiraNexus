package views

import (
	"time"

	"github.com/google/uuid"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
)

// ---------------------------------------------------------------------------
// Incoming request DTOs (Gin binds + validates JSON into these)
// ---------------------------------------------------------------------------
//
// DIDACTIC — binding tags are declarative validation: gin uses
// github.com/go-playground/validator under the hood. A failed tag →
// ShouldBindJSON returns an error → controller answers 400 WITHOUT touching
// the DB. Validate at the edge, trust nothing from the client.

// RegisterRequest is POST /api/v1/auth/register.
type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`       // must look like an email
	Password string `json:"password" binding:"required,min=8,max=72"` // argon2-friendly length
}

// LoginRequest is POST /api/v1/auth/login (same shape as register).
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// RefreshRequest is POST /api/v1/auth/refresh.
// The refresh token is a secret — sent in the BODY, never in a URL
// (URLs leak into logs, history, and Referer headers).
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// ---------------------------------------------------------------------------
// Outgoing response DTOs (serializers)
// ---------------------------------------------------------------------------

// UserView is the public shape of a user.
// DIDACTIC: db.User has PasswordHash — UserView doesn't. That's the whole
// point of a DTO: the hash CANNOT leak because this struct has no such field.
type UserView struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// ToUserView maps db.User → UserView (strips password_hash + stripe ids).
func ToUserView(u db.User) UserView {
	return UserView{
		ID:        u.ID,
		Email:     u.Email,
		Role:      u.Role,
		CreatedAt: u.CreatedAt,
	}
}

// SessionView is the public shape of a session — refresh_token_hash stripped.
type SessionView struct {
	ID        uuid.UUID `json:"id"`
	UserAgent string    `json:"user_agent,omitempty"` // omitempty: not all clients send one
	ClientIP  string    `json:"client_ip,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// ToSessionView maps db.Session → SessionView, handling nullable columns
// (sql.NullString → plain string + omitempty).
func ToSessionView(s db.Session) SessionView {
	return SessionView{
		ID:        s.ID,
		UserAgent: s.UserAgent.String, // .String is "" when Valid == false
		ClientIP:  s.ClientIP.String,
		ExpiresAt: s.ExpiresAt,
		CreatedAt: s.CreatedAt,
	}
}

// AuthResponseView is returned by login/refresh: what the client needs to
// start/continue an authenticated session.
type AuthResponseView struct {
	AccessToken  string    `json:"access_token"`  // 15-min JWT → Authorization header
	RefreshToken string    `json:"refresh_token"` // opaque → POST /auth/refresh body
	ExpiresIn    int64     `json:"expires_in"`    // seconds, so the client can schedule refresh
	TokenType    string    `json:"token_type"`    // "Bearer" (RFC 6750 convention)
	User         UserView  `json:"user"`
}

// NewAuthResponse assembles the login/refresh payload.
// refreshTTLSeconds/accessTokenTTL come from crypto so views don't hardcode
// security policy (single source of truth in internal/crypto).
func NewAuthResponse(accessToken, refreshToken string, accessTokenTTLSeconds int64, u db.User) AuthResponseView {
	return AuthResponseView{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    accessTokenTTLSeconds,
		TokenType:    "Bearer",
		User:         ToUserView(u),
	}
}
