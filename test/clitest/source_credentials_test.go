package clitest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/test/clitest"
)

// init's credential flags belong to one https:// release (RFC 0038 decision
// 12), and each misuse is refused before anything is fetched or created.
func TestInitsSourceFlagsAreRefusedWhereTheyDoNotApply(t *testing.T) {
	r := clitest.New(t)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for args, says := range map[*[]string]string{
		{"init", "--product", "demo", "--source-auth", "basic"}:                                                                             "--source-auth and --source-secret go with --source-secret-file",
		{"init", "--product", "demo", "--source-secret", "vendor_token"}:                                                                    "--source-auth and --source-secret go with --source-secret-file",
		{"init", "--product", "demo", "--release", "./bundle", "--source-secret-file", token}:                                               "applies to an https:// --release",
		{"init", "--product", "demo", "--source-secret-file", token}:                                                                        "applies to an https:// --release",
		{"init", "--product", "demo", "--release", "https://x.example/b.tar.zst", "--source-secret-file", token, "--source-auth", "digest"}: "neither bearer nor basic",
		{"init", "--product", "demo", "--repair", "--source-secret-file", token}:                                                            "does not apply to --repair",
	} {
		r.Run(*args...).ExitCode(domain.ExitUsage).SaysAll(says)
	}
}

func TestReleaseSourceAddTakesOneScheme(t *testing.T) {
	r := clitest.NewInstalled(t)
	r.Run("release", "source", "add", "https://downloads.vendor.example", "--secret", "t").
		ExitCode(domain.ExitUsage).SaysAll("give one of --bearer and --basic")
	r.Run("release", "source", "add", "https://downloads.vendor.example", "--bearer", "--basic", "--secret", "t").
		ExitCode(domain.ExitUsage).SaysAll("give one of --bearer and --basic")
	r.Run("release", "source", "add", "https://downloads.vendor.example/releases", "--bearer", "--secret", "t").
		ExitCode(domain.ExitUsage).SaysAll("names more than a server")

	r.Run("release", "source", "add", "https://downloads.vendor.example", "--bearer", "--secret", "vendor_token").
		ExitCode(0).SaysAll("downloads.vendor.example gets the bearer credential in the secret vendor_token",
		"the secret vendor_token is not set yet")
	r.Run("release", "source", "ls").ExitCode(0).SaysAll("https://downloads.vendor.example", "vendor_token")
}
