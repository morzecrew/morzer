package release

// Version resolution for the bundle build tooling.
//
// The manager supports a *scheme*; it is not a VCS integration. `--version` is
// the real interface and everything here is sugar on top of it, which is why
// every failure below refuses rather than falling back to a default: a silent
// default would produce a bundle that installs, collides with the next one, and
// confuses everybody involved.

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// GitDescription is `git describe --tags --long --dirty`, parsed.
type GitDescription struct {
	// Tag is the nearest reachable tag, as a version.
	Tag domain.Version

	// Distance is how many commits separate HEAD from that tag.
	Distance int

	// SHA is the abbreviated commit, without the "g" git prefixes it with.
	SHA string

	// Dirty reports uncommitted changes in the work tree.
	Dirty bool

	// CommitTime is HEAD's commit date, which becomes the archive's
	// timestamp when nothing else supplies one.
	CommitTime time.Time
}

// DescribeRepository asks git where HEAD sits relative to the nearest tag.
func DescribeRepository(dir string) (GitDescription, error) {
	raw, err := runGit(dir, "describe", "--tags", "--long", "--dirty")
	if err != nil {
		return GitDescription{}, err
	}

	d, err := ParseDescribe(raw)
	if err != nil {
		return GitDescription{}, err
	}

	// `git describe --dirty` reports only *tracked* modifications, and a
	// bundle is archived from the filesystem rather than from the index --
	// so a new untracked file is packed, summed and signed while the
	// version still names the commit as though it described the tree. The
	// status check is what closes that: anything git would report, tracked
	// or not, makes this build dirty.
	dirty, err := workTreeDirty(dir)
	if err != nil {
		return GitDescription{}, err
	}
	d.Dirty = d.Dirty || dirty

	// A second call rather than a --format on the first: `git describe`
	// has no format that carries the commit date.
	stamp, err := runGit(dir, "log", "-1", "--format=%ct")
	if err != nil {
		return GitDescription{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64)
	if err != nil {
		return GitDescription{}, domain.Internal(err,
			"git reported a commit date of %q", strings.TrimSpace(stamp))
	}
	d.CommitTime = time.Unix(seconds, 0).UTC()
	return d, nil
}

// CommitTime is HEAD's commit date, or the zero time when git cannot answer --
// no repository, no git, no commits, an unreadable object store.
//
// Quiet about all of them, not only the first: it feeds the archive's timestamp
// fallback, where every one of those is an ordinary answer that resolves to the
// epoch rather than a problem worth failing an archive over.
func CommitTime(dir string) time.Time {
	stamp, err := runGit(dir, "log", "-1", "--format=%ct")
	if err != nil {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// workTreeDirty reports whether git sees anything uncommitted in dir.
//
// Scoped to the directory rather than the repository, because that is what
// gets packed: a vendor whose bundle lives in a monorepo should not be refused
// over an edit somewhere else in the tree.
//
// `--untracked-files=all` rather than the default `normal`, which reports a
// directory of new files as one entry -- enough to detect, but the explicit
// spelling is what stops a future git default from narrowing this silently.
// Ignored files stay ignored: `status` does not list them without --ignored,
// and a vendor who gitignores their build output has said what they meant.
func workTreeDirty(dir string) (bool, error) {
	out, err := runGit(dir, "status", "--porcelain", "--untracked-files=all", "--", ".")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// ParseDescribe reads `v1.4.0-7-g3be286c` and `v1.4.0-7-g3be286c-dirty`.
func ParseDescribe(raw string) (GitDescription, error) {
	s := strings.TrimSpace(raw)
	malformed := func() (GitDescription, error) {
		return GitDescription{}, domain.Internal(nil,
			"cannot read %q as git describe output", s).
			WithHint("expected <tag>-<distance>-g<sha>, from " +
				"`git describe --tags --long --dirty`")
	}

	var d GitDescription
	if rest, ok := strings.CutSuffix(s, "-dirty"); ok {
		d.Dirty, s = true, rest
	}

	// Read from the right: a tag may contain "-" (every prerelease does),
	// so only the last two fields are unambiguous.
	sha := strings.LastIndex(s, "-g")
	if sha < 0 {
		return malformed()
	}
	d.SHA = s[sha+2:]
	if d.SHA == "" {
		return malformed()
	}
	s = s[:sha]

	distance := strings.LastIndex(s, "-")
	if distance < 0 {
		return malformed()
	}
	n, err := strconv.Atoi(s[distance+1:])
	if err != nil || n < 0 {
		return malformed()
	}
	d.Distance = n

	tag, err := domain.ParseVersion(s[:distance])
	if err != nil {
		return GitDescription{}, domain.ValidationError(err,
			"the nearest tag %q is not a semantic version", s[:distance]).
			WithHint("tag releases as 1.4.0 or v1.4.0, or pass --version")
	}
	d.Tag = tag
	return d, nil
}

// Version renders a description through the scheme.
//
// On a tag, with nothing uncommitted, the version is the tag verbatim -- that
// is the release build, and the only shape here that produces a non-prerelease.
// Anything else is `<next-patch>-dev.<distance>.g<sha>`:
//
//   - The patch is bumped because a prerelease sorts below its own release, so
//     a development build named after the tag it follows would sort behind the
//     release it comes after. This is setuptools-scm's guess-next-dev, arrived
//     at from the same constraint.
//   - The sha is there because commit distance is not unique across branches:
//     two branches seven commits past v1.4.0 both produce dev.7 with different
//     content, which is exactly the collision version identity exists to catch.
//   - The sha is a prerelease identifier and never build metadata. OCI tag
//     grammar excludes "+", so such a version could never be published to a
//     registry -- and metadata is retained by String() while ignored by
//     Compare, which is the guard bypass Manifest.Validate now closes.
func (d GitDescription) Version(allowDirty bool) (domain.Version, error) {
	if d.Dirty && !allowDirty {
		return domain.Version{}, domain.ValidationError(nil,
			"the work tree has uncommitted changes, so the commit %s does not describe it",
			d.SHA).
			WithHint("commit them, or pass --allow-dirty to stamp a version marked .dirty")
	}

	// Guessing forward from a prerelease is ambiguous -- is the next thing
	// after 1.4.0-rc.1 another rc, or 1.4.0? -- and a wrong guess produces
	// a version that sorts wrongly, which is the one failure this scheme
	// exists to prevent. Refusing sends the vendor to --version, which
	// expresses whatever they actually meant.
	if pre := d.Tag.Prerelease(); pre != "" {
		return domain.Version{}, domain.ValidationError(nil,
			"the nearest tag %s is itself a prerelease, so there is no unambiguous next version",
			d.Tag).
			WithHint("pass --version with the version you mean")
	}
	if meta := d.Tag.Metadata(); meta != "" {
		return domain.Version{}, domain.ValidationError(nil,
			"the nearest tag %s carries build metadata, which a release version may not", d.Tag).
			WithHint("tag without the +%s suffix, or pass --version", meta)
	}

	if d.Distance == 0 && !d.Dirty {
		return d.Tag, nil
	}

	// Distance 0 with a dirty tree takes the development shape rather than
	// the tag verbatim: the tree has content the tag does not, so calling
	// it the tag would be a lie, and `1.4.0-dirty` would sort *below* the
	// tag it is ahead of.
	identifiers := fmt.Sprintf("dev.%d.g%s", d.Distance, d.SHA)
	if d.Dirty {
		identifiers += ".dirty"
	}
	return d.Tag.NextPatch().WithPrerelease(identifiers)
}

// runGit invokes git in dir and returns its stdout.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return "", domain.ValidationError(err, "git is not installed").
			WithHint("--version-from-git shells out to git; pass --version instead")
	}
	// Reported rather than defaulted around. The two failures that reach
	// here are "not a repository" and "no names found" -- the second is
	// what a shallow clone produces, which is the default in the most
	// popular CI system in the world, and defaulting past it would stamp a
	// plausible wrong version onto a real release.
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = err.Error()
	}
	return "", domain.ValidationError(err, "git could not describe %s: %s", dir, detail).
		WithHint("--version-from-git needs a repository with a reachable tag. " +
			"A shallow CI checkout fetches no tags: set fetch-depth: 0, or pass --version")
}
