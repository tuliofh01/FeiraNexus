package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
)

// CreateProductRequest is POST /api/v1/products (admin only).
//
// MONEY FIELD: price_cents is an INTEGER of minor units (1299 = $12.99).
// DIDACTIC — never use float64 for money: 0.1 + 0.2 != 0.3 in IEEE-754,
// and rounding bugs silently lose cents. Integer cents are exact and what
// Stripe itself expects (its Amount field is an int64 of minor units).
type CreateProductRequest struct {
	Name          string `json:"name" binding:"required,min=1,max=200"`
	Description   string `json:"description" binding:"max=2000"`
	PriceCents    int64  `json:"price_cents" binding:"required,gt=0"` // > 0 enforced here AND by DB CHECK
	Currency      string `json:"currency" binding:"omitempty,len=3"` // 3-letter ISO 4217
	StockQuantity int32  `json:"stock_quantity" binding:"required,gte=0"`
	IsActive      *bool  `json:"is_active,omitempty"` // pointer = "optional bool"; nil → default true
}

// ProductView is the public product shape.
type ProductView struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	PriceCents    int64     `json:"price_cents"`
	Currency      string    `json:"currency"`
	Price         string    `json:"price"` // human-readable "USD 12.99"
	StockQuantity int32     `json:"stock_quantity"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

// ToProductView maps db.Product → ProductView and formats the display price.
func ToProductView(p db.Product) ProductView {
	return ProductView{
		ID:            p.ID,
		Name:          p.Name,
		Description:   p.Description.String,
		PriceCents:    p.PriceCents,
		Currency:      p.Currency,
		Price:         FormatCents(p.PriceCents, p.Currency),
		StockQuantity: p.StockQuantity,
		IsActive:      p.IsActive,
		CreatedAt:     p.CreatedAt,
	}
}

// ToProductViews maps a slice (keep mappings in ONE place so every endpoint
// serializes identically).
func ToProductViews(ps []db.Product) []ProductView {
	out := make([]ProductView, 0, len(ps))
	for _, p := range ps {
		out = append(out, ToProductView(p))
	}
	return out
}

// FormatCents renders integer minor units as "USD 12.99".
//
// SUGGESTION: for full i18n use golang.org/x/text/message (plural rules,
// locale-aware separators like 1.234,56 in pt-BR) or github.com/leekchan/accounting.
// This simple version is exact: integer division avoids float drift.
func FormatCents(cents int64, currency string) string {
	// Java-style Math: 1299 / 100 = 12, abs(1299 % 100) = 99 → "12.99".
	// (We assume 2-decimal currencies — true for USD/EUR/BRL; JPY has 0 —
	// another reason to move to golang.org/x/text when you go international.)
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	major := cents / 100
	minor := cents % 100
	return fmt.Sprintf("%s %s%d.%02d", sign, strings.ToUpper(currency), major, minor)
}
