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
// others may; its owner, who can change its mode whatever it is now; and, when
// its group may, that group's members, the people's accounts whose primary
// group it is, and each group nested in it, whose members are its members too.
// macOS's own accounts, below uid 500, are left out of the primary groups' list:
// a stock Mac's `daemon` account has the `daemon` group as its own. Empty means
// root alone.
func Writers(ctx context.Context, runner exec.Runner, dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o002 != 0 {
		return []string{"everyone"}, nil
	}
	uid, gid, err := ownerOf(info)
	if err != nil {
		return nil, err
	}
	groupWrites := info.Mode().Perm()&0o020 != 0
	if uid == 0 && !groupWrites {
		return nil, nil
	}
	uids, err := dscl(ctx, runner, "-list", "/Users", "UniqueID")
	if err != nil {
		return nil, err
	}
	var writers []string
	if uid != 0 {
		owner, ok := nameFor(uids, uid)
		if !ok {
			owner = "uid " + strconv.Itoa(uid)
		}
		writers = append(writers, owner)
	}
	if !groupWrites {
		return others(writers), nil
	}
	groups, err := dscl(ctx, runner, "-list", "/Groups", "PrimaryGroupID")
	if err != nil {
		return nil, err
	}
	name, ok := nameFor(groups, gid)
	if !ok {
		return nil, fmt.Errorf("no group has id %d", gid)
	}
	record, err := dscl(ctx, runner, "-read", "/Groups/"+name, "GroupMembership", "NestedGroups")
	if err != nil && !strings.Contains(record, "No such key") {
		return nil, err
	}
	users, err := dscl(ctx, runner, "-list", "/Users", "PrimaryGroupID")
	if err != nil {
		return nil, err
	}
	writers = append(writers, values(record, "GroupMembership")...)
	writers = append(writers, people(namesWith(users, gid), uids)...)
	if nested := values(record, "NestedGroups"); len(nested) > 0 {
		guids, err := dscl(ctx, runner, "-list", "/Groups", "GeneratedUID")
		if err != nil {
			return nil, err
		}
		for _, guid := range nested {
			writers = append(writers, "group "+groupNamed(guids, guid))
		}
	}
	return others(writers), nil
}

func ownerOf(info fs.FileInfo) (uid, gid int, err error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("%s reports no owner", info.Name())
	}
	return int(st.Uid), int(st.Gid), nil
}

func dscl(ctx context.Context, runner exec.Runner, args ...string) (string, error) {
	res, err := runner.Run(ctx, exec.Command{
		Argv:          append([]string{Binary, "."}, args...),
		Timeout:       10 * time.Second,
		CaptureOutput: true,
	})
	return res.Stdout + res.Stderr, err
}

// nameFor finds the name whose id is id in `dscl . -list <path> <key>`, which
// prints a name and an id per line.
func nameFor(listing string, id int) (string, bool) {
	names := namesWith(listing, id)
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

// values reads key's values from `dscl . -read`, which prints `Key: a b` per
// key, or the key alone with its values indented on the lines after; a key the
// record lacks is printed as `No such key: Key`.
func values(read, key string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(read, "\n") {
		if strings.HasPrefix(line, " ") {
			if in {
				out = append(out, strings.Fields(line)...)
			}
			continue
		}
		k, rest, _ := strings.Cut(line, ":")
		if in = k == key; in {
			out = append(out, strings.Fields(rest)...)
		}
	}
	return out
}

// groupNamed is the name `dscl . -list /Groups GeneratedUID` gives guid, or
// guid itself where none does.
func groupNamed(listing, guid string) string {
	for _, line := range strings.Split(listing, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[1] == guid {
			return fields[0]
		}
	}
	return guid
}

// others is names less root, sorted and once each.
func others(names []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == "root" })
	slices.Sort(out)
	return slices.Compact(out)
}
