package controllers

import (
	"encoding/json"
	"io"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/views"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/worker"
)

// WebhookController receives Stripe events.
//
// SECURITY MODEL: Stripe → us. We verify authenticity with an HMAC-SHA256
// signature over the RAW BODY (Stripe-Signature header). Anyone who can't
// produce a valid signature gets a 400. This replaces JWT auth for this route.
//
// PERFORMANCE MODEL: verify → enqueue → 200 OK (milliseconds). The heavy DB
// work happens on a worker. Fast acks prevent Stripe timeout-retries.
type WebhookController struct {
	disp          *worker.Dispatcher
	webhookSecret string
	logger        *slog.Logger
}

func NewWebhookController(disp *worker.Dispatcher, webhookSecret string, logger *slog.Logger) *WebhookController {
	return &WebhookController{disp: disp, webhookSecret: webhookSecret, logger: logger}
}

// StripeWebhook handles POST /api/v1/webhooks/stripe.
func (wc *WebhookController) StripeWebhook(c *gin.Context) {
	// 1) RAW BYTES. DIDACTIC: signature verification hashes the exact bytes
	// Stripe sent. If any middleware had parsed/marshaled the body first
	// (JSON round-trip changes whitespace/key order), the HMAC would mismatch.
	// That's why this route has NO body-consuming middleware and we read raw.
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		views.RenderError(c, 400, "unable to read body")
		return
	}
	// SUGGESTION: cap the body size first to prevent memory abuse:
	//   c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)

	// 2) Signature header (format: t=...,v1=...).
	signature := c.GetHeader("Stripe-Signature")
	if signature == "" {
		views.RenderError(c, 400, "missing Stripe-Signature header")
		return
	}
	if wc.webhookSecret == "" || wc.webhookSecret == "whsec_placeholder" {
		wc.logger.Error("webhook: STRIPE_WEBHOOK_SECRET not configured")
		views.RenderError(c, 503, "webhook not configured")
		return
	}

	// 3) Verify HMAC + timestamp (replay window) in one call.
	//    Payload is only TRUSTED after this returns nil.
	event, err := webhook.ConstructEvent(payload, signature, wc.webhookSecret)
	if err != nil {
		// Do not echo verification internals to the caller — log server-side.
		wc.logger.Warn("webhook: signature verification failed", "err", err)
		views.RenderError(c, 400, "invalid signature")
		return
	}

	switch string(event.Type) {
	case "payment_intent.succeeded":
		piID, err := paymentIntentID(event)
		if err != nil || piID == "" {
			wc.logger.Error("webhook: missing payment_intent id", "err", err)
			views.RenderError(c, 400, "malformed event payload")
			return
		}

		// Enqueue → worker marks order paid + decrements stock (ACID).
		if err := wc.disp.Enqueue(worker.Job{
			Type: worker.JobWebhookProcess,
			Payload: map[string]string{
				"payment_intent_id": piID,
				"event_id":          string(event.ID),
			},
		}); err != nil {
			// Queue full/closed → tell Stripe we FAILED so it retries later.
			// Losing a "paid" notification is much worse than a 500 here.
			wc.logger.Error("webhook: enqueue failed", "err", err, "payment_intent", piID)
			views.RenderError(c, 500, "temporary failure, retry later")
			return
		}
		wc.logger.Info("webhook: queued payment_intent.succeeded", "payment_intent", piID)
		views.RenderSuccess(c, gin.H{"queued": true})

	case "payment_intent.payment_failed":
		// TODO(you): enqueue a job to mark the order "failed" so users see it
		// (mirror handleWebhookProcess: find order by PI id → UpdateOrderStatus).
		wc.logger.Info("webhook: payment_intent.payment_failed received (no-op)")
		views.RenderSuccess(c, gin.H{"queued": false, "note": "payment_failed handler not implemented"})

	default:
		// MUST ack unknown event types with 200 — otherwise Stripe retries
		// them forever for events we simply don't care about (e.g. "created").
		views.RenderSuccess(c, gin.H{"queued": false})
	}
}

// paymentIntentID extracts the top-level id from event.Data.Object without
// caring about the exact Go type of Object (defensive JSON round-trip).
func paymentIntentID(event stripe.Event) (string, error) {
	raw, err := json.Marshal(event.Data.Object)
	if err != nil {
		return "", err
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", err
	}
	return obj.ID, nil
}
