# Treckrr dev tasks. The project builds/tests inside Docker (no local Go needed).
# Usage: `make check` before committing; `make run` to start the app.

IMG    := golang:1.27-alpine
LINT   := golangci/golangci-lint:v2.13.1
PG_MAJOR ?= 16
GO     := docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.27.1 $(IMG) sh -c
GOTEST := docker run --rm --network treckrr-itest -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.27.1 -e PG_MAJOR="$(PG_MAJOR)" -e TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(IMG) sh -c

.PHONY: help run down logs build vet fmt fmt-check lint deadcode test test-unit check

help: ## Show this help
	@grep -hE '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/' | sort

run: ## Build the image and start the compose stack
	docker compose up -d --build

down: ## Stop the compose stack
	docker compose down

logs: ## Follow the app logs
	docker logs -f treckrr-app

build: ## go build all packages
	$(GO) "go build ./..."

vet: ## go vet
	$(GO) "go vet -tags=integration ./..."

fmt: ## gofmt -w
	$(GO) "gofmt -w internal/ cmd/"

fmt-check: ## fail if any file needs gofmt
	$(GO) "test -z \"$$(gofmt -l internal/ cmd/)\""

lint: ## golangci-lint
	docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.27.1 $(LINT) golangci-lint run --build-tags=integration ./...

deadcode: ## unreachable-function analysis (matches CI)
	$(GO) "go install golang.org/x/tools/cmd/deadcode@v0.49.0 && out=\$$(\$$(go env GOPATH)/bin/deadcode -tags=integration -test ./...); echo \"\$$out\"; [ -z \"\$$out\" ] || exit 1"

test: ## Full race + integration suite (requires isolated TEST_DATABASE_URL, PG_MAJOR=16 or 18)
	$(GOTEST) "sh .github/scripts/test-local.sh"

test-unit: ## Unit-only convenience; explicitly excludes the DB-backed release gate
	$(GO) "go test ./..."

check: fmt-check build vet lint deadcode test ## Core Go CI checks; full security/image workflows remain separate
	@echo "OK: build, vet, gofmt, golangci, deadcode, tests all passed"
