package suite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/notify/webhook"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The outbox and the scheduled doctor (RFC 0043 P2): what a target did not
// accept is kept, and sent later, once, by whichever sender holds the outbox.

// hook is a webhook endpoint answering from a script: each request takes the
// next status, and the last repeats.
type hook struct {
	srv *httptest.Server

	mu       sync.Mutex
	statuses []int
	got      []hookRequest
}

type hookRequest struct {
	ID, Key, Kind string
	Status        int
}

func newHook(t *testing.T, statuses ...int) *hook {
	t.Helper()
	h := &hook{statuses: statuses}
	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&ev)
		h.mu.Lock()
		status := h.statuses[0]
		if len(h.statuses) > 1 {
			h.statuses = h.statuses[1:]
		}
		h.got = append(h.got, hookRequest{ID: ev.ID, Key: r.Header.Get("Idempotency-Key"), Kind: ev.Kind, Status: status})
		h.mu.Unlock()
		if status >= 300 && status < 400 {
			w.Header().Set("Location", "https://elsewhere.example/")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// answer makes every later request get status.
func (h *hook) answer(status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses = []int{status}
}

func (h *hook) requests() []hookRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookRequest(nil), h.got...)
}

func (h *hook) target(t *testing.T, name, minLevel string) ports.Notifier {
	t.Helper()
	n, err := webhook.New(webhook.Options{
		Name: name, URL: h.srv.URL, Client: h.srv.Client(), MinLevel: minLevel, RetryAfter: time.Millisecond,
	})
	require.NoError(t, err)
	return n
}

func outboxFiles(t *testing.T, h *harness) []string {
	t.Helper()
	files, err := os.ReadDir(h.Deps.Paths.NotifyOutboxDir())
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, f := range files {
		names = append(names, f.Name())
	}
	return names
}

// configure records a target in the installation, as `notify target add` does:
// an entry whose target is not configured is not sent but dropped.
func configure(t *testing.T, h *harness, names ...string) {
	t.Helper()
	for _, name := range names {
		_, err := ops.NotifyTargetAdd(context.Background(), h.Deps, ops.NotifyTargetAddOptions{
			Target: domain.NotifyTargetConfig{Name: name, URL: "https://hooks.example/" + name},
		})
		require.NoError(t, err)
	}
}

// finishAnOperation runs one operation, whose outcome is notified. Each call
// sets another level, since a `config set` that changes nothing runs nothing.
func finishAnOperation(t *testing.T, h *harness) {
	t.Helper()
	inst, err := h.Deps.State.LoadInstallation(context.Background())
	require.NoError(t, err)
	level := "debug"
	if inst.Parameters["log_level"] == "debug" {
		level = "warn"
	}
	_, err = ops.ConfigSet(context.Background(), h.Deps, ops.ConfigSetOptions{Set: map[string]string{"log_level": level}})
	require.NoError(t, err)
}

func entries(t *testing.T, h *harness) int {
	t.Helper()
	n := 0
	for _, name := range outboxFiles(t, h) {
		if len(name) > 5 && name[len(name)-5:] == ".json" {
			n++
		}
	}
	return n
}

// A target that is down keeps the event, and `notify flush` delivers it once
// the target answers, with the same id and Idempotency-Key.
func TestADownTargetsEventIsKeptAndFlushedOnce(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	down := newHook(t, http.StatusServiceUnavailable)
	configure(t, h, "chat")
	h.Deps.Notifier = ports.Notifiers{down.target(t, "chat", "")}

	finishAnOperation(t, h)
	sent := down.requests()
	require.Len(t, sent, 2, "a 503 is tried once more, and only once")
	require.Equal(t, 1, entries(t, h), "the event the target did not accept was not kept")

	// Still down: one entry is tried, with its retry, and the next is not.
	finishAnOperation(t, h)
	require.Equal(t, 2, entries(t, h))
	before := len(down.requests())
	report, err := ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 2, report.Kept)
	assert.Len(t, down.requests(), before+2, "a target that is down was tried for every entry")

	down.answer(http.StatusNoContent)
	report, err = ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 2, report.Sent)
	got := down.requests()
	first := got[len(got)-2]
	assert.Equal(t, sent[0].ID, first.ID, "the oldest was not sent first, or carries another id")
	assert.Equal(t, sent[0].ID, first.Key)
	assert.Zero(t, entries(t, h), "a delivered entry was kept")

	total := len(got)
	report, err = ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Zero(t, report.Sent, "an entry was sent twice")
	assert.Len(t, down.requests(), total)
}

// One sender at a time: while another holds the outbox, a flush sends nothing,
// and the target hears the event once (decision 11).
func TestASecondSenderSendsNothing(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	target := newHook(t, http.StatusServiceUnavailable)
	configure(t, h, "chat")
	h.Deps.Notifier = ports.Notifiers{target.target(t, "chat", "")}
	finishAnOperation(t, h)
	target.answer(http.StatusNoContent)
	before := len(target.requests())

	release, err := h.Deps.Locker.Acquire(ctx, "notify-outbox", ports.LockOptions{Owner: ports.LockOwner{PID: os.Getpid()}})
	require.NoError(t, err)
	report, err := ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.True(t, report.Busy)
	assert.Len(t, target.requests(), before, "a second sender sent")
	require.NoError(t, release())

	report, err = ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Sent)
	assert.Len(t, target.requests(), before+1)
}

// A 302 is not retried at once, is kept, and is delivered once the URL answers
// (decision 13). A 400 is final: not retried, not kept. A 401 is kept, and
// delivered once the target accepts.
func TestWhichAnswersAreKept(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		kept     bool
		attempts int
	}{
		"a redirect":     {http.StatusFound, true, 1},
		"a bad request":  {http.StatusBadRequest, false, 1},
		"a refused auth": {http.StatusUnauthorized, true, 1},
	} {
		t.Run(name, func(t *testing.T) {
			h := appliedHarness(t)
			target := newHook(t, tc.status)
			configure(t, h, "chat")
			h.Deps.Notifier = ports.Notifiers{target.target(t, "chat", "")}

			finishAnOperation(t, h)
			assert.Len(t, target.requests(), tc.attempts)
			if !tc.kept {
				assert.Zero(t, entries(t, h), "a final answer was kept")
				return
			}
			require.Equal(t, 1, entries(t, h))
			target.answer(http.StatusOK)
			report, err := ops.NotifyFlush(context.Background(), h.Deps, ops.Options{})
			require.NoError(t, err)
			assert.Equal(t, 1, report.Sent)
			assert.Zero(t, entries(t, h))
		})
	}
}

// With two targets and one down, only that one's entry is written, and the
// other hears the event once. A send to the down one keeps it.
func TestOnlyTheDownTargetKeepsTheEvent(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	up := newHook(t, http.StatusNoContent)
	down := newHook(t, http.StatusServiceUnavailable)
	configure(t, h, "up", "down")
	h.Deps.Notifier = ports.Notifiers{up.target(t, "up", ""), down.target(t, "down", "")}

	finishAnOperation(t, h)
	assert.Len(t, up.requests(), 1)
	require.Equal(t, 1, entries(t, h))

	report, err := ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Kept)
	assert.Len(t, up.requests(), 1, "the target that accepted heard the event again")
	assert.Equal(t, 1, entries(t, h))

	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "warn", findCheck(t, doctor, "notify.outbox").Status, "doctor did not warn about the waiting event")
}

// `notify target remove` takes the target's entries with it.
func TestRemovingATargetForgetsItsEntries(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	_, err := ops.NotifyTargetAdd(ctx, h.Deps, ops.NotifyTargetAddOptions{
		Target: domain.NotifyTargetConfig{Name: "chat", URL: "https://hooks.example/morzer"},
	})
	require.NoError(t, err)
	down := newHook(t, http.StatusServiceUnavailable)
	h.Deps.Notifier = ports.Notifiers{down.target(t, "chat", "")}
	finishAnOperation(t, h)
	require.Equal(t, 1, entries(t, h))

	_, err = ops.NotifyTargetRemove(ctx, h.Deps, ops.Options{}, "chat")
	require.NoError(t, err)
	assert.Zero(t, entries(t, h), "the removed target's entries were kept")

	// An entry left with no target at all is still reported.
	require.NoError(t, os.WriteFile(h.Deps.Paths.NotifyOutboxDir()+"/evt_0.0000000000000000.json",
		[]byte(`{"target":"chat","event":{"id":"evt_0"}}`), 0o600))
	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "warn", findCheck(t, doctor, "notify.outbox").Status, "an entry nobody will send went unreported")
}

// The scheduled run notifies one doctor.finished, at the level of the worst
// check, naming the checks that are not ok.
func TestTheScheduledDoctorNotifiesWhatFails(t *testing.T) {
	h := appliedHarness(t)
	rec := &recordingNotifier{}
	h.Deps.Notifier = rec
	h.Runtime.Fail = map[string]error{"Status": domain.RuntimeError(errInjected, "cannot read service status")}

	report, err := ops.DoctorUnattended(context.Background(), h.Deps)
	require.NoError(t, err)
	require.Equal(t, events.CheckFail, report.Worst)

	var finished []events.Event
	for _, ev := range rec.events {
		if ev.Kind == events.KindDoctorFinished {
			finished = append(finished, ev)
		}
	}
	require.Len(t, finished, 1, "want one doctor.finished per run")
	assert.Equal(t, events.LevelError, finished[0].Level)
	ids := map[string]bool{}
	for _, c := range finished[0].Checks {
		ids[c.ID] = true
		assert.NotEqual(t, events.CheckOK, c.Status, "an ok check was named")
	}
	assert.True(t, ids["runtime.services"], "the failing check was not named: %+v", finished[0].Checks)
	assert.NotEmpty(t, finished[0].ID, "the event has no id to deduplicate by")
}

// The operation holding the deployment lock is running, not unfinished; a
// record a dead process left is reported (decision 12).
func TestTheLockHoldersRecordIsNotUnfinished(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	require.NoError(t, h.Deps.State.AppendOperation(ctx, domain.OperationRecord{
		ID: "op-live", Type: domain.OpTypeBackup, Status: domain.StatusRunning, StartedAt: domain.NewTime(time.Now()),
	}))

	release, err := h.Deps.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{PID: os.Getpid(), OperationID: "op-live", Type: "backup"},
	})
	require.NoError(t, err)
	doctor, err := ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "ok", findCheck(t, doctor, "config.operations").Status, "a running backup was reported unfinished")
	require.NoError(t, release())

	doctor, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "fail", findCheck(t, doctor, "config.operations").Status, "a record nobody holds was not reported")

	// Another operation's lock does not excuse this record.
	release, err = h.Deps.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{PID: os.Getpid(), OperationID: "op-other", Type: "apply"},
	})
	require.NoError(t, err)
	doctor, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, "fail", findCheck(t, doctor, "config.operations").Status, "another operation's lock hid a dead record")
	require.NoError(t, release())

	// One that needs attention is reported whoever holds the lock.
	require.NoError(t, h.Deps.State.AppendOperation(ctx, domain.OperationRecord{
		ID: "op-stuck", Type: domain.OpTypeUpdate, Status: domain.StatusManualIntervention, StartedAt: domain.NewTime(time.Now()),
	}))
	release, err = h.Deps.Locker.Acquire(ctx, "deployment", ports.LockOptions{
		Owner: ports.LockOwner{PID: os.Getpid(), OperationID: "op-stuck", Type: "update"},
	})
	require.NoError(t, err)
	defer func() { _ = release() }()
	doctor, err = ops.Doctor(ctx, h.Deps)
	require.NoError(t, err)
	check := findCheck(t, doctor, "config.operations")
	assert.Equal(t, "fail", check.Status)
	assert.Contains(t, check.Message, "op-stuck", "a record needing attention was skipped for holding the lock")
}

// The doctor timer exists while a notify target does, goes with
// `doctor.scheduled=false`, and takes its OnCalendar from `doctor.schedule`
// (decision 4).
func TestTheDoctorTimerFollowsTheTargetsAndTheSettings(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	h.Supervisor.Present = true
	h.Deps.Supervisor = h.Supervisor
	units, err := h.Supervisor.Units(ports.UnitParams{Product: "demo"})
	require.NoError(t, err)
	require.NoError(t, h.Supervisor.InstallUnits(ctx, units, ports.EnableAll))
	timer := "demo-doctor.timer"
	require.NotContains(t, h.Supervisor.Installed, timer, "a machine with nobody to tell got a doctor timer")

	_, err = ops.NotifyTargetAdd(ctx, h.Deps, ops.NotifyTargetAddOptions{
		Target: domain.NotifyTargetConfig{Name: "chat", URL: "https://hooks.example/morzer"},
	})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, timer, "a target was added and nothing runs doctor for it")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"doctor.scheduled": "false"}})
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, timer, "doctor.scheduled=false kept the timer")

	_, err = ops.SetSettings(ctx, h.Deps, ops.SetSettingsOptions{Set: map[string]string{"doctor.scheduled": "true"}})
	require.NoError(t, err)
	assert.Contains(t, h.Supervisor.Installed, timer)
	_, err = ops.NotifyTargetRemove(ctx, h.Deps, ops.Options{}, "chat")
	require.NoError(t, err)
	assert.NotContains(t, h.Supervisor.Installed, timer, "the timer outlived the last target")
}

// An entry waits while its target is configured and cannot be built this run --
// a secret that will not decrypt drops it from the notifiers -- and goes only
// once the target is no longer configured.
func TestAnEntryWaitsForATargetThatCannotBeBuilt(t *testing.T) {
	h := appliedHarness(t)
	ctx := context.Background()
	_, err := ops.NotifyTargetAdd(ctx, h.Deps, ops.NotifyTargetAddOptions{
		Target: domain.NotifyTargetConfig{Name: "chat", URL: "https://hooks.example/morzer"},
	})
	require.NoError(t, err)
	down := newHook(t, http.StatusServiceUnavailable)
	h.Deps.Notifier = ports.Notifiers{down.target(t, "chat", "")}
	finishAnOperation(t, h)
	require.Equal(t, 1, entries(t, h))

	h.Deps.Notifier = nil
	report, err := ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, entries(t, h), "an entry was deleted because its target could not be built this run")
	assert.Equal(t, 1, report.Kept)

	// No longer configured, by a route other than `notify target remove`.
	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	inst.Notify.Targets = nil
	require.NoError(t, h.Deps.State.SaveInstallation(ctx, inst))
	report, err = ops.NotifyFlush(ctx, h.Deps, ops.Options{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Orphaned)
	assert.Zero(t, entries(t, h))
}

// `notify flush --dry-run` says what is waiting and sends nothing.
func TestADryRunFlushSendsNothing(t *testing.T) {
	h := appliedHarness(t)
	target := newHook(t, http.StatusServiceUnavailable)
	configure(t, h, "chat")
	h.Deps.Notifier = ports.Notifiers{target.target(t, "chat", "")}
	finishAnOperation(t, h)
	target.answer(http.StatusNoContent)
	before := len(target.requests())

	report, err := ops.NotifyFlush(context.Background(), h.Deps, ops.Options{DryRun: true})
	require.NoError(t, err)
	assert.Len(t, target.requests(), before, "a dry run sent")
	assert.Equal(t, 1, entries(t, h), "a dry run deleted an entry")
	assert.Equal(t, 1, report.Waiting)
	assert.Contains(t, report.Summary(), "would send 1")
}
