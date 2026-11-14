# FeiraNexus — E-Commerce REST API (`fn_webserver`)

Go + Gin MVC backend for the FeiraNexus e-commerce platform. Talks to **PostgreSQL 17** via `pgx/v5` + `pgxpool`, with compile-safe SQL through **sqlc**. Serves checkout via **Stripe PaymentIntents**, asynchronous jobs via a buffered channel worker pool, and ships on **DigitalOcean App Platform**.

> Monorepo layout: this service lives at `fn_webserver/` inside `github.com/tuliofh01/FeiraNexus`. Root contains CI (`.github/workflows/ci-deploy.yml`) and the DO app spec (`.do/app.yaml`).

---

## Stack

| Layer | Technology | Notes |
|-------|-----------|-------|
| Language | **Go 1.26** | |
| Web framework | **Gin** | Routing, binding, middleware |
| Database | **PostgreSQL 17** | Managed PG on DigitalOcean; local via docker-compose |
| Driver / pool | **pgx/v5 + pgxpool** | Type-safe, high-performance Postgres access |
| Codegen | **sqlc v2** | `sqlc.yaml` → `internal/models/db` (pgx/v5, JSON tags, `uuid.UUID`) |
| Auth | **Argon2id** + **JWT (HMAC-SHA256, 15 min)** + `crypto/rand` refresh tokens | SHA-256 digest stored for refresh tokens |
| Encryption | **AES-256-GCM** | 12-byte random nonce, 256-bit key from env |
| Payments | **Stripe v81** | PaymentIntents (idempotent), webhook signature verification |
| Concurrency | **golang.org/x/sync/errgroup**, buffered channel worker pool | cap 256 jobs, 4 workers, graceful drain |
| CI/CD | **GitHub Actions** → **DigitalOcean App Platform** | `go vet`, `go test -race`, then deploy |
| Container | **Docker** multi-stage | `golang:1.25-alpine` → `alpine:3.21`, non-root `appuser`, `/healthcheck` |

---

## Architecture — MVC

```
internal/
├── models/          # MODEL — schema, sqlc queries, Store + ACID transactions
│   ├── migrations/001_init.sql
│   ├── queries/{users,sessions,products,orders}.sql
│   ├── db/          # sqlc-generated type-safe models & queries
│   └── store.go     # Store wraps *db.Queries + *pgxpool.Pool (ExecTx)
├── views/           # VIEW — JSON envelopes, request/response DTOs
│   ├── response.go          # APIResponse{Success,Data,Error,Meta} + Render helpers
│   ├── auth_view.go         # UserView, SessionView, register/login DTOs
│   ├── product_view.go      # ProductView, currency/cents formatters
│   └── order_view.go        # CheckoutRequest, CheckoutResponseView
└── controllers/     # CONTROLLER — orchestration, business logic
    ├── router.go               # route + middleware registration
    ├── auth_controller.go      # register, login, refresh, logout, sessions
    ├── product_controller.go   # list, get, create (admin)
    ├── checkout_controller.go  # errgroup validation → ExecTx → PaymentIntent
    ├── webhook_controller.go   # raw-body signature verify → worker dispatch → 200
    └── health_controller.go    # /healthz DB ping + worker queue telemetry
```

Cross-cutting:

- `internal/config/` — environment loading.
- `internal/crypto/` — `password.go` (Argon2id), `token.go` (JWT + refresh), `aes.go` (AES-256-GCM).
- `internal/worker/` — `dispatcher.go`: `jobChan chan Job` (cap 256), 4 goroutines, `sync.WaitGroup` drain on SIGINT/SIGTERM, 30-minute session janitor ticker.
- `internal/middleware/` — `auth.go` (JWT auth + admin guard), `ratelimit.go` (token bucket).

### Data flow (checkout)

1. `POST /api/v1/checkout` → `CheckoutController`.
2. `errgroup` concurrently validates the user and product stock against the Model layer.
3. `store.ExecTx` creates the `pending` order + items with `SELECT … FOR UPDATE` stock locks.
4. Stripe `PaymentIntent` created with `IdempotencyKey = order.ID.String()`.
5. `CheckoutResponseView` (with `client_secret`) rendered by the View layer.

### Data flow (stripe webhook)

1. `POST /api/v1/webhooks/stripe` reads the raw body, verifies `Stripe-Signature` with `webhook.ConstructEvent`.
2. Enqueues `JobWebhookProcess` on the worker channel and immediately returns `200 OK`.
3. Worker runs `store.ExecTx` to mark the order `paid` and decrement inventory atomically.

---

## API reference

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/auth/register` | — | Register (argon2id hash) |
| POST | `/api/v1/auth/login` | — | Issue 15-min JWT + refresh token |
| POST | `/api/v1/auth/refresh` | refresh token | Rotate refresh token (reuse → 401 + `JobRevokeCompromisedUser`) |
| POST | `/api/v1/auth/logout` | JWT | Revoke session |
| GET | `/api/v1/auth/sessions` | JWT | List active sessions |
| DELETE | `/api/v1/auth/sessions/:id` | JWT | Revoke one session |
| GET | `/api/v1/products` | — | List products |
| GET | `/api/v1/products/:id` | — | Product detail |
| POST | `/api/v1/products` | JWT + admin | Create product |
| POST | `/api/v1/checkout` | JWT | Create order + PaymentIntent |
| POST | `/api/v1/webhooks/stripe` | Stripe sig. | Process payment webhook |
| GET | `/healthz` | — | DB ping + worker queue depth |

---

## Local setup

```bash
# 1. Generate .env (CSPRNG JWT_SECRET + AES_MASTER_KEY_HEX); --force to overwrite
./scripts/gen_env.sh

# 2. Dependencies
go mod tidy

# 3. Postgres (healthcheck-gated)
make docker-up

# 4. Apply schema
make migrate

# 5. Regenerate sqlc code (optional; db/ is checked in)
make sqlc

# 6. Run
make run
```

### Makefile targets

| Target | Command |
|--------|---------|
| `make env` | `./scripts/gen_env.sh` |
| `make sqlc` | `sqlc generate` |
| `make docker-up` | `docker compose up -d postgres` + healthcheck wait |
| `make docker-down` | `docker compose down` |
| `make migrate` | Apply `migrations/001_init.sql` |
| `make run` | `go run ./cmd/api/main.go` |
| `make test` | `go test -v -race ./...` |
| `make build` | `go build -o bin/fn_webserver ./cmd/api/main.go` |

---

## Deployment

**DigitalOcean App Platform** (`.do/app.yaml`): app `feiranexus-api`, `source_dir: /fn_webserver`, `dockerfile_path: fn_webserver/Dockerfile`, `/healthz` health check, env bindings, Managed PostgreSQL 17 (`feiranexus-db`), `deploy_on_push: true` on `main`.

**GitHub Actions** (`.github/workflows/ci-deploy.yml`): `defaults.run.working-directory: ./fn_webserver` → `go vet ./...` → `go test -v -race ./...` → DigitalOcean deploy (on push to `main`).

**Docker**: multi-stage, distroless-style Alpine runtime, non-root `appuser`, `HEALTHCHECK` on `/healthz`.

---

## UML documentation

PlantUML diagrams live in [`docs/`](docs/):

- [`docs/class_diagram.puml`](docs/class_diagram.puml) — MVC classes and dependencies.
- [`docs/db_diagram.puml`](docs/db_diagram.puml) — ERD for the five tables.
- [`docs/checkout_activity.puml`](docs/checkout_activity.puml) — checkout flow (errgroup, tx, PaymentIntent).
- [`docs/webhook_activity.puml`](docs/webhook_activity.puml) — webhook verification and async processing.
- [`docs/use_case_diagram.puml`](docs/use_case_diagram.puml) — actors (Customer, Admin, Stripe) and use cases.

Render with the PlantUML CLI or any PlantUML-enabled editor:

```bash
plantuml docs/*.puml
```
