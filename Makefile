.PHONY: build test lint fmt vet run check clean help proto e2e
APP_NAME := fakemarket
CMD_PATH := ./cmd/fakemarket
BIN_DIR  := bin
BINARY   := $(BIN_DIR)/$(APP_NAME)

GO       ?= go
GOLANGCI_LINT ?= golangci-lint

# ---------------------------------------------------------------------------
# Primary targets
# ---------------------------------------------------------------------------

## proto: generate gRPC/protobuf Go code
proto:
	@mkdir -p api/gen
	protoc -I api/proto \
		--go_out=api/gen --go_opt=paths=source_relative \
		--go-grpc_out=api/gen --go-grpc_opt=paths=source_relative \
		api/proto/fakemarket/v1/trading.proto

## build: compile the FakeMarket binary
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BINARY) $(CMD_PATH)

## test: run all unit tests
test:
	$(GO) test ./...

## fmt: format all Go source files
fmt:
	$(GO) fmt ./...
	@command -v goimports >/dev/null 2>&1 && goimports -w . || true

## vet: run go vet
vet:
	$(GO) vet ./...

## lint: run static analysis (golangci-lint when available, else go vet)
lint:
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run ./...; \
	else \
		echo "golangci-lint not found; falling back to go vet"; \
		$(GO) vet ./...; \
	fi

## run: start FakeMarket locally
run: build
	$(BINARY)

## check: full local validation (fmt check + vet + lint + test + build)
check: fmt-check vet lint test build
	@echo ""
	@echo "format      PASS"
	@echo "vet         PASS"
	@echo "lint        PASS"
	@echo "tests       PASS"
	@echo "build       PASS"

## fmt-check: verify formatting without writing changes
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "ERROR: the following files need formatting:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## help: show available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
