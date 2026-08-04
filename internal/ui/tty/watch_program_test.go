//go:build !race

package tty_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/tty"
)

// `status --watch` owns a Bubble Tea program with an alt screen, and tearing
// one down races inside the library rather than inside this package:
// `cancelreader.epollCancelReader.Close` closes the input file while its own
// wait goroutine is calling `File.Fd()` on it. The detector reports it on
// every shutdown of a program that was given an input.
//
// So these are excluded from the race build rather than removed. What is under
// test here is the lifecycle -- that a cancelled watch ends and reports
// nothing, that a failed refresh is displayed rather than returned -- and it is
// worth having. The model's own behaviour is covered in watch_test.go, which
// drives it without a program and runs under -race with everything else.
//
// If bubbletea fixes this, delete the build tag and fold these back in.

// TestWatchEndsWhenItsContextDoes. `status --watch` is bound to its context so
// Ctrl-C simply ends it: there is no work in flight a torn-down display could
// orphan.
func TestWatchEndsWhenItsContextDoes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- tty.Watch(ctx, tty.WatchOptions{
			Output:   &bytes.Buffer{},
			Input:    pipeInput(t),
			Theme:    theme.New(false, false),
			Interval: 50 * time.Millisecond,
			Refresh: func(context.Context) (ops.Status, error) {
				return ops.Status{Product: "demo"}, nil
			},
		})
	}()

	select {
	case err := <-done:
		// A cancelled context is the caller's own story; reporting
		// "program was killed" on top of it would obscure the exit code.
		if err != nil {
			t.Errorf("a cancelled watch returned an error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the watch outlived its context, so Ctrl-C does not end it")
	}
}

// TestWatchReportsARefreshThatFails rather than showing a stale screen as
// though it were current.
func TestWatchReportsARefreshThatFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	err := tty.Watch(ctx, tty.WatchOptions{
		Output:   &out,
		Input:    pipeInput(t),
		Theme:    theme.New(false, false),
		Interval: 50 * time.Millisecond,
		Refresh: func(context.Context) (ops.Status, error) {
			return ops.Status{}, errors.New("cannot reach the daemon")
		},
	})
	if err != nil {
		t.Errorf("a watch whose refresh failed returned an error rather than "+
			"displaying one: %v", err)
	}
	if strings.Contains(out.String(), "demo") {
		t.Error("the watch showed a snapshot it never received")
	}
}
