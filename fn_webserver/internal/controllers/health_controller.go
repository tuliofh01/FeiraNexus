package controllers

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/worker"
)

// HealthController answers operational probing.
//
// CONSUMERS of /healthz:
//   - Dockerfile HEALTHCHECK   (container restart on failure)
//   - DigitalOcean App Platform (route replacement / autoscaling)
//   - load balancers / uptime monitors
//
// It reports BOTH dependencies the app can't work without:
//   1. database reachable (Ping with a SHORT timeout — health checks must be fast)
//   2. worker queue depth (backlog telemetry: growing queue = stuck workers)
type HealthController struct {
	store *models.Store
	disp  *worker.Dispatcher
}

func NewHealthController(store *models.Store, disp *worker.Dispatcher) *HealthController {
	return &HealthController{store: store, disp: disp}
}

// Healthz handles GET /healthz.
//
// NOTE: plain JSON here (not the views.APIResponse envelope) on purpose —
// infra tools parse boring {"status":"ok"}; they don't know your envelope.
func (hc *HealthController) Healthz(c *gin.Context) {
	// Fresh context: don't inherit the client's request cancellation, but
	// never hang longer than 2s (probes expect quick answers).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
	defer cancel()

	dbOK := true
	var dbErr string
	if err := hc.store.Pool().Ping(ctx); err != nil {
		dbOK = false
		dbErr = err.Error()
	}

	status := "ok"
	code := 200
	if !dbOK {
		status = "degraded"
		code = 503 // non-2xx → Docker/DO mark the instance unhealthy
	}

	resp := gin.H{
		"status":      status,
		"db_ok":       dbOK,
		"queue_depth": hc.disp.QueueLen(), // jobs waiting = worker backlog
		"time":        time.Now().UTC().Format(time.RFC3339),
	}
	if dbErr != "" {
		resp["db_error"] = dbErr
	}
	c.JSON(code, resp)

	// SUGGESTION: promote queue_depth to a Prometheus gauge and expose
	// /metrics (github.com/gin-gonic/contrib/prometheus or promauto) —
	// dashboards > logs when 3am pages happen.
}
