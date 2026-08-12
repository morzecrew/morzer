package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `morzer installation describe` — RFC 0027 P1.
//
// A verb of its own rather than a flag on `installation export`, which decision
// 8 left open and this resolves. `export` produces an encrypted identity bundle
// whose whole purpose is to be unreadable by anyone but a recovery key holder;
// this produces a plaintext file whose whole purpose is to be read, reviewed and
// committed. Two artifacts that differ in exactly the property an operator cares
// about, behind one verb separated by a flag, is how somebody publishes the
// wrong one.

func newInstallationDescribeCommand(app *App) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "describe",
		Short: "Write this installation as a file that documents it",
		Long: "Reads a live installation and writes what an operator chose: the release\n" +
			"and its digest, the parameters, the policy, the backup and notification\n" +
			"targets, and the names of the secrets that must exist.\n\n" +
			"It holds no secret value and cannot -- every credential in an\n" +
			"installation is already a reference to a secret by name, so the document\n" +
			"carries names. That is what makes it safe to commit, which is the point:\n" +
			"the answer to \"what is this machine\" becomes a file somebody can review\n" +
			"and diff rather than four commands somebody has to remember to run.\n\n" +
			"Nothing reads it back. `morzer apply -f` is specified in RFC 0027 and\n" +
			"deliberately not built, so this is documentation rather than an\n" +
			"interface, and changing the file changes nothing.",
		Example: "  morzer installation describe\n" +
			"  morzer installation describe --output morzer.yaml\n" +
			"  morzer installation describe --json | jq -e '.data.release.digest'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ops.Describe(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.emitDocument(cmd.Context(), doc, output)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "",
		"write to this file instead of stdout")
	return cmd
}

// emitDocument writes the description where the caller asked for it.
//
// Three destinations and one rule: whatever `--json` is given, stdout carries
// one JSON object and nothing else. A YAML document on stdout beside an
// envelope is the contract break wave 11 already found once, in
// `completion install --print-path`.
func (a *App) emitDocument(_ context.Context, doc domain.InstallationDocument, output string) error {
	if output == "" {
		if a.json != nil {
			a.jsonData = doc
			return nil
		}
		rendered, err := renderDocument(doc)
		if err != nil {
			return err
		}
		a.passThrough(string(rendered))
		return nil
	}

	rendered, err := renderDocument(doc)
	if err != nil {
		return err
	}
	if err := writeDocument(output, rendered); err != nil {
		return err
	}

	if a.json != nil {
		a.jsonData = doc
		return nil
	}
	// On stderr, so that a run which named a file leaves stdout empty and a
	// caller piping it gets nothing it did not ask for. `--output -` is not
	// a spelling of stdout here -- it writes a file called `-` -- because
	// stdout is what the command already does when `--output` is absent,
	// and a second way to ask for it is a second thing to get wrong.
	fmt.Fprintf(a.Stream.Err, "wrote %s\n", output)
	return nil
}

func renderDocument(doc domain.InstallationDocument) ([]byte, error) {
	rendered, err := yaml.Marshal(doc)
	if err != nil {
		return nil, domain.Internal(err, "cannot render the installation document")
	}
	// A header, because this file is going into somebody's repository and
	// the first question a reader has is what wrote it and whether editing
	// it does anything.
	header := "# Written by `morzer installation describe`. Nothing reads it back:\n" +
		"# editing this file changes nothing. See RFC 0027.\n"
	return append([]byte(header), rendered...), nil
}

// writeDocument writes the file, refusing a symlink and replacing atomically.
//
// The path comes from an operator's command line and is usually inside a
// repository, which is what each of the two rules is about.
//
// The symlink check is a *refusal*, not the protection. `atomicfs.WriteFile`
// renames a finished temporary file into place, so it replaces the link rather
// than writing through it whatever this check does -- and quietly replacing a
// symlink an operator deliberately created is its own surprise. The check is
// what turns that into a sentence naming the file they meant. It cannot be
// raced into following a link, because there is no path-following write to
// race: the rename is the write.
//
// Atomic because this file is regenerated over a previous copy that somebody
// has already reviewed and committed. A truncating write that fails halfway
// leaves a partial document where a whole one used to be, and git would show
// the loss as an ordinary edit.
//
// 0600 is enforced on the replacement rather than only on creation:
// `os.WriteFile` applies its mode when it *creates* a file, so rewriting an
// existing world-readable document left it world-readable. atomicfs chmods the
// temporary file before the rename, which lands the mode every time.
func writeDocument(path string, body []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(path)
		return domain.Usage("%s is a symlink to %s", path, target).
			WithHint("refusing to replace it -- name the file you mean")
	}
	// It names an installation, its domains and its targets. None of that
	// is a secret, and none of it is anybody else's business either.
	return atomicfs.WriteFile(path, body, 0o600)
}
