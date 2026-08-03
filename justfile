# morzer — build, test and release recipes.
#
# Run `just` with no arguments to list everything.

binary  := "morzer"
cmd     := "./cmd/morzer"
dist    := "dist"

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit  := `git rev-parse --short HEAD 2>/dev/null || echo unknown`
date    := `date -u +%Y-%m-%dT%H:%M:%SZ`

ldflags := "-s -w -X main.version=" + version + " -X main.commit=" + commit + " -X main.date=" + date

export CGO_ENABLED := "0"

# List the available recipes.
default:
    @just --list --unsorted

# -trimpath and CGO_ENABLED=0 above are reproducibility requirements: the
# binary must be a single static file with no runtime dependency on the build
# machine's libc.

# Compile the binary for the host platform.
build:
    go build -trimpath -ldflags "{{ldflags}}" -o {{binary}} {{cmd}}

# Cross-compile for the supported targets, with checksums.
build-all:
    mkdir -p {{dist}}
    GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "{{ldflags}}" -o {{dist}}/{{binary}}-linux-amd64 {{cmd}}
    GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "{{ldflags}}" -o {{dist}}/{{binary}}-linux-arm64 {{cmd}}
    cd {{dist}} && sha256sum {{binary}}-linux-* > SHA256SUMS

# Run the full test suite.
test *args:
    go test {{args}} ./...

# Every adapter runs the same tests against the same suite, so this is the
# recipe to run after writing a new one.

# Run only the shared port contract suites.
contract:
    go test -v ./test/suite/ -run Contract

# A skipped real-adapter suite means the production adapter was never exercised
# and the fake carried the run alone. That is the failure this recipe exists to
# catch, so absence of a skip is asserted rather than assumed.

# Run the contract suites and fail if any of them skipped.
contract-strict:
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(mktemp)
    trap 'rm -f "$out"' EXIT
    go test ./test/suite/ -run Contract -v 2>&1 | tee "$out"
    if grep -q -- '--- SKIP' "$out"; then
        echo >&2
        echo "error: a contract suite skipped, so a real adapter went unexercised." >&2
        echo "       install the missing tool (sops) and re-run." >&2
        exit 1
    fi

# The engine publishes events from step goroutines while presenters consume
# them, so this is the recipe that actually exercises the bus.

# Run under the race detector.
test-race:
    CGO_ENABLED=1 go test -race ./...

# -coverpkg attributes coverage to the package whose statements ran, not the
# package whose test ran. Without it the integration suite gets no credit for
# exercising ops and the adapters, and the total reads 8.7% instead of 45%.

# Run the tests with coverage and print the total.
test-cover:
    go test -coverpkg=./internal/... -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -func=coverage.out | tail -1

# Fail when total coverage drops below the floor.
coverage-gate floor="45": test-cover
    .github/scripts/coverage-floor.sh coverage.out {{floor}}

# Open the coverage report in a browser.
cover-html: test-cover
    go tool cover -html=coverage.out

# go vet.
vet:
    go vet ./...

# golangci-lint, including the depguard layering rules.
lint:
    #!/usr/bin/env sh
    if ! command -v golangci-lint >/dev/null 2>&1; then
        echo "golangci-lint is not installed:"
        echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"
        exit 1
    fi
    golangci-lint run

# Format the tree.
fmt:
    gofmt -w .

# Fail when anything is unformatted.
fmt-check:
    #!/usr/bin/env sh
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
        echo "these files are not formatted:"
        echo "$unformatted"
        exit 1
    fi

# Tidy and verify the module graph.
tidy:
    go mod tidy
    go mod verify

# Everything CI runs.
check: fmt-check vet test

# `check` deliberately omits lint and the strict contract run so it works
# without golangci-lint or sops installed. This recipe is the exact-parity one:
# if it passes, CI passes.

# Run exactly what CI runs. Needs golangci-lint and sops.
ci: fmt-check vet lint contract-strict test-race coverage-gate

# Exercises the real binary against the example bundle without touching /etc,
# which is what the hidden --root flag exists for.

# Initialise a throwaway installation under ./tmp and show it off.
demo: build
    #!/usr/bin/env sh
    set -e
    rm -rf tmp/demo tmp/keys
    mkdir -p tmp/keys

    printf '\n\033[1m== generating an offline recovery key ==\033[0m\n'
    recovery=$(./{{binary}} secret recipients generate-recovery-key tmp/keys/recovery.key)
    echo "recovery recipient: $recovery"

    printf '\n\033[1m== init ==\033[0m\n'
    ./{{binary}} --root tmp/demo init \
        --release ./testdata/bundle \
        --profile embedded \
        --domain demo.example \
        --recovery-recipient "$recovery" \
        --install-units=false

    printf '\n\033[1m== status ==\033[0m\n'
    ./{{binary}} --root tmp/demo status

    printf '\n\033[1m== doctor ==\033[0m\n'
    ./{{binary}} --root tmp/demo doctor || true

    printf '\n\033[1m== secret list ==\033[0m\n'
    ./{{binary}} --root tmp/demo secret list

    printf '\ninstallation is under ./tmp/demo — poke at it with:\n'
    printf '  ./{{binary}} --root tmp/demo <command>\n'

# Show what `apply` would do, without doing it.
demo-plan: demo
    @printf '\n\033[1m== apply --dry-run ==\033[0m\n'
    ./{{binary}} --root tmp/demo apply --dry-run

# Show the machine-readable output contract.
demo-json: demo
    @printf '\n\033[1m== status --json ==\033[0m\n'
    ./{{binary}} --root tmp/demo status --json
    @printf '\n\033[1m== doctor --json (summary) ==\033[0m\n'
    ./{{binary}} --root tmp/demo doctor --json | jq '{ok, exit_code, worst: .data.worst, summary: .data.summary}'

# Validate the example release bundle.
verify-bundle:
    ./{{binary}} release verify ./testdata/bundle

# Remove build artifacts and the demo installation.
clean:
    rm -rf {{binary}} {{dist}} coverage.out tmp
