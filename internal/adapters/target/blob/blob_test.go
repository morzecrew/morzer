package blob

import (
	"path/filepath"
	"strings"
	"testing"
)

// safeDestination is where a fetch decides what to write, driven by a manifest
// on a target -- which is a file this manager may not have written, because the
// whole premise of a target is that it is somewhere else.
//
// Tested here rather than through the port because the port cannot plant a
// hostile manifest on a target: a push refuses one, which is the other half of
// the defence and is covered by the contract suite. This half needs the
// function.
func TestSafeDestinationRefusesAnythingLeavingTheDestination(t *testing.T) {
	dest := filepath.Join("/var", "lib", "demo", "backups", "20260101T000000Z")

	for name, component := range map[string]string{
		"a parent reference":   "../../.ssh/authorized_keys",
		"one buried in a path": "nested/../../../etc/shadow",
		"a bare parent":        "..",
		"an absolute path":     "/etc/shadow",
		"an empty component":   "",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := safeDestination(dest, component)
			if err == nil {
				t.Fatalf("safeDestination(%q) = %q, and should have been refused: "+
					"whoever controls the target chooses where this machine writes",
					component, got)
			}
		})
	}
}

func TestSafeDestinationAcceptsWhatABackupActuallyContains(t *testing.T) {
	dest := filepath.Join("/var", "lib", "demo", "backups", "20260101T000000Z")

	for _, component := range []string{
		"database.sql.age",
		"secrets.sops.yaml.age",
		"files/uploads.tar.age", // a hook artifact in a subdirectory
		"./database.sql.age",    // harmless, and manifests in the wild have it
		// A backslash is a legal filename character on the only platform
		// this ships for, so this is one oddly-named file inside the
		// destination rather than an escape. Refusing it would refuse a
		// name a hook is entitled to produce.
		`weird\name.age`,
	} {
		got, err := safeDestination(dest, component)
		if err != nil {
			t.Fatalf("safeDestination(%q) was refused: %v", component, err)
		}
		if !strings.HasPrefix(got, dest+string(filepath.Separator)) {
			t.Errorf("safeDestination(%q) = %q, which is not under %q", component, got, dest)
		}
	}
}

// TestOnlyParentComponentsAreRefused. The rule every transport applies to a key
// out of a manifest, tested once, here, beside the contract it belongs to.
//
// It used to live twice -- once in the SFTP adapter and once in S3 -- and the
// two had already disagreed: a substring test for ".." rejected `notes..age` on
// one and accepted it on the other, so whether a backup could be restored
// depended on which transport had carried it.
func TestOnlyParentComponentsAreRefused(t *testing.T) {
	for key, want := range map[string]bool{
		"../.ssh/authorized_keys":     true,
		"../secrets":                  true,
		"id/../../etc/shadow":         true,
		"..":                          true,
		"notes..age":                  false,
		"database..dump":              false,
		"id/database.sql.age":         false,
		"id/nested..name/file.tar.gz": false,
	} {
		if got := HasParentComponent(key); got != want {
			t.Errorf("HasParentComponent(%q) = %v, want %v", key, got, want)
		}
	}
}
