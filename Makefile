# qwash — common dev tasks.
# Mirrors the CI gates so `make lint` / `make test` reproduce CI locally.

BINARY       := bin/qwash
PKG          := ./...
# Throwaway PostgreSQL for `make test-db` (port avoids a local 5432/5438).
PG_IMAGE     ?= postgres:17
PG_PORT      ?= 5439
PG_CONTAINER := qwash-test-pg

.DEFAULT_GOAL := build
.PHONY: build test test-race test-db lint fmt vet clean help

build: ## Build the qwash binary into bin/
	go build -o $(BINARY) .

# Integration tests exec ./bin/qwash, so the binary must exist first, and a
# reachable PostgreSQL is required (set PGHOST/PGPORT/PGUSER/... or use test-db).
test: build ## Build, then run the suite (needs a reachable PostgreSQL)
	go test $(PKG)

test-race: ## Like `test` but race-instrumented (binary + tests), as in CI
	go build -race -o $(BINARY) .
	go test -race $(PKG)

test-db: build ## Run the suite against a throwaway PostgreSQL container (self-contained)
	@docker rm -f $(PG_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=postgres \
		-p $(PG_PORT):5432 $(PG_IMAGE) >/dev/null
	@echo "waiting for PostgreSQL on :$(PG_PORT)..."
	@for i in $$(seq 1 30); do \
		docker exec $(PG_CONTAINER) pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
	@PGHOST=localhost PGPORT=$(PG_PORT) PGUSER=postgres PGPASSWORD=postgres \
		PGDATABASE=qwash_test PGSSLMODE=disable go test $(PKG); \
		status=$$?; docker rm -f $(PG_CONTAINER) >/dev/null 2>&1; exit $$status

lint: ## Run the CI gates locally (gofmt -s, go vet, staticcheck)
	@test -z "$$(gofmt -s -l .)" || { echo "gofmt -s needed in:"; gofmt -s -l .; exit 1; }
	go vet $(PKG)
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@latest
	staticcheck $(PKG)

fmt: ## Apply gofmt -s -w to the tree
	gofmt -s -w .

clean: ## Remove build artifacts
	rm -rf bin dist

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
