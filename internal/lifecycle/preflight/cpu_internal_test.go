package preflight

import "testing"

// What a cgroup quota rounds to is a decision, and rounding it the wrong way
// hides exactly the shortfall the check exists to report. Tested here rather
// than through availableCPUs, because that answer depends on how many cores
// the test machine happens to have.
func TestQuotaCPUs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
		ok      bool
	}{
		{"no quota in force", "max 100000\n", 0, false},
		{"exactly one CPU", "100000 100000\n", 1, true},
		{"four CPUs", "400000 100000\n", 4, true},
		// Down, not up: 1.5 CPUs does not give a product that asked for
		// two cores what it asked for, and rounding up would report
		// two.
		{"one and a half CPUs", "150000 100000\n", 1, true},
		// But never zero: half a CPU is not no CPU, and zero would fail
		// every requirement including `cpus: 1`.
		{"half a CPU", "50000 100000\n", 1, true},
		{"a tenth of a CPU", "10000 100000\n", 1, true},

		// Unparseable is not evidence of a narrower limit, so the
		// caller keeps the OS count rather than inventing one.
		{"empty", "", 0, false},
		{"one field", "100000\n", 0, false},
		{"three fields", "100000 100000 100000\n", 0, false},
		{"a non-numeric quota", "lots 100000\n", 0, false},
		{"a zero period", "100000 0\n", 0, false},
		{"a negative period", "100000 -100000\n", 0, false},
		{"a negative quota", "-100000 100000\n", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := quotaCPUs(tc.content)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("cpus = %d, want %d", got, tc.want)
			}
		})
	}
}
