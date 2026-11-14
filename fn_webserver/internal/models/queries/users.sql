-- name: GetUserByEmail :one
SELECT id, email, password_hash, role, stripe_customer_id, created_at, updated_at
FROM users
WHERE email = $1;

-- name: GetUserByID :one
SELECT id, email, password_hash, role, stripe_customer_id, created_at, updated_at
FROM users
WHERE id = $1;

-- name: CreateUser :one
INSERT INTO users (email, password_hash, role)
VALUES ($1, $2, $3)
RETURNING id, email, password_hash, role, stripe_customer_id, created_at, updated_at;

-- name: UpdateUserStripeCustomerID :one
UPDATE users
SET stripe_customer_id = $2,
    updated_at         = now()
WHERE id = $1
RETURNING id, email, password_hash, role, stripe_customer_id, created_at, updated_at;

-- name: UpdateUserRole :one
UPDATE users
SET role       = $2,
    updated_at = now()
WHERE id = $1
RETURNING id, email, password_hash, role, stripe_customer_id, created_at, updated_at;

-- name: DeleteUser :exec
DELETE FROM users
WHERE id = $1;
