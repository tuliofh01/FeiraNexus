APP_NAME := fn_webserver
CMD := ./cmd/api/main.go
BIN_DIR := bin

.PHONY: env sqlc docker-up docker-down migrate run test build clean help

## env: Generate .env with CSPRNG secrets (use --force to overwrite)
env:
	@./scripts/gen_env.sh

## sqlc: Generate type-safe Go code from SQL
sqlc:
	sqlc generate

## docker-up: Start PostgreSQL and wait for healthcheck
docker-up:
	docker compose up -d postgres
	@echo "Waiting for PostgreSQL healthcheck..."
	@until [ "$$(docker inspect --format='{{.State.Health.Status}}' fn_webserver-postgres-1 2>/dev/null || echo missing)" = "healthy" ]; do \
		printf "."; sleep 2; \
	done; \
	echo " PostgreSQL is healthy."

## docker-down: Stop all containers
docker-down:
	docker compose down

## migrate: Apply SQL migrations to the local database
migrate:
	@echo "Applying migrations..."
	@docker compose exec -T postgres psql -U feiranexus -d feiranexus_db < internal/models/migrations/001_init.sql
	@echo "Migrations applied."

## run: Run the API server locally
run:
	go run $(CMD)

## test: Run tests with race detector
test:
	go test -v -race ./...

## build: Build the API binary
build:
	go build -o $(BIN_DIR)/$(APP_NAME) $(CMD)

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## help: Show this help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':' || true
