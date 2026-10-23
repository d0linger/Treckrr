# Treckrr dev tasks. The project builds/tests inside Docker (no local Go needed).
# Usage: `make check` before committing; `make run` to start the app.

IMG    := golang:1.27.3-alpine3.24
LINT   := golangci/golangci-lint:v2.13.1
PG_MAJOR ?= 16
GO     := docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.27.3 $(IMG) sh -c
GOTEST := docker run --rm --network treckrr-itest -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.27.3 -e PG_MAJOR="$(PG_MAJOR)" -e TEST_DATABASE_URL="$(TEST_DATABASE_URL)" -e TEST_INVOICE_MIGRATION_DATABASES="$(TEST_INVOICE_MIGRATION_DATABASES)" $(IMG) sh -c

.PHONY: help run down logs demo demo-down demo-reset build vet fmt fmt-check lint deadcode test test-unit test-parity check

help: ## Show this help
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | sort

run: ## Build the image and start the compose stack
	docker compose up -d --build

down: ## Stop the compose stack
	docker compose down

logs: ## Follow the app logs
	docker logs -f treckrr-app

DEMO := docker compose -f compose.demo.yml

demo: ## Start the practice instance with sample data on http://localhost:8090 (never touches the real stack)
	$(DEMO) up -d --build --wait
	$(DEMO) exec -T db psql -U treckrr_demo -d treckrr_demo -v ON_ERROR_STOP=1 < demo/seed.sql

demo-down: ## Stop the practice instance, keeping its data
	$(DEMO) down

demo-reset: ## Delete the practice instance and its data (next `make demo` starts fresh)
	$(DEMO) down --volumes

build: ## go build all packages
	$(GO) "go build ./..."

vet: ## go vet
	$(GO) "go vet -tags=integration ./..."

fmt: ## gofmt -w
	$(GO) "gofmt -w internal/ cmd/"

fmt-check: ## fail if any file needs gofmt
	$(GO) "test -z \"$$(gofmt -l internal/ cmd/)\""

lint: ## golangci-lint
	# Analyzer-only compatibility pin; application builds use Go 1.27.3 above.
	docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.26.10 $(LINT) golangci-lint run --build-tags=integration ./...

deadcode: ## unreachable-function analysis (matches CI)
	$(GO) "go install golang.org/x/tools/cmd/deadcode@v0.49.0 && sh .github/scripts/check-deadcode.sh"

test: ## Full race + integration suite (isolated TEST_DATABASE_URL, TEST_INVOICE_MIGRATION_DATABASES=1, PG_MAJOR=16 or 18)
	$(GOTEST) "sh .github/scripts/test-local.sh"

test-parity: ## Old/new financial comparison in owned disposable containers (Docker, Node, git history required)
	node .github/scripts/billing-parity.mjs f85ca89377f22797dc66f65c37ae37943e18c18d

test-unit: ## Unit-only convenience; explicitly excludes the DB-backed release gate
	$(GO) "go test ./..."

check: fmt-check build vet lint deadcode test ## Core Go CI checks; full security/image workflows remain separate
	@echo "OK: build, vet, gofmt, golangci, deadcode, tests all passed"
