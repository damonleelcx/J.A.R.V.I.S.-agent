# FORGE — developer and CI entry points.
#
# Every repeatable action lives here rather than in a one-off shell snippet, so
# that CI, a new contributor, and an operator at 3am all run the same commands.

# GOWORK=off: this repository is often checked out inside a parent Go workspace
# (a go.work higher up the tree). Without this, `go build ./...` fails with
# "directory is contained in a module that is not one of the workspace modules".
# Setting it here makes the repo build identically inside or outside a workspace.
export GOWORK := off

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

BINDIR      := bin
PKG         := github.com/damonleelcx/J.A.R.V.I.S.-agent
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

# Local development database. Runs on a non-default port so it cannot collide
# with another Postgres already on this machine.
DB_CONTAINER := forge-pg
DB_PORT      ?= 55840
DB_USER      ?= forge
DB_PASS      ?= forge_dev_pw
DB_NAME      ?= forge
DB_URL       := postgres://$(DB_USER):$(DB_PASS)@localhost:$(DB_PORT)/$(DB_NAME)?sslmode=disable

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show this help
	@echo "FORGE — make targets"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Database URL: $(DB_URL)"

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Build all binaries into ./bin
	@mkdir -p $(BINDIR)
	@# Binaries are added as their phase lands, so `make build` never claims to
	@# produce something that does not exist yet.
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/forged        ./cmd/forged
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/forge-worker ./cmd/forge-worker
	go build -ldflags "$(LDFLAGS)" -o $(BINDIR)/forgectl ./cmd/forgectl
	@echo "built $(VERSION) ($(COMMIT)) into $(BINDIR)/"

.PHONY: clean
clean: ## Remove build output and local runtime state
	rm -rf $(BINDIR) dist .forge

# ---------------------------------------------------------------------------
# Quality gates
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: fmt-check
fmt-check: ## Fail if any file is unformatted
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then \
	  echo "These files are not gofmt-formatted:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: ## Run unit tests (no database required)
	go test -count=1 -race ./...

.PHONY: test-integration
test-integration: db-wait ## Run all tests including those needing live Postgres
	FORGE_TEST_DATABASE_URL="$(DB_URL)" go test -count=1 -race ./...

.PHONY: test-cover
test-cover: db-wait ## Run tests with coverage and print a summary
	FORGE_TEST_DATABASE_URL="$(DB_URL)" go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -20

.PHONY: check
check: fmt-check vet test-integration ## Everything CI runs

# ---------------------------------------------------------------------------
# Local database
# ---------------------------------------------------------------------------

.PHONY: db-up
db-up: ## Start the local Postgres container
	@if docker ps -a --format '{{.Names}}' | grep -qx '$(DB_CONTAINER)'; then \
	  docker start $(DB_CONTAINER) >/dev/null && echo "started existing $(DB_CONTAINER)"; \
	else \
	  docker run -d --name $(DB_CONTAINER) -p $(DB_PORT):5432 \
	    -e POSTGRES_USER=$(DB_USER) -e POSTGRES_PASSWORD=$(DB_PASS) -e POSTGRES_DB=$(DB_NAME) \
	    postgres:17-alpine >/dev/null && echo "created $(DB_CONTAINER) on port $(DB_PORT)"; \
	fi

.PHONY: db-wait
db-wait: ## Block until the database accepts connections
	@if ! docker info >/dev/null 2>&1; then \
	  echo "Docker is not reachable."; \
	  echo "  cause : the Docker daemon is not running, or DOCKER_HOST points at a profile that is down."; \
	  echo "  fix   : start Docker (or 'colima start'), then re-run. Current DOCKER_HOST=$${DOCKER_HOST:-<unset, using default socket>}"; \
	  exit 1; \
	fi
	@if ! docker ps -a --format '{{.Names}}' | grep -qx '$(DB_CONTAINER)'; then \
	  echo "No container named '$(DB_CONTAINER)' exists."; \
	  echo "  fix   : run 'make db-up' to create it."; \
	  exit 1; \
	fi
	@if ! docker ps --format '{{.Names}}' | grep -qx '$(DB_CONTAINER)'; then \
	  echo "Container '$(DB_CONTAINER)' exists but is not running."; \
	  echo "  fix   : run 'make db-up' to start it."; \
	  exit 1; \
	fi
	@for i in $$(seq 1 30); do \
	  if docker exec $(DB_CONTAINER) pg_isready -U $(DB_USER) >/dev/null 2>&1; then exit 0; fi; \
	  sleep 1; \
	done; \
	echo "Container '$(DB_CONTAINER)' is running but Postgres did not accept connections within 30s."; \
	echo "  fix   : inspect startup errors with 'docker logs $(DB_CONTAINER)'"; \
	exit 1

.PHONY: db-down
db-down: ## Stop the local Postgres container (data is preserved)
	-docker stop $(DB_CONTAINER)

.PHONY: db-reset
db-reset: ## Destroy and recreate the local database. DESTRUCTIVE.
	@read -p "This deletes all local FORGE data. Type 'yes' to continue: " ok; \
	 [ "$$ok" = "yes" ] || { echo "aborted"; exit 1; }
	-docker rm -f $(DB_CONTAINER)
	$(MAKE) db-up db-wait migrate

.PHONY: db-shell
db-shell: ## Open psql against the local database
	docker exec -it $(DB_CONTAINER) psql -U $(DB_USER) -d $(DB_NAME)

# ---------------------------------------------------------------------------
# Operations
# ---------------------------------------------------------------------------

.PHONY: migrate
migrate: ## Apply the migration chain (idempotent)
	FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl migrate

.PHONY: migrate-dry-run
migrate-dry-run: ## List migrations without applying them
	FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl migrate --dry-run

.PHONY: health
health: ## Check database connectivity
	FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl health

.PHONY: run
run: db-wait ## Run the API server against the local database
	go run ./cmd/forged

.PHONY: work
work: db-wait ## Run the agent workers against the local database
	go run ./cmd/forge-worker

.PHONY: outbox
outbox: ## List messages the development mail transport has written
	@ls -lt .forge/outbox 2>/dev/null | head -20 || echo "no outbox yet — sign up first"

.PHONY: config-print
config-print: ## Print effective configuration with secrets redacted
	go run ./cmd/forgectl config
