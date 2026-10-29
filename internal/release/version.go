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

// GitDescription is where a bundle directory's last change sits relative to
// the nearest tag, in the shape `git describe --tags --long` prints.
type GitDescription struct {
	// Tag is the nearest tag reachable from HEAD, as a version.
	Tag domain.Version

	// Distance is how many commits the bundle's last change holds that the
	// tag does not. Zero when the tag already holds the bundle as it is.
	Distance int

	// SHA is the abbreviated commit, without the "g" git prefixes it with.
	SHA string

	// Dirty reports uncommitted changes in the bundle directory.
	Dirty bool

	// CommitTime is the date of the bundle's last change, which becomes the
	// archive's timestamp when nothing else supplies one.
	CommitTime time.Time
}

// DescribeRepository describes the last commit that changed dir, against the
// nearest tag reachable from HEAD.
//
// The last change to dir rather than HEAD, because dir is what gets packed: in
// a repository that holds more than the bundle, every commit elsewhere would
// otherwise mint a new version for the same bundle, and a pipeline's "this
// version is already published" check could never fire. The dirty check below
// was already scoped the same way.
func DescribeRepository(dir string) (GitDescription, error) {
	raw, err := runGit(dir, "describe", "--tags", "--long")
	if err != nil {
		return GitDescription{}, err
	}
	head, err := ParseDescribe(raw)
	if err != nil {
		return GitDescription{}, err
	}
	tag := strings.TrimSuffix(strings.TrimSpace(raw),
		fmt.Sprintf("-%d-g%s", head.Distance, head.SHA))

	change, err := lastChange(dir)
	if err != nil {
		return GitDescription{}, err
	}

	// Counted from HEAD's tag, not from the tag nearest the change: a change
	// merged from a branch cut before that tag is still newer than it, and
	// must sort after it. A change the tag already holds counts zero, which
	// renders as the tag itself -- a release tagged on a commit that did not
	// touch the bundle is that release.
	count, err := runGit(dir, "rev-list", "--count", "refs/tags/"+tag+".."+change.full)
	if err != nil {
		return GitDescription{}, err
	}
	distance, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil {
		return GitDescription{}, domain.Internal(err,
			"git counted %q commits past %s", strings.TrimSpace(count), tag)
	}

	// `git describe --dirty` is not used: it reports tracked modifications
	// anywhere in the repository, and none of the untracked files a bundle
	// archived from the filesystem would carry. The status check sees both,
	// in the bundle directory only.
	dirty, err := workTreeDirty(dir)
	if err != nil {
		return GitDescription{}, err
	}

	return GitDescription{
		Tag:        head.Tag,
		Distance:   distance,
		SHA:        change.short,
		Dirty:      dirty,
		CommitTime: change.at,
	}, nil
}

// CommitTime is the date of the last commit that changed dir, or the zero time
// when git cannot answer -- no repository, no git, no commits, an unreadable
// object store.
//
// Quiet about all of them, not only the first: it feeds the archive's timestamp
// fallback, where every one of those is an ordinary answer that resolves to the
// epoch rather than a problem worth failing an archive over.
func CommitTime(dir string) time.Time {
	change, err := lastChange(dir)
	if err != nil {
		return time.Time{}
	}
	return change.at
}

type commit struct {
	full, short string
	at          time.Time
}

// lastChange names the last commit that changed dir. A directory no commit has
// touched yet falls back to HEAD; everything in it is untracked, so the dirty
// check refuses the build anyway.
func lastChange(dir string) (commit, error) {
	const format = "--format=%H %h %ct"
	out, err := runGit(dir, "log", "-1", format, "--", ".")
	if err == nil && strings.TrimSpace(out) == "" {
		out, err = runGit(dir, "log", "-1", format)
	}
	if err != nil {
		return commit{}, err
	}

	fields := strings.Fields(out)
	if len(fields) != 3 {
		return commit{}, domain.Internal(nil, "git reported the last commit as %q", strings.TrimSpace(out))
	}
	seconds, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return commit{}, domain.Internal(err, "git reported a commit date of %q", fields[2])
	}
	return commit{full: fields[0], short: fields[1], at: time.Unix(seconds, 0).UTC()}, nil
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
