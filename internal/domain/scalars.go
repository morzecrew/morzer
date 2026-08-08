package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Duration is time.Duration with YAML/JSON text marshalling, so manifests can
// say `10m` instead of a nanosecond count and JSON output stays readable.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string {
	if d == 0 {
		return "0s"
	}
	return time.Duration(d).String()
}

// Or returns d, or fallback when d is unset. Manifest timeouts are optional
// and every call site needs the same "zero means default" rule.
func (d Duration) Or(fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return time.Duration(d)
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Duration) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return ValidationError(err, "invalid duration %q", s).
			WithHint(`durations look like "30s", "10m" or "2h"`)
	}
	if parsed < 0 {
		return ValidationError(nil, "duration %q must not be negative", s)
	}
	*d = Duration(parsed)
	return nil
}

// FileMode is a Unix permission bitmask written in YAML as a quoted octal
// string ("0640"). Unquoted octal in YAML is a decimal-vs-octal trap, so the
// parser insists on a string and rejects anything with a mode bit set beyond
// the permission bits.
type FileMode uint32

func (m FileMode) Perm() uint32 { return uint32(m) & 0o7777 }

func (m FileMode) String() string { return fmt.Sprintf("%04o", uint32(m)) }

func (m FileMode) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// UnmarshalYAML refuses any node that is not a string, because YAML's own
// number rules rewrite the value before this type ever sees it.
//
// An unquoted `0640` is a YAML integer: it decodes to 416, arrives at
// UnmarshalText as "416", and parses back as 0416 -- owner read-only, group
// execute-only, other read/write. What makes that worse than a plain misread
// is that it is selective. A mode whose decimal form contains an 8 or a 9 is
// not a valid octal string, so it is refused loudly: 0600 -> "384", 0755 ->
// "493". Every mode whose decimal digits happen to all be octal digits passes
// silently -- 0400, 0640, 0644, 0660, 0664, 0770 and 0777, which is most of
// what anybody writes.
//
// The comment on the type has claimed since the beginning that "the parser
// insists on a string". This is the code that makes it true.
func (m *FileMode) UnmarshalYAML(unmarshal func(any) error) error {
	var raw any
	if err := unmarshal(&raw); err != nil {
		return err
	}
	s, ok := raw.(string)
	if !ok {
		return ValidationError(nil, "file mode must be a quoted octal string").
			WithHint(`quote it -- mode: "0640". Unquoted, YAML reads 0640 as the ` +
				`decimal number 416, which is the permission 0416`)
	}
	return m.UnmarshalText([]byte(s))
}

func (m *FileMode) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*m = 0
		return nil
	}
	parsed, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return ValidationError(err, "invalid file mode %q", s).
			WithHint(`modes are quoted octal strings, e.g. "0640"`)
	}
	if parsed > 0o7777 {
		return ValidationError(nil, "file mode %q sets bits outside the permission range", s)
	}
	*m = FileMode(parsed)
	return nil
}

// ByteSize accepts the IEC and SI spellings operators actually type: 4GiB,
// 512MB, 40G, or a bare byte count.
//
// A bare count is a YAML integer, so YAML's own base rules apply to it before
// this type sees anything: `010` is octal and arrives as 8, `0x10` is hex and
// arrives as 16. That is inherited, not chosen -- unlike FileMode, where the
// spelling in question is the one every manifest uses and refusing it was
// worth the guard. A zero-padded byte count is not a form anyone writes, and
// refusing integers here would break the bare count this type documents. It is
// pinned by test rather than fixed, so it is a known property instead of a
// surprise; the test is where the decision gets revisited if that changes.
type ByteSize int64

const (
	KiB = 1024
	MiB = 1024 * KiB
	GiB = 1024 * MiB
	TiB = 1024 * GiB
)

func (b ByteSize) Bytes() int64 { return int64(b) }

// String renders a size for a human.
//
// A value that divides evenly into a unit keeps its exact form, so a manifest
// saying `4GiB` round-trips as `4GiB`. Anything else -- free disk space, a
// backup size -- is rendered in the largest unit that leaves a number below
// 1024, with one decimal. Reporting 1066834032KiB free is technically correct
// and completely unreadable.
func (b ByteSize) String() string {
	if b == 0 {
		return "0"
	}
	if b < 0 {
		return "-" + (-b).String()
	}

	switch {
	case b%TiB == 0:
		return fmt.Sprintf("%dTiB", b/TiB)
	case b%GiB == 0:
		return fmt.Sprintf("%dGiB", b/GiB)
	case b%MiB == 0:
		return fmt.Sprintf("%dMiB", b/MiB)
	case b%KiB == 0 && b < MiB:
		return fmt.Sprintf("%dKiB", b/KiB)
	}

	for _, unit := range []struct {
		size   ByteSize
		suffix string
	}{{TiB, "TiB"}, {GiB, "GiB"}, {MiB, "MiB"}, {KiB, "KiB"}} {
		if b >= unit.size {
			return fmt.Sprintf("%.1f%s", float64(b)/float64(unit.size), unit.suffix)
		}
	}
	return strconv.FormatInt(int64(b), 10)
}

// byteUnits is ordered longest-suffix-first so "GiB" matches before "G".
var byteUnits = []struct {
	suffix string
	mult   int64
}{
	{"KiB", KiB}, {"MiB", MiB}, {"GiB", GiB}, {"TiB", TiB},
	{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
	{"K", KiB}, {"M", MiB}, {"G", GiB}, {"T", TiB},
	{"B", 1},
}

func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

func (b *ByteSize) UnmarshalText(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		*b = 0
		return nil
	}
	upper := strings.ToUpper(s)
	for _, u := range byteUnits {
		if !strings.HasSuffix(upper, strings.ToUpper(u.suffix)) {
			continue
		}
		numPart := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
		n, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			return ValidationError(err, "invalid size %q", s)
		}
		if n < 0 {
			return ValidationError(nil, "size %q must not be negative", s)
		}
		// Bounded before the int64 conversion, which would otherwise wrap
		// an absurd size into a huge negative -- and a negative requirement
		// is one every "is there enough?" check trivially satisfies.
		// ParseFloat also accepts "NaN" and "Inf", which no size is.
		//
		// >= rather than >: float64(MaxInt64) rounds up to exactly 2^63,
		// so a value sitting exactly on the quotient (2^53 KiB) passes a
		// > check and still converts to a negative.
		if math.IsNaN(n) || n >= float64(math.MaxInt64)/float64(u.mult) {
			return ValidationError(nil, "size %q is out of range", s)
		}
		*b = ByteSize(n * float64(u.mult))
		return nil
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return ValidationError(err, "invalid size %q", s).
			WithHint(`sizes look like "4GiB", "512MB" or a plain byte count`)
	}
	if n < 0 {
		return ValidationError(nil, "size %q must not be negative", s)
	}
	*b = ByteSize(n)
	return nil
}
