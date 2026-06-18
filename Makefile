# Makefile — conductor-platform standard entry points (P4-3).
#
# One command each for build / test / gate / run / migrate / docker / db, all
# wired to EXACTLY how the project actually works (the same commands CI runs and
# the same CONDUCTOR_* env the daemon reads). No secret lives here: the develop
# performer command and the gh-token are operator-supplied at runtime.
#
# `gate` is the canonical "is it green" command and mirrors .github/workflows.
# Portable to BSD/GNU make on macOS + Linux; recipes use plain POSIX sh. Run
# `make help` (the default) for the target list.
#
# Override any variable on the command line, e.g.:
#   make run PROJECT=myproj ROOT=/tmp/ws
#   make docker-build IMAGE=conductor-platform:dev
#   make itest TEST_DATABASE_URL="postgres://...:.../db?sslmode=disable"

# ---- overridable variables (?= so the environment / CLI win) ----------------
GO            ?= go
GOLANGCI_LINT ?= golangci-lint
GOFMT         ?= gofmt
BINDIR        ?= bin
IMAGE         ?= conductor-platform:latest
COMPOSE       ?= docker compose

# Daemon run config (cmd/conductor flags; each falls back to CONDUCTOR_*).
# DSN empty => in-memory store (dev/test). Set DSN to a Postgres URL for the
# shared, migrated store. PROJECT and ROOT are the only required run inputs.
PROJECT       ?= demo
ROOT          ?= /tmp/conductor-ws
BASE_BRANCH   ?= develop
HTTP_ADDR     ?= :8080
INTERVAL      ?= 30s
DSN           ?=
DEVELOP_CMD   ?= claude -p
HEARTBEAT     ?= $(ROOT)/heartbeat.json

# Local dev Postgres (db-up/db-down). Password is a LOCAL DEV DEFAULT, not a
# secret; override for anything non-local. TEST_DATABASE_URL is what the
# statestore/events integration tests read (they SKIP when it is unset).
PG_CONTAINER      ?= conductor-pg
PG_IMAGE          ?= postgres:16-alpine
PG_PORT           ?= 5433
PG_USER           ?= conductor
PG_PASSWORD       ?= conductor
PG_DB             ?= conductor
TEST_DATABASE_URL ?= postgres://$(PG_USER):$(PG_PASSWORD)@localhost:$(PG_PORT)/$(PG_DB)?sslmode=disable

.PHONY: help build test e2e vet lint fmt fmt-check gate run check \
        docker-build compose-up compose-down db-up db-down itest clean

# ---- help (default) ---------------------------------------------------------
help: ## Print this target list
	@echo 'conductor-platform — make targets'
	@echo ''
	@echo 'Usage: make <target> [VAR=value ...]'
	@echo ''
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  %-14s %s\n", $$1, $$2}'
	@echo ''
	@echo 'Key vars: PROJECT ROOT DSN IMAGE INTERVAL HTTP_ADDR TEST_DATABASE_URL'
	@echo 'See docs/DEPLOY.md for the full deployment guide.'

# ---- gate pieces (the real CI commands) -------------------------------------
build: ## Build both binaries into $(BINDIR) (conductor, conductorctl)
	@mkdir -p $(BINDIR)
	$(GO) build -o $(BINDIR)/conductor    ./cmd/conductor
	$(GO) build -o $(BINDIR)/conductorctl ./cmd/conductorctl
	@echo "built: $(BINDIR)/conductor $(BINDIR)/conductorctl"

test: ## Run the unit/integration test suite (go test ./...)
	$(GO) test ./...

e2e: ## Run the hermetic end-to-end test (-tags e2e)
	$(GO) test -tags e2e -run E2E ./internal/conductor/

vet: ## Run go vet over all packages
	$(GO) vet ./...

lint: ## Run golangci-lint (v2, .golangci.yml)
	$(GOLANGCI_LINT) run

fmt: ## Format all Go files in place (gofmt -w .)
	$(GOFMT) -w .

fmt-check: ## Fail if any Go file is not gofmt-clean (gofmt -l .)
	@unformatted=$$($(GOFMT) -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi; \
	echo "gofmt: clean"

# ---- the canonical green gate ----------------------------------------------
gate: ## Full deterministic gate: build + test + vet + lint + fmt-check (what CI runs)
	$(GO) build ./...
	$(GO) test ./...
	$(GO) vet ./...
	$(GOLANGCI_LINT) run
	@$(MAKE) fmt-check
	@echo "gate: GREEN"

# ---- run the daemon / stall detector ---------------------------------------
run: ## Run the tick daemon locally (DSN empty => in-memory store)
	@echo "running conductor: project=$(PROJECT) root=$(ROOT) http=$(HTTP_ADDR) dsn=$(if $(DSN),<set>,<in-memory>)"
	CONDUCTOR_DSN="$(DSN)" \
	$(GO) run ./cmd/conductor \
		-project "$(PROJECT)" \
		-root "$(ROOT)" \
		-base "$(BASE_BRANCH)" \
		-interval "$(INTERVAL)" \
		-http-addr "$(HTTP_ADDR)" \
		-develop-cmd "$(DEVELOP_CMD)" \
		-heartbeat "$(HEARTBEAT)"

check: ## Run the INDEPENDENT stall detector over $(HEARTBEAT) (exit 0=FRESH)
	$(GO) run ./cmd/conductor -check -heartbeat "$(HEARTBEAT)"

# ---- docker -----------------------------------------------------------------
docker-build: ## Build the runtime image ($(IMAGE))
	docker build -t $(IMAGE) .

compose-up: ## Start daemon + Postgres via docker compose (detached)
	$(COMPOSE) up -d

compose-down: ## Stop the compose stack and drop its volumes
	$(COMPOSE) down -v

# ---- local dev Postgres for the DB-gated integration tests ------------------
db-up: ## Start a throwaway local Postgres for itest (prints TEST_DATABASE_URL)
	docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_USER=$(PG_USER) \
		-e POSTGRES_PASSWORD=$(PG_PASSWORD) \
		-e POSTGRES_DB=$(PG_DB) \
		-p $(PG_PORT):5432 \
		$(PG_IMAGE) >/dev/null
	@echo "waiting for postgres on :$(PG_PORT) ..."
	@for i in $$(seq 1 30); do \
		if docker exec $(PG_CONTAINER) pg_isready -U $(PG_USER) -d $(PG_DB) >/dev/null 2>&1; then \
			echo "postgres ready"; break; \
		fi; \
		sleep 1; \
	done
	@echo 'export TEST_DATABASE_URL="$(TEST_DATABASE_URL)"'

db-down: ## Stop and remove the local dev Postgres container
	-docker rm -f $(PG_CONTAINER) >/dev/null 2>&1
	@echo "removed $(PG_CONTAINER)"

itest: ## Run the DB-gated integration tests (needs db-up first / TEST_DATABASE_URL)
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test ./internal/statestore/... ./internal/events/...

# ---- housekeeping -----------------------------------------------------------
clean: ## Remove built binaries
	rm -rf $(BINDIR)
