package compose

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

var _ ports.FileSharingProber = (*Runtime)(nil)

// sharingProbeTimeout bounds the probe, the helper image's pull included.
const sharingProbeTimeout = 2 * time.Minute

var (
	// notShared is Docker Desktop refusing a bind outside its file sharing
	// (RFC 0055 §13 S1).
	notShared = regexp.MustCompile(`The path (\S+) is not shared from the host`)

	// unreachable is the engine failing to stat a bind's source that the
	// user who runs it cannot reach. It names the Mac's path, or the VM's
	// under /host_mnt (§13, the third run).
	unreachable = regexp.MustCompile(`stat (.+?): permission denied`)
)

// ProbeFileSharing lists each directory in read and writes, then removes, a
// file in each in write, from one container (RFC 0055 §5.3 and decision 16). A
// directory outside the engine's file sharing is refused as the container
// starts, and so is one whose way down the engine's user cannot list; one that
// user cannot list or write is found by the script inside.
//
// Through the volume helper image, pinned as it always is, with no network.
// Unlike a backup, the probe lets the engine pull it: it runs on the first
// apply, before anything else would have, and a pull that fails is a probe
// that did not run rather than a refusal.
func (r *Runtime) ProbeFileSharing(ctx context.Context, read, write []string) error {
	ref := r.HelperImage()
	if !digestPinned.MatchString(ref) {
		return unpinnedHelper(ref)
	}

	// Named by this manager's pid, so a doctor beside an apply cannot remove
	// the file the other is about to.
	probe := fmt.Sprintf(".file-sharing-probe-%d", os.Getpid())
	argv := []string{r.docker, "run", "--rm", "--network", "none"}
	var script strings.Builder
	for i, dir := range read {
		target := fmt.Sprintf("/probe/read/%d", i)
		argv = append(argv, "--mount", "type=bind,source="+dir+",target="+target+",readonly")
		fmt.Fprintf(&script, "ls -a %s >/dev/null 2>&1 || echo unlistable %d\n", target, i)
	}
	for i, dir := range write {
		target := fmt.Sprintf("/probe/write/%d", i)
		argv = append(argv, "--mount", "type=bind,source="+dir+",target="+target)
		// touch, not `: >`: a redirection that fails on a special
		// built-in ends a POSIX shell, and the probe with it.
		fmt.Fprintf(&script, "{ touch %[1]s/%[3]s && rm %[1]s/%[3]s; } 2>/dev/null || echo unwritable %[2]d\n",
			target, i, probe)
	}
	// Last, so an image that ran something other than the script cannot
	// pass for one that found nothing.
	script.WriteString("echo probed\n")
	argv = append(argv, ref, "sh", "-c", script.String())

	res, err := r.runner.Run(ctx, exec.Command{
		Argv:          argv,
		Timeout:       sharingProbeTimeout,
		Redact:        r.redact,
		CaptureOutput: true,
	})
	if err != nil {
		return sharingRefusal(err, ref)
	}

	failed := map[string]struct {
		dirs []string
		verb string
	}{"unlistable": {read, "list"}, "unwritable": {write, "write"}}
	var found []string
	probed := false
	for _, line := range strings.Split(res.Stdout, "\n") {
		word, index, _ := strings.Cut(strings.TrimSpace(line), " ")
		f := failed[word]
		if i, err := strconv.Atoi(index); err == nil && i >= 0 && i < len(f.dirs) {
			found = append(found, f.verb+" "+f.dirs[i])
		}
		probed = probed || word == "probed"
	}
	if !probed {
		return domain.RuntimeError(nil, "the file-sharing probe through %s printed no result",
			domain.ShortImageRef(ref)).
			WithHint("the probe runs `sh` in the helper image; set %s to an image with a POSIX shell, "+
				"or leave it unset", helperImageEnv)
	}
	if len(found) > 0 {
		return domain.Preflight(nil, "a container cannot %s", strings.Join(found, ", or ")).
			WithHint("the engine reaches this machine's files as the user who runs it, who has to be able " +
				"to list each managed directory and own the ones a container writes; `ls -ld` shows who can")
	}
	return nil
}

// sharingRefusal reads why the probe's container did not run: a bind the
// engine refused names a directory, and anything else is a probe that did not
// run.
func sharingRefusal(err error, ref string) error {
	message := "cannot run the file-sharing probe through " + domain.ShortImageRef(ref)
	var exitErr *exec.ExitError
	if asExit(err, &exitErr) {
		message += ": " + engineError(exitErr.Stderr)
		if m := notShared.FindStringSubmatch(exitErr.Stderr); m != nil {
			return domain.Preflight(nil, "the engine does not share %s with containers", m[1]).
				WithHint("add %s to the engine's file sharing: in Docker Desktop, Settings, Resources, "+
					"File sharing", m[1])
		}
		if m := unreachable.FindStringSubmatch(exitErr.Stderr); m != nil {
			dir := strings.TrimPrefix(m[1], "/host_mnt")
			return domain.Preflight(nil, "a container cannot reach %s: the user who runs the engine "+
				"cannot list it, or a directory above it", dir).
				WithHint("that user has to be able to list every directory from / down to %s; "+
					"`ls -ld` each to find the one it cannot", dir)
		}
	}
	return wrapExit(err, message,
		"check that the engine answers, and that "+ref+" is on this machine or can be pulled")
}

// engineError is the line of the engine's output that says why the container
// did not start: docker prints the pull it tried before it, and a usage hint
// after it.
func engineError(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "Error") {
			return strings.TrimSpace(line)
		}
	}
	return firstLine(stderr)
}
