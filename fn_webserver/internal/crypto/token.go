package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ---------------------------------------------------------------------------
// JWT access tokens (HS256) + CSPRNG refresh tokens (SHA-256 digested)
// ---------------------------------------------------------------------------
//
// DIDACTIC — the two-token auth pattern used here:
//
//  ACCESS TOKEN (JWT, short-lived 15 min)
//    - Carries claims: sub (user id), sid (session id), role.
//    - Signed with HMAC-SHA256 using JWT_SECRET from .env.
//    - Sent on EVERY request in "Authorization: Bearer <token>".
//    - Because it's signed (not encrypted) it is *readable* by the client —
//      never put passwords or secrets inside. Integrity is guaranteed by HMAC.
//
//  REFRESH TOKEN (opaque random 32 bytes, long-lived 7 days)
//    - Only the SHA-256 HEX DIGEST is stored in sessions.refresh_token_hash.
//      If the DB leaks, attackers cannot recover the refresh token from the
//      digest (SHA-256 is one-way).
//    - Used to obtain a new access token at POST /auth/refresh.
//    - ROTATED on every use: old session row is updated with a new hash.
//      Reuse of an already-rotated/revoked token = evidence of theft →
//      the controller revokes ALL sessions for that user (compromise response).
//
// SUGGESTION: when you're comfortable, add refresh-token "families" (store a
// parent id) so you can revoke a whole chain, and consider the RFC 9700
// (OAuth refresh token rotation) recommendations.

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 7 * 24 * time.Hour // used for sessions.expires_at
	refreshByteLen  = 32                  // 256-bit CSPRNG refresh token
)

// Claims are the custom JWT claims (plus registered ones like exp/iat).
// DIDACTIC: json tags here control the claim names *inside* the token payload.
type Claims struct {
	v5Registered
	SessionID string `json:"sid"`
	Role      string `json:"role"`
}

// v5Registered embeds the standard claims (exp, iat, sub, jti...).
// Aliased to keep the public Claims struct tidy.
type v5Registered = jwt.RegisteredClaims

// TokenManager holds the signing secret. Create ONCE at startup and share.
type TokenManager struct {
	secret []byte
}

// NewTokenManager validates the secret length up front (fail fast at boot,
// not on the first login). 256-bit = 32 bytes = 64 hex chars from gen_env.sh.
func NewTokenManager(secret string) (*TokenManager, error) {
	if len(secret) < 32 {
		return nil, errors.New("crypto: JWT_SECRET must be at least 32 bytes (run scripts/gen_env.sh)")
	}
	return &TokenManager{secret: []byte(secret)}, nil
}

// GenerateAccessToken signs a 15-minute HS256 JWT for the user+session.
func (t *TokenManager) GenerateAccessToken(userID, sessionID, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		v5Registered: v5Registered{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			Issuer:    "feiranexus",
		},
		SessionID: sessionID,
		Role:      role,
	}
	// jwt/v5: NewWithClaims picks the alg; we hardcode HS256 (never trust
	// "alg" from the token header — that's the classic alg=none attack).
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ParseAccessToken verifies signature + expiry and returns the claims.
// Returns an error for tampered, expired, or wrong-signature tokens.
func (t *TokenManager) ParseAccessToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(tok *jwt.Token) (any, error) {
		// Belt-and-suspenders: reject anything that isn't HS256 even if the
		// library would already be suspicious.
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("crypto: unexpected signing method %v", tok.Header["alg"])
		}
		return t.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("crypto: invalid token")
	}
	return claims, nil
}

// GenerateRefreshToken returns:
//   - plaintext: the value the client stores (base64url, 32 random bytes)
//   - hash: the SHA-256 hex digest we persist in sessions.refresh_token_hash
//
// The plaintext is returned EXACTLY ONCE (at login/refresh) and never stored.
func GenerateRefreshToken() (plaintext, hash string, err error) {
	buf := make([]byte, refreshByteLen)
	if _, err := rand.Read(buf); err != nil { // crypto/rand CSPRNG
		return "", "", fmt.Errorf("crypto: generating refresh token: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, HashRefreshToken(plaintext), nil
}

// HashRefreshToken computes the hex digest stored in the DB.
// Used at login (store) and at refresh (lookup + compare).
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CompareRefreshToken checks a presented token against a stored digest in
// constant time (equal length digests, so ConstantTimeCompare is safe here).
func CompareRefreshToken(plaintext, storedHashHex string) bool {
	a := sha256.Sum256([]byte(plaintext))
	b, err := hex.DecodeString(storedHashHex)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(a[:], b) == 1
}
