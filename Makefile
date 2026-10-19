.PHONY: build test lint fmt vet run check clean help proto e2e bench bench-scale profile profile-full docker-build docker-up docker-down docker-logs sim demo-p104
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
		api/proto/fakemarket/v1/trading.proto \
		api/proto/fakemarket/v1/query.proto

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

## docker-build: build FakeMarket image
docker-build:
	docker compose build

## docker-up: start 3-node + prometheus + grafana
docker-up:
	docker compose up -d

## docker-down: stop stack (volumes retained)
docker-down:
	docker compose down

## docker-logs: follow node logs
docker-logs:
	docker compose logs -f node-a node-b node-c

## sim: P10.3 canonical demo (2m, 4 clients, 8 instruments) — not P7 bench
sim:
	$(GO) run ./cmd/fakemarket-sim -seed 42 -scenario normal -duration 2m -rate 50 -clients 4 -instruments 8 -accounts 50 -metrics-addr :9100

## sim-quick: short deterministic check (fingerprint + trades + P8.6)
sim-quick:
	$(GO) run ./cmd/fakemarket-sim -seed 42 -scenario normal -commands 400 -rate 2000 -clients 4 -instruments 8

## demo-p104: P10.4 failure/recovery demo (real P9 paths; not Compose HTTP kill)
demo-p104:
	$(GO) run ./cmd/fakemarket-demo -seed 42 -pre 120 -post 80 -instruments 8 -accounts 50

## bench: P7.4 short reproducible benches (not part of check duration)
bench:
	$(GO) test ./internal/bench/ -bench=. -benchmem -benchtime=200ms -count=1

## bench-scale: fixed-N scale tests (set BENCH_SCALE=100k|full)
bench-scale:
	$(GO) test ./internal/bench/ -run 'Scale_' -count=1 -v

## profile: P7.5 pprof A–E (each test = own process so mutex/block do not bleed). Not a thruput claim.
profile:
	@mkdir -p docs/benchmarks/profiles/out
	@c=$$(git rev-parse --short HEAD 2>/dev/null || echo local); \
	export GIT_COMMIT=$$c PROFILE_WRITE=1 PROFILE_N=$${PROFILE_N:-800}; \
	for t in TestProfile_A_Mixed TestProfile_B_HotInstrument TestProfile_C_FullMatch TestProfile_D_NoMatch_WAL TestProfile_E_DeepBook; do \
	  echo "=== $$t ==="; \
	  $(GO) test ./internal/bench/ -run "^$$t$$" -count=1 -v || exit 1; \
	done

## profile-full: longer steady-state profiles (separate processes)
profile-full:
	@mkdir -p docs/benchmarks/profiles/out
	@c=$$(git rev-parse --short HEAD 2>/dev/null || echo local); \
	export GIT_COMMIT=$$c PROFILE_WRITE=1 PROFILE_FULL=1 PROFILE_N=$${PROFILE_N:-5000}; \
	for t in TestProfile_A_Mixed TestProfile_B_HotInstrument TestProfile_C_FullMatch TestProfile_D_NoMatch_WAL TestProfile_E_DeepBook; do \
	  echo "=== $$t ==="; \
	  $(GO) test ./internal/bench/ -run "^$$t$$" -count=1 -v || exit 1; \
	done

## profile-top: print top of key pprof files (requires prior make profile)
profile-top:
	@c=$$(git rev-parse --short HEAD 2>/dev/null || echo local); \
	d=docs/benchmarks/profiles/out/$$c; \
	echo "== $$d =="; \
	for f in A_mixed_cpu A_mixed_allocs B_hot_cpu B_hot_mutex B_hot_block C_fullmatch_cpu C_fullmatch_allocs D_wal_cpu D_wal_block E_depth_cpu; do \
	  p=$$d/$${f}.pprof; \
	  if [ -f $$p ]; then echo "--- $$f ---"; go tool pprof -top -nodecount=12 $$p 2>/dev/null | head -22; echo; fi; \
	done
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
