.DEFAULT_GOAL := help
CONTROL_PLANE := control-plane
DASHBOARD := dashboard
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

.PHONY: bootstrap
bootstrap: ## Create the first account and print its API token (EMAIL=... NAME=...)
	@test -n "$(EMAIL)" || (echo "usage: make bootstrap EMAIL=you@example.com NAME='Your Name'" && exit 1)
	cd $(CONTROL_PLANE) && LAUNCHPAD_DATABASE_URL="$(DATABASE_URL)" \
		go run ./cmd/bootstrap -email "$(EMAIL)" -name "$(NAME)"

.PHONY: build
build: ## Compile the control-plane binaries into bin/
	cd $(CONTROL_PLANE) && go build -o ../bin/launchpad-api ./cmd/api
	cd $(CONTROL_PLANE) && go build -o ../bin/launchpad-bootstrap ./cmd/bootstrap

.PHONY: test
test: ## Run unit tests (database integration tests are skipped)
	cd $(CONTROL_PLANE) && go test ./...

.PHONY: test-integration
test-integration: ## Run all tests including those that need a live database
	cd $(CONTROL_PLANE) && LAUNCHPAD_TEST_DATABASE_URL="$(DATABASE_URL)" go test ./... -count=1

# npm install rather than npm ci: it is a no-op when the tree is already current,
# so it costs nothing to depend on and means no target fails with a missing
# module on a fresh checkout.
.PHONY: dashboard-install
dashboard-install:
	cd $(DASHBOARD) && npm install --no-audit --no-fund

.PHONY: dashboard
dashboard: dashboard-install ## Serve the dashboard on :5173, proxying /v1 to the control plane
	cd $(DASHBOARD) && npm run dev

.PHONY: dashboard-build
dashboard-build: dashboard-install ## Type-check the dashboard and bundle it into dashboard/dist
	cd $(DASHBOARD) && npm run build

.PHONY: dashboard-test
dashboard-test: dashboard-install ## Run the dashboard tests
	cd $(DASHBOARD) && npm test

.PHONY: api-client
api-client: dashboard-install ## Regenerate the dashboard's API types from the OpenAPI spec
	cd $(DASHBOARD) && npm run generate:api

.PHONY: api-client-check
api-client-check: dashboard-install ## Fail if the generated API types are behind the spec
	cd $(DASHBOARD) && npm run check:api

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
check: fmt vet test api-client-check dashboard-test ## Format, vet and test both halves — run this before committing
