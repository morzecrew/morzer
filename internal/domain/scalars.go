package domain

import (
	"fmt"
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
type ByteSize int64

const (
	KiB = 1024
	MiB = 1024 * KiB
	GiB = 1024 * MiB
	TiB = 1024 * GiB
)

func (b ByteSize) Bytes() int64 { return int64(b) }

func (b ByteSize) String() string {
	switch {
	case b == 0:
		return "0"
	case b%TiB == 0:
		return fmt.Sprintf("%dTiB", b/TiB)
	case b%GiB == 0:
		return fmt.Sprintf("%dGiB", b/GiB)
	case b%MiB == 0:
		return fmt.Sprintf("%dMiB", b/MiB)
	case b%KiB == 0:
		return fmt.Sprintf("%dKiB", b/KiB)
	default:
		return strconv.FormatInt(int64(b), 10)
	}
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
