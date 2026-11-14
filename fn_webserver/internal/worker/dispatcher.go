// Package worker implements a buffered-channel job pool (producer/consumer).
//
// DIDACTIC — why async at all?
// Stripe webhook processing (mark order paid, decrement stock) can take 100ms+
// of DB work. Stripe expects a fast 200 OK; slow responses cause retries and
// timeouts. So the controller just ENQUEUES a job (microseconds) and returns;
// one of our 4 worker goroutines does the heavy lifting in the background.
//
// THE CONCURRENCY MODEL (this is the "complex part" — read carefully):
//
//	         Enqueue() ──► [ jobChan (buffered, cap 256) ] ──► worker×4
//	                          ▲ producers never block          │ each goroutine
//	                          │ unless FULL (we return error)  │ loops forever
//	                          │                                ▼
//	                     30-min janitor ticker ──────── DeleteExpiredSessions
//
//	Graceful shutdown: cancel(ctx) stops the janitor; Shutdown() closes the
//	channel (no more jobs), then waits (WaitGroup) until workers drain it.
//	Closing a channel you receive from is the idiomatic "poison pill" that
//	tells consumers "no more work will ever arrive" → their range loop exits.
//
// SUGGESTIONS when you outgrow this:
//   - Persistence: jobs die with the process. Move to a DB-backed queue or
//     pgmq/NATS/Redis Streams if losing jobs on deploy is unacceptable.
//   - Retries with backoff: wrap handlers with retry(n) before giving up.
//   - Metrics: expose QueueLen() + processed/error counters to Prometheus.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/tuliofh01/FeiraNexus/fn_webserver/internal/models"
)

// JobType enumerates known jobs.
type JobType string

const (
	// JobWebhookProcess: Stripe says "payment succeeded" → mark order paid
	// and decrement inventory in one ACID transaction.
	JobWebhookProcess JobType = "webhook_process"
	// JobRevokeCompromisedUser: a revoked refresh token was replayed →
	// assume theft and revoke EVERY session of that user.
	JobRevokeCompromisedUser JobType = "revoke_compromised_user"
	// JobAuditLog: fire-and-forget audit record (you implement the sink).
	JobAuditLog JobType = "audit_log"
)

// Job is one unit of async work.
// Payload is a flat string map on purpose: JSON-friendly, easy to log.
// SUGGESTION: switch to a typed payload per JobType (a small interface{})
// once payloads get more complex — compile-time safety beats string keys.
type Job struct {
	Type    JobType
	Payload map[string]string
}

// HandlerFunc processes one job. Handlers must be idempotent (Stripe
// may deliver the same event more than once).
type HandlerFunc func(ctx context.Context, job Job) error

var (
	ErrDispatcherClosed = errors.New("worker: dispatcher is closed")
	ErrQueueFull        = errors.New("worker: job queue is full (256)")
)

const (
	queueCapacity = 256
	numWorkers    = 4
	janitorEvery  = 30 * time.Minute
	// jobTimeout bounds each job's DB work so one bad job can't wedge a
	// worker forever.
	jobTimeout = 30 * time.Second
)

// Dispatcher owns the channel, the workers, and the janitor.
type Dispatcher struct {
	store    *models.Store
	jobChan  chan Job
	handlers map[JobType]HandlerFunc
	logger   *slog.Logger
	wg       sync.WaitGroup
	closed   atomic.Bool // safe Enqueue-after-close guard
}

// New wires default handlers. Call Start(ctx) to spawn goroutines.
func New(store *models.Store, logger *slog.Logger) *Dispatcher {
	d := &Dispatcher{
		store:    store,
		jobChan:  make(chan Job, queueCapacity),
		handlers: make(map[JobType]HandlerFunc),
		logger:   logger,
	}
	d.handlers[JobWebhookProcess] = d.handleWebhookProcess
	d.handlers[JobRevokeCompromisedUser] = d.handleRevokeCompromisedUser
	d.handlers[JobAuditLog] = d.handleAuditLog
	return d
}

// Register lets cmd/api (or tests) add/override handlers — extension point.
func (d *Dispatcher) Register(t JobType, h HandlerFunc) { d.handlers[t] = h }

// Start spawns numWorkers consumer goroutines + the session janitor.
// ctx cancellation stops ONLY the janitor; workers exit via Shutdown().
func (d *Dispatcher) Start(ctx context.Context) {
	for i := 0; i < numWorkers; i++ {
		d.wg.Add(1)
		go d.workerLoop()
	}
	d.wg.Add(1)
	go d.janitorLoop(ctx)
}

// Enqueue NEVER blocks the caller: if the buffer is full we return an error
// instead of stalling HTTP requests (backpressure by refusal).
//
// WHY 500 on webhook queue-full? See webhook controller: non-2xx makes Stripe
// retry later — the job isn't lost. That's intentional reliability design.
func (d *Dispatcher) Enqueue(job Job) error {
	if d.closed.Load() {
		return ErrDispatcherClosed
	}
	select {
	case d.jobChan <- job:
		return nil
	default:
		return fmt.Errorf("%w", ErrQueueFull)
	}
}

// QueueLen powers /healthz telemetry (how deep is the backlog?).
func (d *Dispatcher) QueueLen() int { return len(d.jobChan) }

// Shutdown drains and stops everything. Call AFTER canceling Start's ctx
// (otherwise the janitor never exits and Wait blocks forever).
func (d *Dispatcher) Shutdown() {
	if d.closed.CompareAndSwap(false, true) {
		close(d.jobChan) // poison pill: workers finish the buffer then exit
	}
	d.wg.Wait()
}

// workerLoop is the classic Go consumer: range until channel closes.
func (d *Dispatcher) workerLoop() {
	defer d.wg.Done()
	for job := range d.jobChan {
		h, ok := d.handlers[job.Type]
		if !ok {
			d.logger.Error("worker: no handler for job", "type", string(job.Type))
			continue
		}
		// DIDACTIC: jobs may outlive the request (and even the shutdown
		// signal) that created them. We derive a FRESH context with timeout
		// from Background so DB work can always complete during drain.
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		err := h(ctx, job)
		cancel() // always release the timer — govet's lostcancel checks this
		if err != nil {
			d.logger.Error("worker: job failed", "type", string(job.Type), "err", err)
		}
	}
}

// janitorLoop deletes expired sessions every 30 minutes (a poor-man's cron).
// Exits when ctx is canceled (first signal in graceful shutdown).
func (d *Dispatcher) janitorLoop(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(janitorEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.logger.Info("worker: janitor stopping")
			return
		case <-ticker.C:
			n, err := d.store.DeleteExpiredSessions(ctx)
			if err != nil {
				d.logger.Error("worker: janitor failed", "err", err)
				continue
			}
			if n > 0 {
				d.logger.Info("worker: deleted expired sessions", "count", n)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleWebhookProcess: the money path. ApplyPaymentTx is idempotent —
// duplicate Stripe deliveries won't double-charge inventory.
func (d *Dispatcher) handleWebhookProcess(ctx context.Context, job Job) error {
	piID := job.Payload["payment_intent_id"]
	if piID == "" {
		return errors.New("worker: payload missing payment_intent_id")
	}
	order, err := d.store.ApplyPaymentTx(ctx, piID)
	if err != nil {
		return fmt.Errorf("worker: applying payment %s: %w", piID, err)
	}
	d.logger.Info("worker: payment processed",
		"order_id", order.ID, "status", order.Status, "payment_intent", piID)
	return nil
}

// handleRevokeCompromisedUser: refresh-token replay detected in the auth
// controller → nuke every session for the user.
func (d *Dispatcher) handleRevokeCompromisedUser(ctx context.Context, job Job) error {
	uid, err := uuid.Parse(job.Payload["user_id"])
	if err != nil {
		return fmt.Errorf("worker: bad user_id payload: %w", err)
	}
	if err := d.store.RevokeAllUserSessions(ctx, uid); err != nil {
		return fmt.Errorf("worker: revoking sessions for %s: %w", uid, err)
	}
	d.logger.Warn("worker: all sessions revoked (possible token theft)", "user_id", uid)
	return nil
}

// handleAuditLog: placeholder sink. TODO(you): write to an audit_logs table
// (add the table in a new migration) or ship to your logging stack.
func (d *Dispatcher) handleAuditLog(_ context.Context, job Job) error {
	d.logger.Info("worker: audit event", "event", job.Payload["event"], "detail", job.Payload["detail"])
	return nil
}
