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

.PHONY: test-asr
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

.PHONY: drill
test-asr: ## Transcription fences against the REAL speech provider (costs a fraction of a cent)
	@# These cannot be faked. The defect they guard — a model dropping decimal
	@# points out of engineering speech — is a property of the provider, not of
	@# this code, and a stub returning the right answer would pass forever while
	@# production wrote wrong numbers into transcripts. See internal/llm/transcribe.go.
	FORGE_LLM_API_KEY="$$FORGE_LLM_API_KEY" go test -count=1 -v \
	  -run 'TestTranscription|TestTranscribingNothing|TestAnAbsurdly' ./internal/llm/
	FORGE_LLM_API_KEY="$$FORGE_LLM_API_KEY" go test -count=1 -v \
	  -run 'TestSpokenAudioBecomesAnAttributedTurn|TestThePipelinesOwnContainer' ./internal/media/

drill: db-wait ## Run the recovery drills against live Postgres (PRD NFR-07)
	FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl drill run

.PHONY: check
check: fmt-check vet test-integration drill ## Everything CI runs on every commit

# ---------------------------------------------------------------------------
# Release
# ---------------------------------------------------------------------------

# One command, run identically by a person and by the release workflow.
#
# The drills are in here and not only in `check` because they are the part most
# likely to be skipped by hand: they need a database, they take longer than the
# unit tests, and everything still compiles without them. A release that has not
# injected a real fault has not established that the system degrades safely —
# PRD NFR-07 is a claim about what happens when things break, and nothing else in
# the suite breaks anything.
#
# The evaluation suite is deliberately NOT here. It calls a real model, costs
# money, takes minutes, and is non-deterministic; wiring it into a gate would
# either make releases flaky or get the floors quietly lowered until they stopped
# failing. It runs on its own cadence — `make eval`, and the scheduled workflow.
.PHONY: release-check
release-check: fmt-check vet test-integration drill build ## Everything a release must pass
	@echo
	@echo "release-check passed: formatting, vet, tests against live Postgres,"
	@echo "recovery drills with real injected faults, and a clean build of all three binaries."
	@echo
	@echo "NOT covered by this gate, on purpose:"
	@echo "  · the evaluation suite (real model, costs money, non-deterministic) — make eval"
	@echo "  · every item under 'Carried defects' in docs/implementation-plan.md"

.PHONY: eval
eval: ## Run the evaluation suite against a real model (costs money, takes minutes)
	go run ./cmd/forgectl eval run --repeats 3

.PHONY: eval-list
eval-list: ## What the evaluation suite measures, and why each case exists
	go run ./cmd/forgectl eval list

# Cross-compiled release binaries. Pure Go, so CGO is off and every target
# builds from any host — a release that can only be cut on one person's laptop
# is a release nobody else can cut.
RELEASE_DIR   := dist
RELEASE_OSARCH := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

.PHONY: dist
dist: ## Build cross-platform release binaries into ./dist with checksums
	@rm -rf $(RELEASE_DIR) && mkdir -p $(RELEASE_DIR)
	@for target in $(RELEASE_OSARCH); do \
	  os=$${target%/*}; arch=$${target#*/}; \
	  for cmd in forged forge-worker forgectl; do \
	    out=$(RELEASE_DIR)/$$cmd-$$os-$$arch; \
	    echo "  $$out"; \
	    CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	      go build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/$$cmd || exit 1; \
	  done; \
	done
	@cd $(RELEASE_DIR) && shasum -a 256 * > SHA256SUMS
	@echo
	@echo "$(VERSION) ($(COMMIT)) built into $(RELEASE_DIR)/ with SHA256SUMS"

# The artefact, not the recipe. A binary built without ldflags reports "dev" and
# is indistinguishable from a release once it has left this machine, so the check
# is on what the binary SAYS rather than on how it was compiled.
.PHONY: dist-verify
dist-verify: ## Check that the built binaries report the version they were stamped with
	@test -d $(RELEASE_DIR) || { echo "no $(RELEASE_DIR)/ — run make dist first"; exit 1; }
	@host=$$(go env GOOS)/$$(go env GOARCH); \
	 bin=$(RELEASE_DIR)/forgectl-$${host%/*}-$${host#*/}; \
	 test -x $$bin || { echo "no binary for this host ($$host); cannot verify"; exit 1; }; \
	 reported=$$($$bin version); \
	 echo "$$reported"; \
	 case "$$reported" in \
	   *"$(VERSION)"*) ;; \
	   *) echo "the binary reports a different version from $(VERSION) — ldflags did not reach the build"; exit 1;; \
	 esac; \
	 case "$$reported" in \
	   *" dev "*|*"forgectl dev"*) echo "the binary reports 'dev': it was built without version stamping"; exit 1;; \
	 esac
	@cd $(RELEASE_DIR) && shasum -a 256 -c SHA256SUMS >/dev/null && echo "checksums verified"

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
	@# Ask the DATABASE, not Docker.
	@#
	@# This used to check the Docker daemon and the local container by name,
	@# which is the right diagnosis on a laptop and the wrong question
	@# everywhere else: CI runs Postgres as a service with no container of that
	@# name, so every target depending on this was unusable there — and
	@# `release-check` is supposed to be one command that a person and the
	@# release workflow both run. So the probe is now "can something connect",
	@# and the Docker checks below run only when the answer is no, where they
	@# are still the most likely explanation.
	@for i in $$(seq 1 30); do \
	  if FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl health >/dev/null 2>&1; then exit 0; fi; \
	  sleep 1; \
	done; \
	echo "Nothing is answering at $(DB_URL) after 30s."; \
	if ! docker info >/dev/null 2>&1; then \
	  echo "  cause : the Docker daemon is not reachable, so the local database is not running."; \
	  echo "  fix   : start Docker (or 'colima start'), then 'make db-up'. Current DOCKER_HOST=$${DOCKER_HOST:-<unset, using default socket>}"; \
	elif ! docker ps -a --format '{{.Names}}' | grep -qx '$(DB_CONTAINER)'; then \
	  echo "  cause : no container named '$(DB_CONTAINER)' exists."; \
	  echo "  fix   : run 'make db-up' to create it."; \
	elif ! docker ps --format '{{.Names}}' | grep -qx '$(DB_CONTAINER)'; then \
	  echo "  cause : container '$(DB_CONTAINER)' exists but is not running."; \
	  echo "  fix   : run 'make db-up' to start it."; \
	else \
	  echo "  cause : container '$(DB_CONTAINER)' is running but Postgres is not accepting connections."; \
	  echo "  fix   : inspect startup errors with 'docker logs $(DB_CONTAINER)'"; \
	fi; \
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
