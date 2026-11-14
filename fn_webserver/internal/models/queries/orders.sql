-- name: CreateOrder :one
INSERT INTO orders (user_id, status, total_cents, currency, stripe_payment_intent_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at;

-- name: CreateOrderItem :one
INSERT INTO order_items (order_id, product_id, quantity, unit_price_cents)
VALUES ($1, $2, $3, $4)
RETURNING id, order_id, product_id, quantity, unit_price_cents;

-- name: GetOrder :one
SELECT id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at
FROM orders
WHERE id = $1;

-- name: GetOrderByPaymentIntent :one
SELECT id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at
FROM orders
WHERE stripe_payment_intent_id = $1;

-- name: ListUserOrders :many
SELECT id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at
FROM orders
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListOrderItems :many
SELECT id, order_id, product_id, quantity, unit_price_cents
FROM order_items
WHERE order_id = $1;

-- name: UpdateOrderStatus :one
UPDATE orders
SET status = $2
WHERE id = $1
RETURNING id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at;

-- name: SetOrderPaymentIntent :one
UPDATE orders
SET stripe_payment_intent_id = $2
WHERE id = $1
RETURNING id, user_id, status, total_cents, currency, stripe_payment_intent_id, created_at;
