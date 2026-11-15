// Package dirservice reads who can write a directory through its group on a
// Mac, from Directory Services: RunDir's own entry sits in /private/var/run,
// which its group can write, so that group's accounts could move RunDir aside
// (RFC 0055, logs/macos-host.md).
package dirservice

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/morzecrew/morzer/internal/infra/exec"
)

// Binary is dscl by its path, so PATH does not choose what root runs.
const Binary = "/usr/bin/dscl"

// Writers lists the accounts other than root that can write dir: everyone when
// others may, and otherwise, when its group may, that group's members and the
// people's accounts whose primary group it is. macOS's own accounts, below uid
// 500, are left out of the second: a stock Mac's `daemon` account has the
// `daemon` group as its own. Empty means root alone.
func Writers(ctx context.Context, runner exec.Runner, dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o002 != 0 {
		return []string{"everyone"}, nil
	}
	if info.Mode().Perm()&0o020 == 0 {
		return nil, nil
	}
	gid, err := groupOf(info)
	if err != nil {
		return nil, err
	}
	groups, err := dscl(ctx, runner, "-list", "/Groups", "PrimaryGroupID")
	if err != nil {
		return nil, err
	}
	name, ok := nameFor(groups, gid)
	if !ok {
		return nil, fmt.Errorf("no group has id %d", gid)
	}
	membership, err := dscl(ctx, runner, "-read", "/Groups/"+name, "GroupMembership")
	if err != nil && !strings.Contains(membership, "No such key") {
		return nil, err
	}
	users, err := dscl(ctx, runner, "-list", "/Users", "PrimaryGroupID")
	if err != nil {
		return nil, err
	}
	uids, err := dscl(ctx, runner, "-list", "/Users", "UniqueID")
	if err != nil {
		return nil, err
	}
	return others(append(members(membership), people(namesWith(users, gid), uids)...)), nil
}

func groupOf(info fs.FileInfo) (int, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%s reports no group", info.Name())
	}
	return int(st.Gid), nil
}

func dscl(ctx context.Context, runner exec.Runner, args ...string) (string, error) {
	res, err := runner.Run(ctx, exec.Command{
		Argv:          append([]string{Binary, "."}, args...),
		Timeout:       10 * time.Second,
		CaptureOutput: true,
	})
	return res.Stdout + res.Stderr, err
}

// nameFor finds the name whose id is gid in `dscl . -list <path> PrimaryGroupID`,
// which prints a name and an id per line.
func nameFor(listing string, gid int) (string, bool) {
	names := namesWith(listing, gid)
	if len(names) == 0 {
		return "", false
	}
	return names[0], true
}

// namesWith is every name the listing gives the id gid.
func namesWith(listing string, gid int) []string {
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if id, err := strconv.Atoi(fields[1]); err == nil && id == gid {
			out = append(out, fields[0])
		}
	}
	return out
}

// firstPersonUID is where macOS starts the ids of people's accounts.
const firstPersonUID = 500

// people is the names whose id in a `dscl . -list /Users UniqueID` listing is a
// person's, not one of macOS's own accounts.
func people(names []string, uids string) []string {
	ids := map[string]int{}
	for _, line := range strings.Split(uids, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			if id, err := strconv.Atoi(fields[1]); err == nil {
				ids[fields[0]] = id
			}
		}
	}
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool {
		id, ok := ids[n]
		return ok && id < firstPersonUID
	})
}

// members reads `GroupMembership: a b` from `dscl . -read`; a group with no
// members has no such key.
func members(read string) []string {
	_, rest, ok := strings.Cut(read, "GroupMembership:")
	if !ok {
		return nil
	}
	return strings.Fields(rest)
}

// others is names less root, sorted and once each.
func others(names []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "root" })
	slices.Sort(out)
	return slices.Compact(out)
}
