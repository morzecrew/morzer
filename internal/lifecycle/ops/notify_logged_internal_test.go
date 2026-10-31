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

type recordingNotifier struct{ got []events.Event }

func (r *recordingNotifier) Name() string { return "rec" }
func (r *recordingNotifier) Notify(_ context.Context, ev events.Event) error {
	r.got = append(r.got, ev)
	return nil
}

// Every forwarded event carries an id a receiver can drop a redelivery by
// (RFC 0043 decision 3), and one that already has an id keeps it.
func TestAForwardedEventCarriesAnID(t *testing.T) {
	rec := &recordingNotifier{}
	d := &Deps{Notifier: rec}

	d.notify(context.Background(), events.Event{Kind: events.KindOperationFinished})
	d.notify(context.Background(), events.Event{Kind: events.KindOperationFinished})
	d.notify(context.Background(), events.Event{Kind: events.KindOperationFinished, ID: "evt_KEPT"})

	if len(rec.got) != 3 {
		t.Fatalf("delivered %d events, want 3", len(rec.got))
	}
	if !strings.HasPrefix(rec.got[0].ID, "evt_") || rec.got[0].ID == rec.got[1].ID {
		t.Errorf("ids %q and %q: want two distinct evt_ ids", rec.got[0].ID, rec.got[1].ID)
	}
	if rec.got[2].ID != "evt_KEPT" {
		t.Errorf("an event's own id was replaced with %q", rec.got[2].ID)
	}
}
