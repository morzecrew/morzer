package domain

import (
	"fmt"
	"regexp"
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

// Prerelease is the identifier after the "-", or "" for a release version.
func (v Version) Prerelease() string {
	if v.sv == nil {
		return ""
	}
	return v.sv.Prerelease()
}

// Metadata is the identifier after the "+", or "" for a version without one.
//
// Worth having a name because build metadata is the quietest trap in semver:
// String() keeps it, so it reaches directory names and store keys, while
// Compare ignores it, so two versions differing only in metadata are equal to
// every comparison and distinct to every path. A release identity may not carry
// one -- see Manifest.Validate.
func (v Version) Metadata() string {
	if v.sv == nil {
		return ""
	}
	return v.sv.Metadata()
}

// NextPatch is the next patch release after v, with no prerelease and no
// metadata.
//
// "Next patch" rather than "patch plus one", because that is what the semver
// library does and the difference matters: for a *prerelease* input it drops
// the prerelease and leaves the patch alone, so 1.4.0-rc.1 becomes 1.4.0 rather
// than 1.4.1. That is the right answer -- 1.4.0-rc.1 is on its way to 1.4.0 --
// and it is why the VCS scheme refuses a prerelease tag rather than relying on
// this: which release an rc is on its way to is a question about intent, not
// arithmetic.
//
// It exists for that scheme: a prerelease sorts *below* its own release, so a
// development build named after the tag it follows would sort behind the
// release it comes after. Guessing the next patch is what makes it sort
// forward.
func (v Version) NextPatch() Version {
	if v.sv == nil {
		return Version{}
	}
	next := v.sv.IncPatch()
	return Version{sv: &next}
}

// WithPrerelease returns v carrying the given prerelease identifiers.
func (v Version) WithPrerelease(pre string) (Version, error) {
	if v.sv == nil {
		return Version{}, Internal(nil, "cannot add a prerelease to an unset version")
	}
	next, err := v.sv.SetPrerelease(pre)
	if err != nil {
		return Version{}, ValidationError(err, "%q is not a valid prerelease identifier", pre)
	}
	return Version{sv: &next}, nil
}

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

	// inclusive is the same constraint rewritten to compare pre-releases by
	// ordering. Nil when the rewrite does not apply or does not parse; see
	// prereleaseInclusive.
	inclusive *semver.Constraints
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
	return Constraint{raw: s, c: c, inclusive: prereleaseInclusive(s)}, nil
}

func (c Constraint) IsZero() bool   { return c.c == nil }
func (c Constraint) String() string { return c.raw }

// Allows reports whether v satisfies the constraint. An empty constraint
// allows everything -- absence of a bound is not a bound of zero.
//
// Pre-releases are compared by ordering, which is not what the constraint
// library does on its own: it excludes *every* pre-release from a constraint
// that carries none, so `upgrade_from: ">=2.30"` refused a customer running
// 2.31.0-rc.1 with the message "accepts upgrades from >=2.30, installed version
// is 2.31.0-rc.1" -- a sentence that reads as satisfied. That rule is right for
// resolving a dependency, where an rc must not be picked up by accident, and
// wrong here, where the version is a fact about a machine somebody is already
// running rather than a candidate to select.
//
// The retry is against the *same* version, through a constraint rewritten to
// admit pre-releases -- not against the version's release core, which was wrong
// at both boundaries: it made ">=2.0.0" accept 2.0.0-rc.1, which is below the
// floor by ordering, while still refusing an rc that genuinely sits inside the
// range.
func (c Constraint) Allows(v Version) bool {
	if c.c == nil {
		return true
	}
	if v.sv == nil {
		return false
	}
	if c.c.Check(v.sv) {
		return true
	}
	if v.sv.Prerelease() == "" || c.inclusive == nil {
		return false
	}
	return c.inclusive.Check(v.sv)
}

// prereleaseInclusive rewrites a constraint so the library compares
// pre-releases by ordering instead of excluding them.
//
// The library's rule is per element: an element with no pre-release of its own
// refuses every pre-release candidate. Its documented answer is to spell the
// element with one, and "-0" is the lowest there is -- so ">=2.30" becomes
// ">=2.30.0-0", which admits 2.31.0-rc.1 exactly as ordering says it should.
//
// The boundary reading that falls out is the conventional one, and the one this
// field wants: a lower bound admits its own pre-releases ("2.0 or later"
// includes 2.0.0-rc.1), and an upper bound does not (">=1.0.0 <2.0.0" is "any
// 1.x", and 2.0.0-rc.1 is not a 1.x).
//
// Wildcards are left alone: "1.x" cannot carry a pre-release, and a rewrite
// would produce something that does not parse. Anything that fails to parse
// after rewriting returns nil, and Allows keeps the library's own answer --
// refusing, which is the safe direction for a compatibility gate.
//
// The unit that is left alone is the whole alternative, not the wildcard
// element inside it, because the exclusion belongs to the group rather than to
// the element that carries it: ">=1.0.0 <2.x" refuses 1.5.0-rc.1 today, and
// rewriting only the lower bound to ">=1.0.0-0" would have admitted it. An
// alternative *beside* one is still rewritten -- "1.x || >=2.0.0" keeps its
// wildcard branch refusing and lets the second branch admit 2.5.0-rc.1, which
// is the same reading every other "||" gets.
//
// Both questions -- does this token already carry a pre-release, does it carry a
// wildcard -- are asked of the token's *core*, the part before any "+". Build
// metadata is free-form: ">=1.0.0+build-foo" has a hyphen in it and no
// pre-release, and ">=1.0.0+fix" has an "x" in it and no wildcard. Reading
// either as the thing it resembles left a valid constraint refusing every
// pre-release inside its own range.
func prereleaseInclusive(raw string) *semver.Constraints {
	groups := strings.Split(raw, "||")
	changed := false

	for i, group := range groups {
		if hasWildcard(group) {
			continue
		}
		rewritten := versionToken.ReplaceAllStringFunc(group, func(token string) string {
			core, metadata := splitMetadata(token)
			if strings.Contains(core, "-") {
				// Already carries a pre-release, and it means it.
				return token
			}
			return core + "-0" + metadata
		})
		if rewritten != group {
			groups[i], changed = rewritten, true
		}
	}
	if !changed {
		return nil
	}

	parsed, err := semver.NewConstraint(strings.Join(groups, "||"))
	if err != nil {
		return nil
	}
	return parsed
}

// hasWildcard reports whether an alternative contains a wildcard element.
func hasWildcard(group string) bool {
	// A wildcard spelled on its own -- "*", "x" -- is not a version token at
	// all, because a token starts at a digit. Whatever is left after the
	// tokens are removed is operators and separators, which carry no
	// letters, so an x or a star in there is that spelling.
	if strings.ContainsAny(versionToken.ReplaceAllString(group, ""), "xX*") {
		return true
	}
	for _, token := range versionToken.FindAllString(group, -1) {
		// The numbers only. A wildcard can only stand where a number
		// would, and everything after the "-" is a pre-release
		// identifier, which is free-form the same way build metadata is:
		// ">=1.0.0-rcx.1" names a release candidate, not a wildcard.
		//
		// Nothing observable rides on this today -- a group with a
		// pre-release element anywhere in it is already compared by
		// ordering, so Allows answers from the library before the
		// rewrite is consulted. It is here so the predicate means what
		// its name says, and does not become a bug the day that
		// short-circuit changes.
		core, _ := splitMetadata(token)
		numbers, _, _ := strings.Cut(core, "-")
		if strings.ContainsAny(numbers, "xX*") {
			return true
		}
	}
	return false
}

// splitMetadata cuts a version token into the part that carries meaning and the
// build metadata, which is free-form and must not be read as either.
func splitMetadata(token string) (core, metadata string) {
	if plus := strings.IndexByte(token, '+'); plus >= 0 {
		return token[:plus], token[plus:]
	}
	return token, ""
}

// versionToken matches a whole version inside a constraint -- the numbers and
// whatever pre-release or build metadata follows them -- so the rewrite above
// can look at the token as a unit rather than at a digit run.
//
// It starts at a digit, which is what keeps it off the operators (>=, ^, ~) and
// off the spaced hyphen of a range: "1.0.0 - 2.0.0" is two tokens with the
// separator between them, not one token containing a pre-release.
var versionToken = regexp.MustCompile(`\d[0-9A-Za-z.+-]*`)

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
