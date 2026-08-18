# story-data — common development commands.
#
# Every target here is a thin wrapper around what the service guide already
# prescribes; nothing is available only through make. Run `make` for the list.
#
# Ports and credentials match docker-compose.yml. Override any of them on the
# command line, e.g. `make test DB_PORT=5432`.

DB_HOST ?= localhost
DB_PORT ?= 5433
DB_USER ?= postgres
DB_PASSWORD ?= postgres
DB_NAME ?= story_data

# The dev database the API reads and writes.
DATABASE_URL ?= postgres://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_NAME)?sslmode=disable

# The e2e suite connects here only to CREATE and DROP its own throwaway
# database (story_data_e2e_<pid>), so it points at the maintenance database and
# can never touch the dev one.
TEST_DATABASE_URL ?= postgres://$(DB_USER):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/postgres?sslmode=disable

PKG ?= ./...
# `make test RUN=Ballot` narrows to matching tests; `V=1` adds -v.
RUN ?=
V ?=
# Recursive assignment, not :=, so the target-specific PKG overrides below
# are in effect when this expands.
GOTEST = go test $(PKG) -count=1 $(if $(RUN),-run '$(RUN)') $(if $(V),-v)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@echo "story-data — make <target>"
	@echo
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Vars: DB_PORT=$(DB_PORT) PKG=$(PKG) RUN=<regex> V=1"

# ── running ─────────────────────────────────────────────────────────────────

.PHONY: db
db: ## Start only PostgreSQL (what the tests need)
	docker compose up -d postgres

.PHONY: up
up: ## Start PostgreSQL and the API in Docker (API on :8084)
	docker compose up --build -d
	@echo "API: http://localhost:8084/health"

.PHONY: down
down: ## Stop the Docker stack (keeps the data volume)
	docker compose down

.PHONY: logs
logs: ## Tail the API container logs
	docker compose logs -f api

.PHONY: run
run: ## Run the API from source against the dev database (:8080)
	DATABASE_URL='$(DATABASE_URL)' AUTH_MODE=dev \
	VOTER_MIN_PROFILE_AGE=0 RATE_LIMIT_READS_PER_MINUTE=0 RATE_LIMIT_WRITES_PER_MINUTE=0 \
	go run ./cmd/api

.PHONY: migrate
migrate: ## Apply outstanding migrations to the dev database
	DATABASE_URL='$(DATABASE_URL)' go run ./cmd/api migrate

.PHONY: psql
psql: ## Open a psql shell on the dev database
	PGPASSWORD='$(DB_PASSWORD)' psql -h $(DB_HOST) -p $(DB_PORT) -U $(DB_USER) -d $(DB_NAME)

.PHONY: health
health: ## Check the health endpoint of the Docker API
	curl -fsS http://localhost:8084/health && echo

# ── testing ─────────────────────────────────────────────────────────────────

.PHONY: test
test: ## Run every test (needs PostgreSQL: make db)
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' $(GOTEST)

.PHONY: test-e2e
test-e2e: PKG = ./internal/httpapi/e2e/
test-e2e: ## Run only the black-box HTTP suite
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' $(GOTEST)

.PHONY: test-unit
test-unit: PKG = ./cmd/... ./internal/auth/ ./internal/config/ ./internal/httpapi/
test-unit: ## Run only the tests that need no database
	$(GOTEST)

.PHONY: cover
cover: ## Run the suite and open the coverage report
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' \
	go test ./... -count=1 -coverpkg=./internal/...,./cmd/... -coverprofile=coverage.out
	go tool cover -html=coverage.out

# ── quality ─────────────────────────────────────────────────────────────────

.PHONY: fmt
fmt: ## Format Go code in place
	gofmt -w internal cmd

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: build
build: ## Compile everything
	go build ./...

.PHONY: check
check: ## Everything CI would reject a change for: format, vet, test
	@unformatted=$$(gofmt -l internal cmd); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd (run make fmt):"; echo "$$unformatted"; exit 1; \
	fi
	go vet ./...
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test ./... -count=1

.PHONY: tidy
tidy: ## Prune and verify go.mod / go.sum
	go mod tidy
	go mod verify

# ── housekeeping ────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## Drop e2e databases a crashed test run left behind
	@PGPASSWORD='$(DB_PASSWORD)' psql -h $(DB_HOST) -p $(DB_PORT) -U $(DB_USER) -d postgres -tAc \
		"SELECT datname FROM pg_database WHERE datname LIKE 'story_data_e2e_%'" \
	| while read -r db; do \
		[ -z "$$db" ] && continue; \
		echo "dropping $$db"; \
		PGPASSWORD='$(DB_PASSWORD)' psql -h $(DB_HOST) -p $(DB_PORT) -U $(DB_USER) -d postgres -q -c "DROP DATABASE \"$$db\""; \
	done
	rm -f coverage.out
