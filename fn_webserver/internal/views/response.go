// Package views is the VIEW layer: everything the API puts ON THE WIRE.
//
// DIDACTIC — MVC rule enforced here:
//   Controllers NEVER call c.JSON directly and NEVER serialize db.* models.
//   They call views.RenderXxx(...) with a *View DTO from this package.
//   Why? One place controls the wire shape; DB refactors can't silently
//   change your public API; and sensitive columns (password_hash,
//   refresh_token_hash) are stripped by construction — a DTO simply has no
//   field for them.
//
// THE ENVELOPE every endpoint returns:
//   { "success": true,  "data": ...,        "meta": ... }
//   { "success": false, "error": {code, message} }
package views

import (
	"github.com/gin-gonic/gin"
)

// APIResponse is the single JSON envelope. Pointer to Error so it's omitted
// (omitempty) on success — one struct, two shapes.
type APIResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   *ErrorBody  `json:"error,omitempty"`
	Meta    interface{} `json:"meta,omitempty"`
}

// ErrorBody is the machine-readable + human-readable error pair.
// SUGGESTION: add a "details" field later for per-field validation errors
// ({"field": "email", "issue": "invalid format"}).
type ErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RenderSuccess → 200 with data.
func RenderSuccess(c *gin.Context, data interface{}) {
	c.JSON(200, APIResponse{Success: true, Data: data})
}

// RenderCreated → 201 for successful resource creation (register, checkout).
func RenderCreated(c *gin.Context, data interface{}) {
	c.JSON(201, APIResponse{Success: true, Data: data})
}

// RenderError → any status with the error envelope.
// NOTE: we accept arbitrary codes because controllers map domain errors
// (ErrInsufficientStock → 409, stripe failure → 502, ...).
func RenderError(c *gin.Context, code int, message string) {
	c.JSON(code, APIResponse{Success: false, Error: &ErrorBody{Code: code, Message: message}})
}

// RenderPaginated → 200 with data + pagination meta.
//
// SUGGESTION: make Meta a typed struct (Page, Limit, Total, TotalPages) once
// you implement cursor or offset pagination UI. Total is passed by the
// controller via CountProducts (an extra COUNT query — consider caching or
// SQL window functions if it ever hurts).
func RenderPaginated(c *gin.Context, data interface{}, meta interface{}) {
	c.JSON(200, APIResponse{Success: true, Data: data, Meta: meta})
}
