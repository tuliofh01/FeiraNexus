package controllers

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/paymentintent"
	"golang.org/x/sync/errgroup"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/middleware"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/views"
)

// CheckoutController orchestrates the money flow:
//
//	validate (errgroup, parallel) → order+items (ACID tx) → Stripe PaymentIntent
//	→ respond with client_secret → [user pays] → Stripe webhook → order paid.
//
// The webhook is the SOURCE OF TRUTH for "paid" — never mark paid from the
// browser: clients can lie (the classic "I paid!" forged request).
type CheckoutController struct {
	store  *models.Store
	logger *slog.Logger
}

func NewCheckoutController(store *models.Store, logger *slog.Logger) *CheckoutController {
	return &CheckoutController{store: store, logger: logger}
}

// Checkout handles POST /api/v1/checkout (authenticated users).
//
// CONCURRENCY NOTE (errgroup): validating the user and N products can happen
// in parallel — for 1 product it saves nothing, for 50 it's ~50× fewer
// round-trip latencies added together. errgroup also gives us:
//   - first error wins (g.Wait returns it, context auto-cancels siblings)
//   - no data races (each goroutine writes its OWN variable; g.Wait
//     establishes happens-before before we read them)
func (cc *CheckoutController) Checkout(c *gin.Context) {
	uid, err := uuid.Parse(c.GetString(middleware.CtxUserID))
	if err != nil {
		views.RenderError(c, 401, "unauthenticated")
		return
	}

	var req views.CheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		views.RenderError(c, 400, "invalid request: "+err.Error())
		return
	}

	// Parse product IDs once (uuid validation already ran via binding tags).
	lines := make([]models.OrderLine, 0, len(req.Items))
	for _, item := range req.Items {
		pid, err := uuid.Parse(item.ProductID)
		if err != nil {
			views.RenderError(c, 400, "invalid product_id: "+item.ProductID)
			return
		}
		lines = append(lines, models.OrderLine{ProductID: pid, Quantity: item.Quantity})
	}

	ctx := c.Request.Context()
	g, gctx := errgroup.WithContext(ctx)

	var user db.User
	g.Go(func() error {
		u, err := cc.store.GetUserByID(gctx, uid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("user no longer exists") // → 400
			}
			return err // → 500 (real failure)
		}
		user = u
		return nil
	})

	var products []db.Product
	g.Go(func() error {
		for _, line := range lines {
			p, err := cc.store.GetProduct(gctx, line.ProductID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("product %s not found", line.ProductID)
				}
				return err
			}
			if !p.IsActive {
				return fmt.Errorf("product %s is unavailable", p.Name)
			}
			if p.StockQuantity < line.Quantity {
				// ErrInsufficientStock-style message → 409 below.
				return fmt.Errorf("insufficient stock for %s (have %d, want %d)",
					p.Name, p.StockQuantity, line.Quantity)
			}
			products = append(products, p)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		// Distinguish user-caused (4xx) from infrastructure (5xx) failures.
		if isUserError(err) {
			views.RenderError(c, 400, err.Error())
			return
		}
		cc.logger.Error("checkout: validation", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	_ = user // loaded & validated; kept for future use (tax, customer creation)

	// Currency from the first product (currency-locked carts: SUGGESTION —
	// reject mixed-currency carts explicitly if you add more currencies).
	currency := "usd"
	if len(products) > 0 {
		currency = products[0].Currency
	}

	// ACID: lock rows, re-check stock, price FROM THE DB, insert order+items.
	order, _, err := cc.store.CreateOrderTx(ctx, uid, currency, lines)
	if err != nil {
		if errors.Is(err, models.ErrInsufficientStock) {
			views.RenderError(c, 409, err.Error())
			return
		}
		cc.logger.Error("checkout: create order tx", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	// --- Stripe PaymentIntent -------------------------------------------------
	if stripe.Key == "" || stripe.Key == "sk_test_placeholder" {
		// Fail loudly instead of creating an un-payable order.
		cc.logger.Warn("checkout: stripe not configured")
		views.RenderError(c, 503, "payments are not configured")
		return
	}

	params := &stripe.PaymentIntentParams{
		Amount:      stripe.Int64(order.TotalCents), // int64 minor units
		Currency:    stripe.String(strings.ToLower(order.Currency)),
		Description: stripe.String(fmt.Sprintf("FeiraNexus order %s", order.ID)),
		Metadata:    map[string]string{"order_id": order.ID.String()}, // webhook reconciliation fallback
	}
	// IDEMPOTENCY: if our request times out and we retry (or Stripe retries),
	// the same key guarantees we don't create a SECOND PaymentIntent for one
	// order. Key = our order id (natural idempotency boundary).
	params.IdempotencyKey = stripe.String(order.ID.String())

	pi, err := paymentintent.New(params)
	if err != nil {
		cc.logger.Error("checkout: stripe paymentintent", "order_id", order.ID, "err", err)
		// Best-effort: don't leave an order stuck in "pending" forever.
		if _, uerr := cc.store.UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
			ID: order.ID, Status: "failed",
		}); uerr != nil {
			cc.logger.Error("checkout: mark failed", "err", uerr)
		}
		views.RenderError(c, 502, "payment provider error, please retry")
		return
	}

	// Persist PI id so the webhook can find this order (see webhook controller).
	if _, err := cc.store.AttachPaymentIntent(ctx, order.ID, pi.ID); err != nil {
		// Non-fatal: Metadata["order_id"] lets you reconcile manually —
		// but log loudly, this breaks automated webhook matching.
		cc.logger.Error("checkout: attach payment intent", "order_id", order.ID, "err", err)
	}

	views.RenderCreated(c, views.NewCheckoutResponse(
		order.ID, pi.ClientSecret, order.TotalCents, order.Currency, order.CreatedAt,
	))
}

// isUserError: errgroup validation messages are wrapped with fmt.Errorf and
// are client-fixable by construction in Checkout. Real infra errors we return
// early as err too, so we classify: any error that came from product/user
// validation loops is a 400.
//
// SUGGESTION (better design): define typed sentinel errors
// (ErrProductNotFound, ErrUnavailable, ErrStock) in the model layer and use
// errors.Is here — clearer than classifying by message origin.
func isUserError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "insufficient stock") ||
		strings.Contains(msg, "no longer exists")
}
