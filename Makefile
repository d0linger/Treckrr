# Treckrr dev tasks. The project builds/tests inside Docker (no local Go needed).
# Usage: `make check` before committing; `make run` to start the app.

IMG    := golang:1.26-alpine
LINT   := golangci/golangci-lint:v2.12.2
GO     := docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.26.6 $(IMG) sh -c
GOTEST := docker run --rm --network treckrr-itest -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.26.6 -e TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(IMG) sh -c

.PHONY: help run down logs build vet fmt fmt-check lint deadcode test check

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
	$(GO) "go vet ./..."

fmt: ## gofmt -w
	$(GO) "gofmt -w internal/ cmd/"

fmt-check: ## fail if any file needs gofmt
	$(GO) "test -z \"$$(gofmt -l internal/ cmd/)\""

lint: ## golangci-lint
	docker run --rm -v "$(CURDIR):/src" -v treckrr-gomod:/go/pkg/mod -w /src -e GOTOOLCHAIN=go1.26.6 $(LINT) golangci-lint run ./...

deadcode: ## unreachable-function analysis (matches CI)
	$(GO) "go install golang.org/x/tools/cmd/deadcode@v0.49.0 && out=\$$(\$$(go env GOPATH)/bin/deadcode -test ./...); echo \"\$$out\"; [ -z \"\$$out\" ] || exit 1"

test: ## go test (set TEST_DATABASE_URL for the integration suite, else it skips)
	$(GOTEST) "go test ./..."

check: fmt-check build vet lint deadcode test ## Everything CI runs, locally
	@echo "OK: build, vet, gofmt, golangci, deadcode, tests all passed"
