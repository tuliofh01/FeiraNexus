// Package models is the MODEL layer of the MVC architecture.
//
// It owns everything that touches the database:
//   - migrations/ : raw DDL (schema)
//   - queries/    : raw SQL (source of truth for all DB access)
//   - db/         : sqlc-generated, type-safe Go methods (DO NOT hand-edit)
//   - store.go    : "domain" wrapper that adds transactions on top of db/
//
// DIDACTIC NOTE — why this layer exists:
// Controllers NEVER write SQL and NEVER see raw connection pools. They call
// Store methods. This keeps every database concern in one place, makes the
// code testable (you can fake a Store), and prevents SQL injection by design
// (sqlc generates parameterized queries: $1, $2, ...).
package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
)

// ErrInsufficientStock is a domain-level error. DIDACTIC PATTERN: instead of
// returning HTTP codes from this layer (that's the View's job), we return
// sentinel errors with `errors.Is`-comparable identity. Controllers translate
// them to status codes.
//
// SUGGESTION: add more sentinel errors as you grow (ErrEmailTaken,
// ErrOrderNotPending, ...) and map them in one place in the controllers.
var ErrInsufficientStock = errors.New("insufficient stock for one or more products")

// Store wraps the sqlc-generated Queries with a pgx connection pool so we can
// open ACID transactions. Think of it as: Queries = single statements,
// Store = "single statements + multi-statement atomic units".
//
// The embedded *db.Queries means Store automatically "promotes" every query
// method (Store.GetUserByEmail works without extra boilerplate) — this is Go
// embedding, a core language feature you should get comfortable with.
type Store struct {
	*db.Queries
	pool *pgxpool.Pool
}

// New creates a Store. The pool is created once at startup in cmd/api/main.go
// and shared by every request goroutine (pgxpool is safe for concurrent use).
func New(pool *pgxpool.Pool) *Store {
	return &Store{
		Queries: db.New(pool),
		pool:    pool,
	}
}

// Pool exposes the raw pool for infrastructure tasks (health checks, ping).
// SUGGESTION: you usually don't want controllers touching this directly —
// keep pool usage inside the models package if you can.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ExecTx runs fn inside a database transaction.
//
// DIDACTIC — the 3 rules of transactions in Go:
//  1. BEGIN, then ALWAYS defer ROLLBACK. Rollback on a committed tx is a
//     no-op (it returns ErrTxClosed which we ignore), so this is safe and
//     guarantees that any early return/failure doesn't leave a dangling tx.
//  2. fn receives a Queries bound to the tx — every query inside fn is now
//     part of the same atomic unit.
//  3. COMMIT happens ONLY if fn returns nil. Any error => automatic rollback.
//
// SUGGESTION: add context timeouts here (e.g. ctx, cancel :=
// context.WithTimeout(ctx, 5*time.Second)) so a stuck query can't hold a
// connection forever. Also consider pgx's nested savepoints if you ever need
// "transactions inside transactions".
func (s *Store) ExecTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		// Deliberately ignoring the rollback error: if we already committed,
		// rollback returns pgx.ErrTxClosed which is harmless here.
		_ = tx.Rollback(ctx)
	}()

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err // deferred rollback fires automatically
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// OrderLine is an input DTO for creating orders. Notice it carries only what
// the business needs (product + quantity) — the price is NEVER trusted from
// the client, we always re-read it from the DB inside the transaction.
//
// SUGGESTION: add validation tags or a Validate() method if you expose this
// to untrusted input directly.
type OrderLine struct {
	ProductID uuid.UUID
	Quantity  int32
}

// CreateOrderTx atomically: (1) locks each product row FOR UPDATE,
// (2) re-reads price + verifies stock, (3) inserts the order, (4) inserts the
// items. Either everything succeeds or nothing does.
//
// WHY lock with FOR UPDATE? Two concurrent checkouts for the last unit of a
// product would both read stock=1, both pass validation, and oversell.
// FOR UPDATE serializes them: the second transaction blocks until the first
// commits, then re-reads the NEW stock value.
//
// SUGGESTION: this is the classic "lost update" race — search for "pessimistic
// locking" to go deeper. The alternative (optimistic locking with a version
// column) is also worth learning.
func (s *Store) CreateOrderTx(ctx context.Context, userID uuid.UUID, currency string, lines []OrderLine) (db.Order, []db.OrderItem, error) {
	var (
		createdOrder db.Order
		items        []db.OrderItem
	)

	err := s.ExecTx(ctx, func(q *db.Queries) error {
		var totalCents int64

		// Pass 1: lock + price + stock check for every line.
		// NOTE: we process lines in a deterministic order (product ID) to
		// avoid deadlocks when two transactions touch the same products in
		// reverse order. SUGGESTION: sort lines if the caller can't guarantee order.
		for _, line := range lines {
			// GetProductForUpdate = SELECT ... FOR UPDATE (row-level lock).
			product, err := q.GetProductForUpdate(ctx, line.ProductID)
			if err != nil {
				// pgx.ErrNoRows means the product doesn't exist.
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("product %s: %w", line.ProductID, ErrInsufficientStock)
				}
				return fmt.Errorf("lock product %s: %w", line.ProductID, err)
			}
			if !product.IsActive {
				return fmt.Errorf("product %s is not available: %w", line.ProductID, ErrInsufficientStock)
			}
			if product.StockQuantity < line.Quantity {
				return fmt.Errorf("product %s: %w", line.ProductID, ErrInsufficientStock)
			}

			// Price comes from the DB, never from the HTTP request body —
			// otherwise a malicious client could pay 1 cent for anything.
			totalCents += product.PriceCents * int64(line.Quantity)
		}

		// Pass 2: insert the order row (status starts as "pending" — the
		// Stripe webhook flips it to "paid" asynchronously).
		order, err := q.CreateOrder(ctx, db.CreateOrderParams{
			UserID:                userID,
			Status:                "pending",
			TotalCents:            totalCents,
			Currency:              currency,
			StripePaymentIntentID: sql.NullString{}, // filled in after Stripe responds
		})
		if err != nil {
			return fmt.Errorf("create order: %w", err)
		}

		// Pass 3: insert the items (unit price snapshotted now, so later
		// price changes never rewrite history on old invoices).
		for _, line := range lines {
			product, err := q.GetProductForUpdate(ctx, line.ProductID)
			if err != nil {
				return fmt.Errorf("re-read product %s: %w", line.ProductID, err)
			}
			item, err := q.CreateOrderItem(ctx, db.CreateOrderItemParams{
				OrderID:        order.ID,
				ProductID:      line.ProductID,
				Quantity:       line.Quantity,
				UnitPriceCents: product.PriceCents,
			})
			if err != nil {
				return fmt.Errorf("create order item: %w", err)
			}
			items = append(items, item)
		}

		createdOrder = order
		return nil
	})

	return createdOrder, items, err
}

// ApplyPaymentTx is what the worker runs when Stripe says "payment succeeded":
// mark the order "paid" AND decrement stock atomically. Stock is deliberately
// NOT decremented at checkout time in this design (skeleton) — a later
// improvement is to reserve stock at checkout and release it on timeout.
//
// IDEMPOTENCY: if the webhook fires twice (Stripe retries!), the status check
// makes the second run a no-op — but WITHOUT double-decrementing stock.
//
// SUGGESTION: to make stock handling production-grade, reserve stock during
// CreateOrderTx (decrement there) and RESTORE it here when a payment fails or
// a pending order expires. Add a "pending orders janitor" job alongside the
// session janitor in internal/worker.
func (s *Store) ApplyPaymentTx(ctx context.Context, paymentIntentID string) (db.Order, error) {
	var updated db.Order

	err := s.ExecTx(ctx, func(q *db.Queries) error {
		order, err := q.GetOrderByPaymentIntent(ctx, sql.NullString{
			String: paymentIntentID,
			Valid:  true,
		})
		if err != nil {
			return fmt.Errorf("lookup order by payment intent: %w", err)
		}

		// Idempotency guard: already paid? Do nothing (but still succeed).
		if order.Status == "paid" {
			updated = order
			return nil
		}
		if order.Status != "pending" {
			// Don't resurrect failed/canceled orders from a late webhook.
			updated = order
			return nil
		}

		items, err := q.ListOrderItems(ctx, order.ID)
		if err != nil {
			return fmt.Errorf("list order items: %w", err)
		}
		for _, item := range items {
			// Lock the product row again — this tx is modifying stock.
			if _, err := q.GetProductForUpdate(ctx, item.ProductID); err != nil {
				return fmt.Errorf("lock product %s: %w", item.ProductID, err)
			}
			// DecrementProductStock has a built-in guard:
			//   UPDATE ... SET stock = stock - $2 WHERE stock >= $2
			// If stock vanished meanwhile, RowsAffected = 0 => we abort the
			// whole transaction (order stays pending, you get a signal to fix data).
			n, err := q.DecrementProductStock(ctx, db.DecrementProductStockParams{
				ID:            item.ProductID,
				StockQuantity: item.Quantity,
			})
			if err != nil {
				return fmt.Errorf("decrement stock: %w", err)
			}
			if n == 0 {
				return fmt.Errorf("product %s: %w", item.ProductID, ErrInsufficientStock)
			}
		}

		paid, err := q.UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
			ID:     order.ID,
			Status: "paid",
		})
		if err != nil {
			return fmt.Errorf("mark order paid: %w", err)
		}
		updated = paid
		return nil
	})

	return updated, err
}

// AttachPaymentIntent records the Stripe PaymentIntent ID on the order so the
// webhook can find it later. Thin wrapper so controllers don't have to build
// db.SetOrderPaymentIntentParams themselves.
//
// NAMING NOTE: this Store method is deliberately NOT called
// SetOrderPaymentIntent — the embedded *db.Queries already has a method with
// that name, and a Store method with the same name would SHADOW it
// (the Store version wins). Shadowing is legal but confusing; distinct names
// keep the mental model simple. You can still call the raw query directly via
// s.Queries.SetOrderPaymentIntent(...) if you ever need it.
//
// SUGGESTION: fold this into the same transaction that creates the order if
// you move PaymentIntent creation before order insert (advanced: see Stripe's
// "orders + confirm flow" docs).
func (s *Store) AttachPaymentIntent(ctx context.Context, orderID uuid.UUID, paymentIntentID string) (db.Order, error) {
	return s.Queries.SetOrderPaymentIntent(ctx, db.SetOrderPaymentIntentParams{
		ID: orderID,
		StripePaymentIntentID: sql.NullString{
			String: paymentIntentID,
			Valid:  true,
		},
	})
}
