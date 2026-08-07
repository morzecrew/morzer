// Command morzer manages the lifecycle of a self-hosted product on a single
// Linux machine running Docker Compose.
//
// This file is deliberately minimal: it installs signal handling, hands off to
// the CLI layer, and exits with whatever code that layer resolved. Every other
// decision lives behind an interface somewhere below.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/morzecrew/morzer/internal/cli"
)

// Stamped at link time:
//
//	go build -ldflags "-X main.version=1.0.0 -X main.commit=abc123 -X main.date=..."
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// SIGINT and SIGTERM cancel the root context. Cancellation propagates
	// through every port method to the child process groups, and the
	// operation finishes as `interrupted` with a diagnosable state rather
	// than being killed midway through a migration.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Once the first signal has cancelled the context, the default
	// disposition comes back: a second Ctrl-C then terminates the process
	// the ordinary way. Without this every further signal is swallowed, and
	// the operator watching a wedged teardown -- compensation deliberately
	// keeps running under WithoutCancel -- has no escalation short of
	// `kill -9`, which skips the final journal write this way still gets a
	// chance at.
	go func() {
		<-ctx.Done()
		stop()
	}()

	code := cli.Execute(ctx, cli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	}, os.Args[1:])

	os.Exit(code)
}
