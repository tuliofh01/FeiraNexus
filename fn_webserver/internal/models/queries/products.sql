-- name: ListProducts :many
SELECT id, name, description, price_cents, currency, stock_quantity, is_active, created_at
FROM products
WHERE is_active = true
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: ListProductsAdmin :many
SELECT id, name, description, price_cents, currency, stock_quantity, is_active, created_at
FROM products
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: GetProduct :one
SELECT id, name, description, price_cents, currency, stock_quantity, is_active, created_at
FROM products
WHERE id = $1;

-- name: GetProductForUpdate :one
SELECT id, name, description, price_cents, currency, stock_quantity, is_active, created_at
FROM products
WHERE id = $1
FOR UPDATE;

-- name: CountProducts :one
SELECT count(*)
FROM products
WHERE is_active = true;

-- name: CreateProduct :one
INSERT INTO products (name, description, price_cents, currency, stock_quantity, is_active)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, description, price_cents, currency, stock_quantity, is_active, created_at;

-- name: UpdateProduct :one
UPDATE products
SET name           = $2,
    description    = $3,
    price_cents    = $4,
    currency       = $5,
    stock_quantity = $6,
    is_active      = $7
WHERE id = $1
RETURNING id, name, description, price_cents, currency, stock_quantity, is_active, created_at;

-- name: DecrementProductStock :execrows
UPDATE products
SET stock_quantity = stock_quantity - $2
WHERE id = $1
  AND stock_quantity >= $2;

-- name: DeactivateProduct :one
UPDATE products
SET is_active = false
WHERE id = $1
RETURNING id, name, description, price_cents, currency, stock_quantity, is_active, created_at;
