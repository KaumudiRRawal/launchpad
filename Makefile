.DEFAULT_GOAL := help
CONTROL_PLANE := control-plane
DATABASE_URL ?= postgres://launchpad:launchpad@localhost:5432/launchpad?sslmode=disable

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: db-up
db-up: ## Start the local PostgreSQL container and wait for it to accept connections
	docker compose up -d --wait postgres

.PHONY: db-down
db-down: ## Stop the local PostgreSQL container (data is preserved)
	docker compose down

.PHONY: db-reset
db-reset: ## Destroy the local database including its volume, then start fresh
	docker compose down -v
	$(MAKE) db-up

.PHONY: run
run: ## Run the control plane against the local database
	cd $(CONTROL_PLANE) && LAUNCHPAD_DATABASE_URL="$(DATABASE_URL)" \
		LAUNCHPAD_LOG_LEVEL=debug go run ./cmd/api

.PHONY: build
build: ## Compile the control-plane binary into bin/
	cd $(CONTROL_PLANE) && go build -o ../bin/launchpad-api ./cmd/api

.PHONY: test
test: ## Run unit tests (database integration tests are skipped)
	cd $(CONTROL_PLANE) && go test ./...

.PHONY: test-integration
test-integration: ## Run all tests including those that need a live database
	cd $(CONTROL_PLANE) && LAUNCHPAD_TEST_DATABASE_URL="$(DATABASE_URL)" go test ./... -count=1

.PHONY: vet
vet: ## Run go vet
	cd $(CONTROL_PLANE) && go vet ./...

.PHONY: fmt
fmt: ## Format all Go source
	cd $(CONTROL_PLANE) && go fmt ./...

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	cd $(CONTROL_PLANE) && go mod tidy

.PHONY: check
check: fmt vet test ## Format, vet and test — run this before committing
