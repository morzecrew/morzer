package systemd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/ports"
)

// `Units` copies ports.UnitParams into this package's own UnitParams field by
// field, and a field added to one and not the other is dropped in silence: the
// rendering reads its zero value, which for `SkipBackupTimer` is a machine that
// keeps a backup timer it declared it did not want, and for `BackupSchedule` is
// somebody's maintenance window replaced by the default.
//
// Nothing about a hand-written conversion fails when a field is missing from
// it. What a test can see without knowing what each field means is that the two
// structs still describe the same thing, which is where the omission starts.
func TestTheTwoUnitParamsCarryTheSameFields(t *testing.T) {
	fields := func(v any) map[string]reflect.Type {
		rt := reflect.TypeOf(v)
		out := make(map[string]reflect.Type, rt.NumField())
		for i := range rt.NumField() {
			f := rt.Field(i)
			out[f.Name] = f.Type
		}
		return out
	}

	port := fields(ports.UnitParams{})
	adapter := fields(UnitParams{})

	for name, typ := range port {
		got, ok := adapter[name]
		if !ok {
			t.Errorf("ports.UnitParams.%s has no counterpart here, so `Units` "+
				"cannot be passing it on", name)
			continue
		}
		if got != typ {
			t.Errorf("%s is %s in the port and %s here", name, typ, got)
		}
	}
	for name := range adapter {
		if _, ok := port[name]; !ok {
			t.Errorf("UnitParams.%s exists only in this package, so nothing "+
				"upstream can set it", name)
		}
	}
}

// And that the copy actually happens: every field set to a non-zero value has
// to reach the rendered units.
//
// The field-set test above catches a field added to one struct and not the
// other. It cannot catch the likelier slip -- both structs updated, the line in
// `Units` forgotten -- so this drives the conversion with everything set and
// asserts on what comes out.
func TestUnitsCarriesEveryFieldIntoTheRendering(t *testing.T) {
	s := New(nil, WithUnitDir(t.TempDir()))

	units, err := s.Units(ports.UnitParams{
		Product:        "demo",
		ManagerPath:    "/opt/morzer/bin/morzer",
		ConfigPath:     "/etc/demo/config.yaml",
		Description:    "a description nothing else would produce",
		BackupSchedule: "Mon *-*-* 04:00:00",
		UpdateSchedule: "Tue *-*-* 05:00:00",
		UpdateTimer:    true,
		FleetSchedule:  "Wed *-*-* 06:00:00",
		FleetTimer:     true,
		// Not set here: it is the one field whose effect is a unit's
		// *absence*, which the case below asserts instead.
	})
	if err != nil {
		t.Fatal(err)
	}

	all := ""
	names := map[string]bool{}
	for _, u := range units {
		all += string(u.Contents)
		names[u.Name] = true
	}

	for _, want := range []string{
		"/opt/morzer/bin/morzer",
		"/etc/demo/config.yaml",
		"a description nothing else would produce",
		"Mon *-*-* 04:00:00",
		"Tue *-*-* 05:00:00",
		"Wed *-*-* 06:00:00",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("%q reached no unit, so `Units` dropped it on the way in", want)
		}
	}
	for _, want := range []string{
		"demo.service", "demo-backup.timer", "demo-update.timer", "demo-fleet.timer",
	} {
		if !names[want] {
			t.Errorf("%s was not rendered", want)
		}
	}

	skipped, err := s.Units(ports.UnitParams{Product: "demo", SkipBackupTimer: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range skipped {
		if u.Name == "demo-backup.timer" || u.Name == "demo-backup.service" {
			t.Errorf("SkipBackupTimer did not reach the rendering: %s was built", u.Name)
		}
	}
}
