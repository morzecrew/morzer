package clitest_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gominisign "github.com/jedisct1/go-minisign"

	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/test/clitest"
)

// A bundle may ship its vendor's public key as minisign.pub, and a first-use
// `init` trusts it once the bundle's signature verifies against it (RFC 0052).
// These are the vendor's side of that: the commands that bless a bundle after
// it is signed refuse one whose shipped key would be refused at `init`, so the
// vendor finds out first.

// shipKey writes publicKey into the bundle as minisign.pub, in the form
// `minisign -G` writes, before the bundle is summed and signed.
func shipKey(t *testing.T, dir, publicKey string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ports.PublicKeyFileName),
		[]byte("untrusted comment: minisign public key\n"+publicKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// minisignID is a key's id the way `minisign` prints it.
func minisignID(sk gominisign.PrivateKey) string {
	return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(sk.KeyId[:]))
}

func TestReleaseVerifyChecksTheKeyABundleShips(t *testing.T) {
	t.Run("signed by the key it ships", func(t *testing.T) {
		r := clitest.New(t)
		sk, key := minisignKeypair(t)
		shipKey(t, r.Bundle, key)
		signBundle(t, sk, r.Bundle)
		id := minisignID(sk)

		// Alone, the check says how little it shows.
		r.Run("release", "verify", r.Bundle).ExitCode(0).
			OutputContains(id, "proves only that the two agree", "--signing-key")
		r.Run("--json", "release", "verify", r.Bundle).ExitCode(0).
			FieldEquals("data.shipped_key_id", id)
		// With the vendor's own key, the claim is theirs.
		r.Run("release", "verify", r.Bundle, "--signing-key", key).ExitCode(0).
			OutputContains(id).
			NoOutputContains("proves only")
	})

	t.Run("a stale key is refused, with or without --signing-key", func(t *testing.T) {
		r := clitest.New(t)
		sk, key := minisignKeypair(t)
		old, oldKey := minisignKeypair(t)
		shipKey(t, r.Bundle, oldKey)
		signBundle(t, sk, r.Bundle)

		r.Run("release", "verify", r.Bundle).Failed().
			OutputContains("does not verify against the key it ships", minisignID(old))
		r.Run("release", "verify", r.Bundle, "--signing-key", key).Failed().
			OutputContains("does not verify against the key it ships", minisignID(old))
		// Mid-rotation, with both keys given: the bundle is signed by one
		// of them, and still refused, because the key it ships is not the
		// one that signed it and a first-use install would refuse it.
		r.Run("release", "verify", r.Bundle, "--signing-key", key, "--signing-key", oldKey).Failed().
			OutputContains("does not verify against the key it ships", minisignID(old))
	})

	t.Run("a shipped key with no signature is refused", func(t *testing.T) {
		r := clitest.New(t)
		_, key := minisignKeypair(t)
		shipKey(t, r.Bundle, key)

		// `build` runs before signing, when that is what a bundle has.
		r.Run("release", "build", r.Bundle).ExitCode(0)
		r.Run("release", "verify", r.Bundle).Failed().
			OutputContains(ports.PublicKeyFileName, "not signed")
	})

	t.Run("a bundle that ships no key is checked as before", func(t *testing.T) {
		r := clitest.New(t)
		sk, key := minisignKeypair(t)
		signBundle(t, sk, r.Bundle)

		r.Run("release", "verify", r.Bundle).ExitCode(0).
			NoOutputContains("shipped key", "proves only")
		r.Run("release", "verify", r.Bundle, "--signing-key", key).ExitCode(0)
		out := r.Run("--json", "release", "verify", r.Bundle).ExitCode(0)
		if data, _ := out.JSON()["data"].(map[string]any); data == nil {
			t.Fatal("no data in the JSON report")
		} else if _, ok := data["shipped_key_id"]; ok {
			t.Error("shipped_key_id is reported for a bundle that ships no key")
		}
	})
}

// `archive` checks the shipped key on both of its paths: an unsigned bundle,
// which it otherwise packs with a warning, and a signed one.
func TestReleaseArchiveRefusesAShippedKeyAnInstallWouldRefuse(t *testing.T) {
	out := filepath.Join(t.TempDir(), "demo.tar.zst")

	t.Run("unsigned", func(t *testing.T) {
		r := clitest.New(t)
		_, key := minisignKeypair(t)
		shipKey(t, r.Bundle, key)
		r.Run("release", "build", r.Bundle).ExitCode(0)

		r.Run("release", "archive", r.Bundle, "-o", out).Failed().
			OutputContains(ports.PublicKeyFileName, "not signed").
			NoOutputContains("operators requiring a signature")
	})

	t.Run("signed by another key", func(t *testing.T) {
		r := clitest.New(t)
		sk, _ := minisignKeypair(t)
		old, oldKey := minisignKeypair(t)
		shipKey(t, r.Bundle, oldKey)
		signBundle(t, sk, r.Bundle)

		r.Run("release", "archive", r.Bundle, "-o", out).Failed().
			OutputContains("does not verify against the key it ships", minisignID(old))
	})
}

// `push` is the step after which a stale key is in a registry, so it checks
// the archive it is given rather than trusting `archive` to have.
func TestReleasePushRefusesAStaleShippedKeyBeforeSendingAnything(t *testing.T) {
	r, reg, _, repo := pushFixture(t)
	sk, key := minisignKeypair(t)
	old, oldKey := minisignKeypair(t)
	shipKey(t, r.Bundle, oldKey)
	signBundle(t, sk, r.Bundle)

	// Written directly: `release archive` would refuse this tree.
	archive := filepath.Join(t.TempDir(), "demo-1.2.0.tar.zst")
	if err := release.WriteArchive(r.Bundle, archive, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	r.Run("release", "push", archive, repo, "--signing-key", key).ExitCode(2).
		OutputContains("does not verify against the key it ships", minisignID(old))
	if n := reg.Requests(); n != 0 {
		t.Errorf("the registry received %d requests for a push that was refused", n)
	}
}
