-- name: CreateSession :one
INSERT INTO sessions (user_id, refresh_token_hash, user_agent, client_ip, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, refresh_token_hash, user_agent, client_ip, is_revoked, expires_at, created_at;

-- name: GetSessionByRefreshHash :one
SELECT id, user_id, refresh_token_hash, user_agent, client_ip, is_revoked, expires_at, created_at
FROM sessions
WHERE refresh_token_hash = $1;

-- name: GetSessionByID :one
SELECT id, user_id, refresh_token_hash, user_agent, client_ip, is_revoked, expires_at, created_at
FROM sessions
WHERE id = $1;

-- name: ListUserSessions :many
SELECT id, user_id, refresh_token_hash, user_agent, client_ip, is_revoked, expires_at, created_at
FROM sessions
WHERE user_id = $1
  AND is_revoked = false
  AND expires_at > now()
ORDER BY created_at DESC;

-- name: RotateSessionToken :one
UPDATE sessions
SET refresh_token_hash = $2,
    expires_at         = $3,
    created_at         = now()
WHERE id = $1
  AND is_revoked = false
  AND expires_at > now()
RETURNING id, user_id, refresh_token_hash, user_agent, client_ip, is_revoked, expires_at, created_at;

-- name: RevokeSession :exec
UPDATE sessions
SET is_revoked = true
WHERE id = $1
  AND user_id = $2;

-- name: RevokeAllUserSessions :exec
UPDATE sessions
SET is_revoked = true
WHERE user_id = $1
  AND is_revoked = false;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions
WHERE expires_at < now();
