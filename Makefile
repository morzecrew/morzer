# morzer — build, test and release targets.

BINARY      := morzer
MODULE      := github.com/morzecrew/morzer
CMD         := ./cmd/morzer

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# -trimpath and CGO_ENABLED=0 are reproducibility requirements, not
# preferences: the binary must be a single static file with no runtime
# dependency on the build machine's libc.
LDFLAGS     := -s -w \
               -X main.version=$(VERSION) \
               -X main.commit=$(COMMIT) \
               -X main.date=$(DATE)
GOFLAGS     := -trimpath
export CGO_ENABLED := 0

DIST        := dist

.PHONY: all
all: check build

## build: compile the binary for the host platform
.PHONY: build
build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-all: cross-compile for the supported targets
.PHONY: build-all
build-all:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 $(CMD)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 $(CMD)
	cd $(DIST) && sha256sum $(BINARY)-linux-* > SHA256SUMS

## test: run the full suite
.PHONY: test
test:
	go test ./...

## test-race: run under the race detector
##
## The engine publishes events from step goroutines while presenters consume
## them, so this is the target that actually exercises the bus.
.PHONY: test-race
test-race:
	CGO_ENABLED=1 go test -race ./...

## test-cover: run with coverage
.PHONY: test-cover
test-cover:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

## contract: run only the shared port contract suites
##
## Every adapter runs the same tests. This is the target to run after writing
## a new one.
.PHONY: contract
contract:
	go test -v ./test/suite/ -run Contract

## vet: go vet
.PHONY: vet
vet:
	go vet ./...

## lint: golangci-lint, including the depguard layering rules
.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint is not installed:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; }
	golangci-lint run

## fmt: format the tree
.PHONY: fmt
fmt:
	gofmt -w .

## fmt-check: fail when anything is unformatted
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "these files are not formatted:"; echo "$$unformatted"; exit 1; \
	fi

## tidy: tidy and verify the module graph
.PHONY: tidy
tidy:
	go mod tidy
	go mod verify

## check: everything CI runs
.PHONY: check
check: fmt-check vet test

## demo: initialise a throwaway installation under ./tmp/demo
##
## Exercises the real binary against the example bundle without touching /etc,
## which is what --root exists for.
.PHONY: demo
demo: build
	@rm -rf tmp/demo tmp/keys && mkdir -p tmp/keys
	@recovery=$$(./$(BINARY) --root tmp/keyscratch secret recipients generate-recovery-key tmp/keys/recovery.key 2>/dev/null); \
	./$(BINARY) --root tmp/demo init \
		--release ./testdata/bundle \
		--profile embedded \
		--domain demo.example \
		--recovery-recipient "$$recovery" \
		--install-units=false
	@echo
	@./$(BINARY) --root tmp/demo status
	@echo
	@./$(BINARY) --root tmp/demo doctor || true

## clean: remove build artifacts
.PHONY: clean
clean:
	rm -rf $(BINARY) $(DIST) coverage.out tmp

## help: list targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
