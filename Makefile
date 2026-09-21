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

# The deployment image. IMAGE_BUILD_ARGS carries whatever the machine building it
# needs — `--platform linux/arm64`, `--build-arg APT_MIRROR=…` — without that
# having to be remembered alongside the version arguments, which are not
# optional. See deploy/README.md.
IMAGE            ?= forge:$(COMMIT)
IMAGE_BUILD_ARGS ?=

# Local development database. Runs on a non-default port so it cannot collide
# with another Postgres already on this machine.
DB_CONTAINER := forge-pg
DB_PORT      ?= 55840
# The CAD kernel's interpreter. Not committed: it is 60+ MB of OpenCASCADE, and
# a deployment without it refuses parametric export rather than faking it.
CAD_VENV     ?= .cadvenv
# The venv's interpreter. A Windows venv keeps it in Scripts\python.exe and has no
# bin/ at all, so `make measure-car` defaulted to a path that does not exist there
# (docs/spikes/2026-09-17-live-verification, follow-up). FORGE_CAD_PYTHON, when set,
# is used before either. Fence: TestMeasureCar_PicksTheVenvPythonForThisOS.
ifeq ($(OS),Windows_NT)
CAD_PYTHON   ?= $(abspath $(CAD_VENV))/Scripts/python.exe
else
CAD_PYTHON   ?= $(abspath $(CAD_VENV))/bin/python
endif
# Every package in it, pinned. The same file the image and CI install from.
CAD_REQUIREMENTS := internal/domain/cad/requirements.txt
DB_USER      ?= forge
DB_PASS      ?= forge_dev_pw
DB_NAME      ?= forge
DB_URL       := postgres://$(DB_USER):$(DB_PASS)@localhost:$(DB_PORT)/$(DB_NAME)?sslmode=disable
# Local S3-compatible blob store (MinIO) for the blob store's tests. Pinned to a
# release: an unpinned server is how a test that passed yesterday fails today
# for a reason nobody changed. Development credentials only.
BLOB_CONTAINER := forge-minio
# quay.io, not Docker Hub: MinIO no longer publishes minio/minio there (the repository
# answers 404), so CI could not pull it. Same tag, same image: its manifest digest is
# sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e on both.
# docs/bugfix/2026-09-14-minio-could-not-be-pulled-in-ci.md
BLOB_IMAGE     ?= quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z
BLOB_PORT      ?= 55841
BLOB_USER      ?= forge
BLOB_PASS      ?= forge_dev_minio
BLOB_ENDPOINT  := http://localhost:$(BLOB_PORT)

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

.PHONY: image
image: ## Build the deployment image, stamped with this commit
	@# ‼️ The version arguments are the whole point of having this target rather
	@# than a docker build command in a README: an image built without them
	@# reports "unknown" for the rest of its life, and nothing downstream can
	@# tell which commit answered a request. VERSION/COMMIT/BUILD_DATE are the
	@# same three `make build` stamps into the local binaries.
	docker build $(IMAGE_BUILD_ARGS) \
	  --build-arg FORGE_VERSION=$(VERSION) \
	  --build-arg FORGE_COMMIT=$(COMMIT) \
	  --build-arg FORGE_BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE) -f deploy/Dockerfile .
	@echo "built image $(IMAGE) stamped $(VERSION) ($(COMMIT))"
	@# The stamp, read back out of the image that will actually be deployed. A
	@# build argument that is silently dropped (a typo in the ARG name, a stage
	@# that never declared it) leaves a green build and an unstamped image, and
	@# this is the only place that difference is visible before production is.
	docker run --rm --entrypoint /usr/local/bin/forgectl $(IMAGE) version

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
	@# -timeout for the same reason as test-integration below.
	go test -count=1 -race -timeout 30m ./...

.PHONY: test-integration
test-integration: db-wait ## Run all tests including those needing live Postgres
	@# ‼️ -timeout, because `go test`'s 10m default is PER PACKAGE and two
	@# packages are now close to it. Measured 2026-09-20 on this repository,
	@# without -race: internal/httpapi ~600 s (601 s and 602 s in two whole-repo
	@# runs) and internal/agent 565 s. On a contended machine httpapi tripped the
	@# default outright — "panic: test timed out after 10m0s", in the middle of a
	@# passing test — and the same package finished in 394 s on its own when the
	@# machine was quiet. -race makes both slower still. Nothing was hung either
	@# time; there is simply more work than the default allows, and the number
	@# only goes up as fences are added.
	@#
	@# 30m rather than no limit, for the reason test-cad gives: a genuinely hung
	@# test must still fail the job rather than run to the runner's own ceiling.
	@# Raise it again only on a run that shows the honest work exceeding it.
	FORGE_TEST_DATABASE_URL="$(DB_URL)" go test -count=1 -race -timeout 30m ./...

.PHONY: test-cover
test-cover: db-wait ## Run tests with coverage and print a summary
	@# -timeout for the same reason as test-integration above.
	FORGE_TEST_DATABASE_URL="$(DB_URL)" go test -count=1 -coverprofile=coverage.out -timeout 30m ./...
	go tool cover -func=coverage.out | tail -20

.PHONY: cad-venv
cad-venv: ## Create the Python venv the CAD kernel runs in (PRD VIS-05)
	@# Parametric export needs a real kernel. This builds the one the tests and
	@# the server use, and prints the single line that switches it on.
	@#
	@# Not committed and not required: a deployment without it declares STEP and
	@# refuses it, which is the default and a supported configuration.
	@#
	@# The versions come from $(CAD_REQUIREMENTS), never from PyPI's latest: the
	@# kernel tests prove one OpenCASCADE, and this is the one they prove.
	@# --no-deps + pip check: a dependency missing from the list fails here.
	python3 -m venv $(CAD_VENV)
	$(CAD_VENV)/bin/pip install --quiet --upgrade pip
	$(CAD_VENV)/bin/pip install --quiet --no-deps -r $(CAD_REQUIREMENTS)
	$(CAD_VENV)/bin/pip check
	@$(CAD_VENV)/bin/python -c "import build123d; print('build123d', build123d.__version__)"
	@echo
	@echo "export FORGE_CAD_PYTHON=$(abspath $(CAD_VENV))/bin/python"

.PHONY: cad-script-timing
cad-script-timing: ## Time a scripted part end to end under FORGE's limits, and describe the machine (never fails)
	@# Diagnosis for the open script timeout on CI runners; see scripts/cad_script_timing.py.
	@test -x $(CAD_VENV)/bin/python || { echo "no CAD venv: run \`make cad-venv\` first"; exit 1; }
	@$(CAD_VENV)/bin/python scripts/cad_script_timing.py internal/domain/cad/script.py internal/domain/cad/script_test.go internal/domain/cad/script.go || true

.PHONY: test-cad
test-cad: ## Run the CAD kernel tests against the real kernel (needs `make cad-venv`)
	@# These cannot be faked. Every property they check — that the solid is
	@# valid, that its volume is right, that a cylinder points the way this
	@# system draws it — is a property of OpenCASCADE and not of our code, and a
	@# stub would be asserting that the test author knows what OCCT does.
	@# ‼️ -timeout, because this package outgrew `go test`'s 10m default. On
	@# 2026-09-16, once the stack landed on main, the CI kernel job died with
	@# "panic: test timed out after 10m0s" in the MIDDLE of a passing test
	@# (TestScript_AWholeNumberedParameterIsAnInt): nothing was hung, there was
	@# simply more work than the default allows. Each scripted-part test spends
	@# 2-3 s starting a real build123d, and there are now well over a hundred.
	@#
	@# 30m rather than no limit: a genuinely hung kernel — a python child waiting
	@# on a pipe nobody writes to — has to still fail the job rather than run
	@# until the runner's own 6h ceiling. Raise it again only with a run that
	@# shows the honest work exceeding it, never to get past a hang.
	@test -x $(CAD_VENV)/bin/python || { echo "no CAD venv: run \`make cad-venv\` first"; exit 1; }
	FORGE_CAD_PYTHON="$(abspath $(CAD_VENV))/bin/python" go test -count=1 -v -timeout 30m ./internal/domain/cad/

.PHONY: test-cad-exhaustive
test-cad-exhaustive: ## Run the kernel fences too slow for every PR (~18 min on CI; nightly)
	@# Only the tests gated behind FORGE_EXHAUSTIVE_KERNEL_TESTS. Today that is
	@# TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswer: eight fixtures, four
	@# placement variants, 1,059 s on ubuntu-24.04-arm (CI run 35097920395), against
	@# 686 s for the whole rest of this package. test-cad skips it and runs the
	@# three-fixture fence instead; the nightly kernel-exhaustive job runs this.
	@#
	@# 40m: 2.3x the 18 minutes measured, because runner speed drifts, and still a
	@# ceiling a genuinely hung kernel hits — the reason test-cad has one too.
	@test -x $(CAD_VENV)/bin/python || { echo "no CAD venv: run \`make cad-venv\` first"; exit 1; }
	FORGE_EXHAUSTIVE_KERNEL_TESTS=1 FORGE_CAD_PYTHON="$(abspath $(CAD_VENV))/bin/python" \
		go test -count=1 -v -timeout 40m -run '^TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswer$$' ./internal/domain/cad/

.PHONY: measure-car
measure-car: ## Measure how far a live car build actually gets (SPENDS REAL TOKENS — read the budget note)
	@# The question this answers is "where does it stop", not "does it pass".
	@# See internal/agent/car_ceiling_live_test.go and
	@# docs/research-2026-09-12-vehicles-aircraft-and-structures.md.
	@#
	@# ‼️ This endpoint is a shared weekly token plan and one earlier live spike
	@# spent the week in eighteen calls. FORGE_MEASURE_TOKEN_BUDGET is a HARD
	@# ceiling enforced in the harness: once it is gone no further model call is
	@# placed, the passes already built are kept, and the run reports that what it
	@# measured is a partial car. Raise it deliberately, never by habit.
	@# 300k is the ceiling damon approved for the Phase 2 live milestone (A4,
	@# decided 2026-09-15); it was 400k.
	@# The kernel is FORGE_CAD_PYTHON when it is set, else $(CAD_PYTHON): the venv's
	@# Scripts/python.exe on Windows and bin/python elsewhere.
	@test -n "$$FORGE_LLM_API_KEY" || { echo "FORGE_LLM_API_KEY is not set — source .env first"; exit 1; }
	FORGE_LIVE_LLM_TESTS=1 \
	FORGE_MEASURE_TOKEN_BUDGET="$${FORGE_MEASURE_TOKEN_BUDGET:-300000}" \
	FORGE_CAD_PYTHON="$${FORGE_CAD_PYTHON:-$(CAD_PYTHON)}" \
	go test -count=1 -v -timeout 60m -run TestLiveCarCeiling ./internal/agent/

.PHONY: looks-benchmark
looks-benchmark: ## Build, render and judge the fixed looks prompts (SPENDS REAL TOKENS — read the budget note)
	@# Five fixed prompts, one build each, rendered by this branch's forge3d.js and
	@# by an earlier one, and scored by the looks judge (internal/looks). The record
	@# and every picture land in docs/spikes/2026-09-20-looks-benchmark/.
	@# See internal/agent/looks_benchmark_live_test.go.
	@#
	@# ‼️ TWO hard ceilings, both enforced by refusing the call: the run's total and
	@# one per prompt, so a car that will not settle cannot eat the lever's budget.
	@# 100k is what damon approved for the whole of stage D (2026-09-20); the default
	@# below leaves headroom under it. Raise either deliberately, never by habit.
	@#
	@# FORGE_LOOKS_BEFORE_RENDERER names the forge3d.js today's pictures are compared
	@# AGAINST. Without it the prompts are built and rendered but nothing is judged,
	@# and the record says so. Get one with:
	@#   git show origin/main:internal/httpapi/assets/forge3d.js > /tmp/before-forge3d.js
	@test -n "$$FORGE_LLM_API_KEY" || { echo "FORGE_LLM_API_KEY is not set — source .env first"; exit 1; }
	FORGE_LIVE_LLM_TESTS=1 \
	FORGE_LOOKS_BENCH_BUDGET="$${FORGE_LOOKS_BENCH_BUDGET:-88000}" \
	FORGE_LOOKS_BENCH_PER_PROMPT="$${FORGE_LOOKS_BENCH_PER_PROMPT:-17000}" \
	FORGE_CAD_PYTHON="$${FORGE_CAD_PYTHON:-$(CAD_PYTHON)}" \
	go test -count=1 -v -timeout 55m -run 'TestLiveLooksBenchmark$$' ./internal/agent/

.PHONY: drill
test-asr: ## Speech fences against the REAL provider, both directions (costs a fraction of a cent)
	@# These cannot be faked. The defect they guard — a model dropping decimal
	@# points out of engineering speech — is a property of the provider, not of
	@# this code, and a stub returning the right answer would pass forever while
	@# production wrote wrong numbers into transcripts. See internal/llm/transcribe.go.
	FORGE_LLM_API_KEY="$$FORGE_LLM_API_KEY" go test -count=1 -v \
	  -run 'TestTranscription|TestTranscribingNothing|TestAnAbsurdly' ./internal/llm/
	FORGE_LLM_API_KEY="$$FORGE_LLM_API_KEY" go test -count=1 -v \
	  -run 'TestSpokenAudioBecomesAnAttributedTurn|TestThePipelinesOwnContainer|TestForgesVoiceIsIntelligible' ./internal/media/

drill: db-wait ## Run the recovery drills against live Postgres (PRD NFR-07)
	FORGE_DATABASE_URL="$(DB_URL)" go run ./cmd/forgectl drill run

.PHONY: drill-fences
drill-fences: ## Break each sweep fence on purpose and check it goes red (edits source, then restores it)
	@# Different question from `drill` above, which injects faults into a RUNNING
	@# system and checks it recovers. This injects them into the SOURCE, one at a
	@# time, and checks that the test claiming to hold each one actually fails.
	@#
	@# Why it is worth a target of its own: a test that has never failed is a
	@# claim, not a fence. Two in this repository were written, reviewed and could
	@# not fail — the shape dispatch (wave 18) and the sweep twist test (wave 19)
	@# — and neither was found by reading the code.
	@#
	@# It edits files in place and restores them from a checksummed backup on the
	@# way out, including on an interrupt, and says whether the tree came back
	@# byte-identical. Do not run it beside another build: while a mutation is
	@# applied, the tree on disk is the mutated one.
	@#
	@# The database URL is passed because three of the drills point at the turn
	@# handler, whose fences need one. Without it those tests SKIP, and a skipped
	@# test is reported as UNPROVEN — which is the honest answer and not the one
	@# worth settling for. Not a dependency on db-wait: the other 82 drills need
	@# no database and must still run on a machine without one.
	FORGE_TEST_DATABASE_URL="$(DB_URL)" scripts/drill-fences.sh

.PHONY: check
test-echo: ## Check the hands-free echo guard against the transcripts that caused the loop
	@node scripts/echo-guard-check.js

.PHONY: test-voice-fallback
test-voice-fallback: ## Check one failed audio load falls back to the browser voice exactly once
	@node scripts/voice-fallback-check.js

.PHONY: cad-builders
cad-builders: ## Regenerate the list of build123d names a script may use
	@.cadvenv/bin/python internal/domain/cad/gen_builders.py > internal/domain/cad/builders.txt
	@echo "wrote $$(grep -vc '^#' internal/domain/cad/builders.txt) names"

.PHONY: test-extrusion-size
test-extrusion-size: ## Check the parts panel reports how big an extrusion really is
	@node scripts/extrusion-size-check.js

.PHONY: test-viewport
test-viewport: ## Check a 30k-occurrence car draws as one call per definition (stub GL; NOT a frame time)
	@# Phase 6, stage W1. Drives the shipped forge3d.js through scripts/webgl-stub.js
	@# over WebGL2, WebGL1 with ANGLE_instanced_arrays and WebGL1 without it. The
	@# milliseconds it prints are CPU in node against a context that draws nothing;
	@# the frame time in a real browser is docs/spikes/2026-09-15-instanced-viewport.
	@node scripts/viewport-instancing-check.js

check: fmt-check vet test-integration test-echo test-voice-fallback test-extrusion-size drill ## Everything CI runs on every commit
	@# The fence drills run LAST and from the recipe rather than as a
	@# prerequisite, because they edit the source while they run. As a
	@# prerequisite, `make -j check` could run them beside the test suite, and a
	@# test that read a half-mutated file would fail in a way indistinguishable
	@# from a real defect. Prerequisites are all finished before a recipe starts,
	@# so this is the one place they cannot overlap anything.
	$(MAKE) drill-fences

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

# EVAL_REPORT is written on EVERY run, and that is not a convenience.
#
# A run costs money and takes minutes, and its result is a rate that has to be
# read against the previous one. On 2026-09-04 a run reported a below-floor
# scorer and the diagnosis was lost because the invocation was piped through
# `tail` — which discarded eleven of fourteen cases AND replaced the eval's exit
# status with the pipe's, so a red run reported success. Re-running produced 3/3
# and the original failure was never explained.
#
# Writing the full report to a file makes that irrecoverable-by-accident state
# impossible: whatever happens to the terminal, every reply and every scorer's
# reasoning is on disk.
EVAL_REPORT ?= eval-report.json

.PHONY: eval
eval: ## Run the evaluation suite against a real model (costs money, takes minutes)
	go run ./cmd/forgectl eval run --repeats 3 --json "$(EVAL_REPORT)"
	@echo "Full report, with every reply: $(EVAL_REPORT)"

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

.PHONY: blob-up
blob-up: ## Start the local MinIO container the blob store tests run against
	@if docker ps -a --format '{{.Names}}' | grep -qx '$(BLOB_CONTAINER)'; then \
	  docker start $(BLOB_CONTAINER) >/dev/null && echo "started existing $(BLOB_CONTAINER)"; \
	else \
	  docker run -d --name $(BLOB_CONTAINER) -p $(BLOB_PORT):9000 \
	    -e MINIO_ROOT_USER=$(BLOB_USER) -e MINIO_ROOT_PASSWORD=$(BLOB_PASS) \
	    $(BLOB_IMAGE) server /data >/dev/null && echo "created $(BLOB_CONTAINER) on port $(BLOB_PORT)"; \
	fi

.PHONY: blob-wait
blob-wait: ## Block until MinIO answers its readiness probe
	@# Asks the SERVER, like db-wait asks the database: a running container is
	@# not a ready one, and CI starts it moments before the tests.
	@for i in $$(seq 1 60); do \
	  curl -fsS $(BLOB_ENDPOINT)/minio/health/ready >/dev/null 2>&1 && { echo "MinIO ready at $(BLOB_ENDPOINT)"; exit 0; }; \
	  sleep 1; \
	done; \
	echo "MinIO did not become ready at $(BLOB_ENDPOINT) within 60s. Run \`make blob-up\`, or check \`docker logs $(BLOB_CONTAINER)\`."; exit 1

.PHONY: test-blob
test-blob: blob-wait ## Run the blob store tests against a real MinIO (needs `make blob-up`)
	@# Against a real S3-compatible server, never a fake: conditional writes,
	@# checksums and error shapes are properties of the server.
	FORGE_TEST_BLOB_ENDPOINT="$(BLOB_ENDPOINT)" AWS_ACCESS_KEY_ID="$(BLOB_USER)" \
	  AWS_SECRET_ACCESS_KEY="$(BLOB_PASS)" AWS_REGION=us-east-1 \
	  go test -count=1 -v ./internal/platform/blob/

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

.PHONY: db-clean-test-schemas
db-clean-test-schemas: ## Drop every schema left behind by a test or drill run
	@# Test schemas carry a random per-process id (internal/platform/db/testschema.go)
	@# so that two worktrees cannot drop each other's. The cost of that is a run
	@# killed part-way leaves its schemas behind, where before the next run of the
	@# same test would have reused the name. This sweeps them.
	@#
	@# Only forge_* schemas, never `public` and never `forge_migrations` if it ever
	@# becomes a schema: an operator running this against the wrong database should
	@# lose scratch, not data.
	@# The statement lives in a file rather than in -c: it is dollar-quoted
	@# PL/pgSQL, and getting that through make's $ and the shell's $ intact is
	@# three layers of escaping nobody should have to read. See the file for what
	@# it does and what it refuses to touch.
	docker exec -i $(DB_CONTAINER) psql -U $(DB_USER) -d $(DB_NAME) -q < scripts/clean-test-schemas.sql

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

.PHONY: restart
restart: db-wait ## Stop a running API server and start it again with the source as it is now
	@# Phase 6's acceptance is a browser run "on make restart": the assets are
	@# embedded in the binary (assetFS), so a change to forge3d.js reaches the
	@# workbench only through a rebuild, and a server left running keeps serving the
	@# old viewport while the page looks reloaded.
	@#
	@# ‼️ By process NAME. `go run` builds into a temporary directory and runs the
	@# binary as `forged`, so `pkill -f ./cmd/forged` would miss it and kill only the
	@# `go` wrapper — leaving the old server on the port and the new one failing to
	@# bind. The wait is for the port, not a fixed sleep.
	-@pkill -x forged 2>/dev/null && echo "stopped the running forged" || echo "no forged was running"
	@for i in $$(seq 1 20); do pgrep -x forged >/dev/null || break; sleep 0.5; done
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
