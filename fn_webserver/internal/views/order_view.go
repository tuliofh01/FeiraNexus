package views

import (
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Checkout (POST /api/v1/checkout)
// ---------------------------------------------------------------------------

// CheckoutItem is one line of the cart as the CLIENT sees it: what product,
// how many. NOTICE what's missing: no price. The server re-reads prices from
// the DB — see store.CreateOrderTx.
type CheckoutItem struct {
	ProductID string `json:"product_id" binding:"required,uuid"` // uuid tag = format check
	Quantity  int32  `json:"quantity" binding:"required,min=1,max=100"` // cap bulk abuse
}

// CheckoutRequest is the checkout payload.
// `dive` = "run each item's tags too" — without it, nested validation is SKIPPED
// (classic Gin gotcha; search "golang nested validation dive" if curious).
type CheckoutRequest struct {
	Items []CheckoutItem `json:"items" binding:"required,min=1,dive"`
}

// CheckoutResponseView is what the client needs to finish payment in the
// browser/app with Stripe.js or a mobile SDK:
//   1. call confirmPayment with client_secret
//   2. webhook later flips order → paid (async source of truth)
type CheckoutResponseView struct {
	OrderID      uuid.UUID `json:"order_id"`
	Status       string    `json:"status"` // always "pending" at this point
	ClientSecret string    `json:"client_secret"` // Stripe.js needs this — secret to the payer, harmless
	TotalCents   int64     `json:"total_cents"`
	Currency     string    `json:"currency"`
	Total        string    `json:"total"` // "USD 49.98" for display
	CreatedAt    time.Time `json:"created_at"`
}

// NewCheckoutResponse builds the view from primitives the controller already has.
func NewCheckoutResponse(orderID uuid.UUID, clientSecret string, totalCents int64, currency string, createdAt time.Time) CheckoutResponseView {
	return CheckoutResponseView{
		OrderID:      orderID,
		Status:       "pending",
		ClientSecret: clientSecret,
		TotalCents:   totalCents,
		Currency:     currency,
		Total:        FormatCents(totalCents, currency),
		CreatedAt:    createdAt,
	}
}

// ---------------------------------------------------------------------------
// Order summaries (GET endpoints YOU will add later)
// ---------------------------------------------------------------------------

// OrderItemView is one line of an order for display.
type OrderItemView struct {
	ProductID      uuid.UUID `json:"product_id"`
	ProductName    string    `json:"product_name"` // filled by a JOIN or second query — TODO(you)
	Quantity       int32     `json:"quantity"`
	UnitPriceCents int64     `json:"unit_price_cents"`
	LineTotal      string    `json:"line_total"` // quantity × unit, formatted
}

// OrderView is an order summary (no payment secrets — client_secret is
// deliberately NOT included outside checkout).
type OrderView struct {
	ID         uuid.UUID       `json:"id"`
	Status     string          `json:"status"`
	TotalCents int64           `json:"total_cents"`
	Currency   string          `json:"currency"`
	Total      string          `json:"total"`
	Items      []OrderItemView `json:"items,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// TODO(you): ToOrderView(order db.Order, items []db.OrderItem, names map[uuid.UUID]string)
// then wire GET /api/v1/orders + GET /api/v1/orders/:id in the router.
// SUGGESTION: pass product names via one `SELECT ... WHERE id = ANY($1)` query
// instead of N+1 per-item lookups.
