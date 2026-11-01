package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/infra/lock"
)

// Past 100 entries a target drops its oldest, and doctor says how many it holds
// and how many it dropped (RFC 0043 §5.2).
func TestTheOutboxDropsTheOldestPastItsBound(t *testing.T) {
	paths := domain.PathsUnder(t.TempDir(), "demo")
	d := &Deps{Paths: paths, Locker: lock.New(paths.LockDir())}
	for i := range outboxBound + 2 {
		ev := events.Event{ID: fmt.Sprintf("evt_%026d", i), Kind: events.KindOperationFinished}
		if err := d.writeOutboxEntry("chat", ev); err != nil {
			t.Fatal(err)
		}
	}
	names, err := outboxNames(d.Paths.NotifyOutboxDir(), outboxToken("chat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != outboxBound {
		t.Fatalf("the target keeps %d entries, want %d", len(names), outboxBound)
	}
	if want := fmt.Sprintf("evt_%026d", 2); names[0][:len(want)] != want {
		t.Errorf("the oldest kept is %s, want the two oldest dropped", names[0])
	}

	res := d.checkOutbox().Run(context.Background())
	if res.Status != events.CheckWarn {
		t.Fatalf("notify.outbox is %s with entries waiting", res.Status)
	}
	if want := "chat holds 100 and dropped 2"; !strings.Contains(res.Message, want) {
		t.Errorf("notify.outbox says %q, want it to contain %q", res.Message, want)
	}

	if err := d.forgetTarget(context.Background(), "chat"); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(d.Paths.NotifyOutboxDir())
	if len(files) != 0 {
		t.Errorf("forgetting the target left %d files", len(files))
	}
}

// A run where every check passes notifies nothing; one that warns notifies at
// warn, and one that fails at error (decision 6).
func TestDoctorFinishedIsSentOnlyWhenACheckIsNotOK(t *testing.T) {
	ok := events.CheckResult{ID: "a", Status: events.CheckOK}
	warn := events.CheckResult{ID: "b", Status: events.CheckWarn, Remedy: "do b"}
	fail := events.CheckResult{ID: "c", Status: events.CheckFail}

	if _, sent := doctorFinished("demo", []events.CheckResult{ok, ok}); sent {
		t.Error("a run where every check passed notified")
	}
	ev, sent := doctorFinished("demo", []events.CheckResult{ok, warn})
	if !sent || ev.Level != events.LevelWarn || len(ev.Checks) != 1 || ev.Checks[0].Remedy != "do b" {
		t.Errorf("a warning run sent %v: %+v", sent, ev)
	}
	ev, _ = doctorFinished("demo", []events.CheckResult{warn, fail})
	if ev.Level != events.LevelError || len(ev.Checks) != 2 {
		t.Errorf("a failing run is at %s with %d checks", ev.Level, len(ev.Checks))
	}
}

// The doctor pair is asked for while a target is configured and the setting is
// not off, and with the schedule the installation records (decisions 4 and 8).
func TestTheDoctorUnitParamsFollowTheInstallation(t *testing.T) {
	d := &Deps{}
	inst := domain.Installation{Product: "demo"}
	inst.Policy.DoctorSchedule = "Mon *-*-* 07:00:00"
	if p := d.unitParams(inst); p.DoctorTimer {
		t.Error("a machine with nobody to tell asked for a doctor timer")
	}
	inst.Notify.Targets = []domain.NotifyTargetConfig{{Name: "chat", URL: "https://hooks.example/a"}}
	p := d.unitParams(inst)
	if !p.DoctorTimer || p.DoctorSchedule != "Mon *-*-* 07:00:00" {
		t.Errorf("timer %t schedule %q, want the timer on the recorded schedule", p.DoctorTimer, p.DoctorSchedule)
	}
	inst.Policy.SkipScheduledDoctor = true
	if d.unitParams(inst).DoctorTimer {
		t.Error("doctor.scheduled=false kept the timer")
	}
}
