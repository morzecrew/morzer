package domain

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Version is a semantic version. It is a distinct type so a release version,
// a tool version, and a schema number cannot be confused at a call site.
type Version struct {
	sv *semver.Version
}

// ParseVersion accepts both "1.2.0" and "v1.2.0".
func ParseVersion(s string) (Version, error) {
	sv, err := semver.NewVersion(strings.TrimSpace(s))
	if err != nil {
		return Version{}, ValidationError(err, "invalid version %q", s).
			WithHint("versions must be semantic, e.g. 1.2.0")
	}
	return Version{sv: sv}, nil
}

// MustParseVersion is for constants and tests only.
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

func (v Version) IsZero() bool { return v.sv == nil }

func (v Version) String() string {
	if v.sv == nil {
		return ""
	}
	return v.sv.String()
}

func (v Version) Compare(o Version) int {
	switch {
	case v.sv == nil && o.sv == nil:
		return 0
	case v.sv == nil:
		return -1
	case o.sv == nil:
		return 1
	}
	return v.sv.Compare(o.sv)
}

func (v Version) LessThan(o Version) bool    { return v.Compare(o) < 0 }
func (v Version) GreaterThan(o Version) bool { return v.Compare(o) > 0 }
func (v Version) Equal(o Version) bool       { return v.Compare(o) == 0 }

func (v Version) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

func (v *Version) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = Version{}
		return nil
	}
	parsed, err := ParseVersion(string(b))
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

// Constraint is a semver range such as ">=1.0.0 <2.0.0".
type Constraint struct {
	raw string
	c   *semver.Constraints
}

func ParseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Constraint{}, nil
	}
	c, err := semver.NewConstraint(s)
	if err != nil {
		return Constraint{}, ValidationError(err, "invalid version constraint %q", s).
			WithHint(`constraints look like ">=2.30" or ">=1.0.0 <2.0.0"`)
	}
	return Constraint{raw: s, c: c}, nil
}

func (c Constraint) IsZero() bool   { return c.c == nil }
func (c Constraint) String() string { return c.raw }

// Allows reports whether v satisfies the constraint. An empty constraint
// allows everything -- absence of a bound is not a bound of zero.
func (c Constraint) Allows(v Version) bool {
	if c.c == nil {
		return true
	}
	if v.sv == nil {
		return false
	}
	return c.c.Check(v.sv)
}

func (c Constraint) MarshalText() ([]byte, error) { return []byte(c.raw), nil }

func (c *Constraint) UnmarshalText(b []byte) error {
	parsed, err := ParseConstraint(string(b))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// Compatibility is the release's declaration of what it can be installed
// over and what it can be rolled back from. All checks live here rather than
// in the update operation, so they are unit-testable without any I/O.
type Compatibility struct {
	DatabaseSchemaMin int        `yaml:"database_schema_min" json:"database_schema_min"`
	DatabaseSchemaMax int        `yaml:"database_schema_max" json:"database_schema_max"`
	RollbackSafe      bool       `yaml:"rollback_safe" json:"rollback_safe"`
	MinManagerVersion Version    `yaml:"min_manager_version" json:"min_manager_version"`
	UpgradeFrom       Constraint `yaml:"upgrade_from" json:"upgrade_from"`
}

// CompatibilityReport is the result of checking a candidate release against
// the current installation. It reports every problem at once: an operator
// fixing one blocker should not discover the next one on the retry.
type CompatibilityReport struct {
	OK       bool     `json:"ok"`
	Problems []string `json:"problems,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *CompatibilityReport) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
	r.OK = false
}

func (r *CompatibilityReport) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Err converts a failed report into a typed error, joining every problem so
// the operator sees the whole picture in one message.
func (r CompatibilityReport) Err() error {
	if r.OK {
		return nil
	}
	return IncompatibleError(nil, "release is not compatible with this installation:\n  - %s",
		strings.Join(r.Problems, "\n  - ")).
		WithHint("check compatibility in the release manifest, or update in smaller steps")
}

// CheckUpgrade validates a transition from `from` to `to`. `from` may be the
// zero Version for a fresh install, in which case upgrade_from does not apply.
// currentSchema is the database schema version currently deployed; pass 0 when
// it is unknown (fresh install) to skip the schema-range check.
func CheckUpgrade(from, to Version, target Compatibility, managerVersion Version, currentSchema int) CompatibilityReport {
	report := CompatibilityReport{OK: true}

	if !target.MinManagerVersion.IsZero() && managerVersion.LessThan(target.MinManagerVersion) {
		report.problem("release %s requires manager >= %s, this is %s",
			to, target.MinManagerVersion, managerVersion)
	}

	if !from.IsZero() {
		if !target.UpgradeFrom.IsZero() && !target.UpgradeFrom.Allows(from) {
			report.problem("release %s accepts upgrades from %q, installed version is %s",
				to, target.UpgradeFrom, from)
		}
		if to.LessThan(from) {
			report.warn("target version %s is older than installed %s; this is a downgrade", to, from)
		}
		if to.Equal(from) {
			report.warn("target version %s is already installed", to)
		}
	}

	// Each bound is guarded by its own presence. Nesting the minimum check
	// inside the maximum's guard meant a release declaring only
	// database_schema_min never warned about a schema below it.
	if currentSchema > 0 {
		if target.DatabaseSchemaMax > 0 && currentSchema > target.DatabaseSchemaMax {
			report.problem("database schema %d is newer than release %s supports (max %d)",
				currentSchema, to, target.DatabaseSchemaMax)
		}
		if target.DatabaseSchemaMin > 0 && currentSchema < target.DatabaseSchemaMin {
			report.warn("database schema %d is below release minimum %d; migrations will run",
				currentSchema, target.DatabaseSchemaMin)
		}
	}

	return report
}

// RollbackAssessment answers the three questions `rollback` must report
// separately, because they fail independently and an operator needs to know
// which one blocked them.
type RollbackAssessment struct {
	ContainersReversible bool   `json:"containers_reversible"`
	SchemaCompatible     bool   `json:"schema_compatible"`
	RestoreRequired      bool   `json:"restore_required"`
	Reason               string `json:"reason,omitempty"`
}

// AssessRollback evaluates returning from `current` to `previous`.
// currentSchema is the schema version the running database is at.
func AssessRollback(current, previous Compatibility, currentSchema int) RollbackAssessment {
	a := RollbackAssessment{ContainersReversible: true, SchemaCompatible: true}
	var reasons []string

	if !current.RollbackSafe {
		a.ContainersReversible = false
		a.RestoreRequired = true
		reasons = append(reasons,
			"the installed release declares rollback_safe: false, so its migrations are irreversible")
	}

	// Evaluated independently of the check above rather than after an early
	// return. The three answers exist to fail separately; reporting the
	// schema as compatible because an earlier blocker short-circuited would
	// tell an operator it had been looked at when it had not.
	if currentSchema > 0 && previous.DatabaseSchemaMax > 0 && currentSchema > previous.DatabaseSchemaMax {
		a.SchemaCompatible = false
		a.RestoreRequired = true
		reasons = append(reasons, fmt.Sprintf(
			"database schema is at %d but the previous release supports at most %d; "+
				"rolling back containers alone would leave the application reading a schema it does not understand",
			currentSchema, previous.DatabaseSchemaMax))
	}

	a.Reason = strings.Join(reasons, "; ")
	return a
}
