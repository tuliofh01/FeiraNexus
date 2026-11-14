package controllers

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models/db"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/views"
)

// ProductController serves the catalog: public reads + admin creation.
type ProductController struct {
	store  *models.Store
	logger *slog.Logger
}

func NewProductController(store *models.Store, logger *slog.Logger) *ProductController {
	return &ProductController{store: store, logger: logger}
}

// List returns paginated ACTIVE products.
// GET /api/v1/products?limit=20&offset=0
func (pc *ProductController) List(c *gin.Context) {
	// Parse + clamp pagination from the QUERY STRING.
	// SUGGESTION: switch to cursor pagination (created_at, id) when data
	// grows — OFFSET gets slower as pages go deeper.
	limit := queryInt(c, "limit", 20, 1, 100)
	offset := queryInt(c, "offset", 0, 0, 1_000_000)

	products, err := pc.store.ListProducts(c.Request.Context(), db.ListProductsParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		pc.logger.Error("products: list", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	// Total powers "page X of Y" in the UI (one cheap COUNT query).
	total, err := pc.store.CountProducts(c.Request.Context())
	if err != nil {
		pc.logger.Error("products: count", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	views.RenderPaginated(c, views.ToProductViews(products), gin.H{
		"limit":  limit,
		"offset": offset,
		"total":  total,
	})
	// TODO(you): ?include_inactive=true gated by RequireAdmin for an admin
	// catalog screen (use ListProductsAdmin).
}

// Get returns one product by UUID.
// GET /api/v1/products/:id
func (pc *ProductController) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		views.RenderError(c, 400, "invalid product id")
		return
	}

	product, err := pc.store.GetProduct(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			views.RenderError(c, 404, "product not found")
			return
		}
		pc.logger.Error("products: get", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}
	// SUGGESTION: hide inactive products from non-admins →
	// if !product.IsActive && c.GetString(middleware.CtxRole) != "admin" { 404 }

	views.RenderSuccess(c, views.ToProductView(product))
}

// Create adds a product (admin only — route guards it with RequireAdmin).
// POST /api/v1/products
func (pc *ProductController) Create(c *gin.Context) {
	var req views.CreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		views.RenderError(c, 400, "invalid request: "+err.Error())
		return
	}

	// Optional fields → explicit defaults.
	currency := req.Currency
	if currency == "" {
		currency = "usd"
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive // pointer indirection distinguishes
	}                           // "absent" from "explicitly false"

	product, err := pc.store.CreateProduct(c.Request.Context(), db.CreateProductParams{
		Name:          req.Name,
		Description:   sql.NullString{String: req.Description, Valid: req.Description != ""},
		PriceCents:    req.PriceCents,
		Currency:      currency,
		StockQuantity: req.StockQuantity,
		IsActive:      isActive,
	})
	if err != nil {
		pc.logger.Error("products: create", "err", err)
		views.RenderError(c, 500, "internal error")
		return
	}

	views.RenderCreated(c, views.ToProductView(product))
}

// queryInt reads ?name= with default + min/max clamping (never trust input:
// limit=999999 would otherwise force a huge query).
func queryInt(c *gin.Context, name string, def, min, max int) int {
	raw := c.Query(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
