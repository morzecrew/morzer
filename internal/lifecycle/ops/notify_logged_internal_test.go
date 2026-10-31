package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/logging"
	"github.com/morzecrew/morzer/internal/ports"
)

type failingNotifier struct{}

func (failingNotifier) Name() string { return "hook" }
func (failingNotifier) Notify(context.Context, events.Event) error {
	return errors.New("cannot reach the notify target hook")
}

// A target that refuses an event is logged, through the fan-out the CLI always
// wraps targets in. It was logged nowhere: the fan-out dropped the error, and
// the webhook does not log its own.
func TestAFailedNotifyTargetIsLogged(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)))

	d := &Deps{Notifier: ports.Notifiers{failingNotifier{}}}
	d.notify(ctx, events.Event{Kind: events.KindOperationFinished})

	if !strings.Contains(buf.String(), "cannot reach the notify target hook") {
		t.Errorf("the failed delivery was not logged; log:\n%s", buf.String())
	}
}
