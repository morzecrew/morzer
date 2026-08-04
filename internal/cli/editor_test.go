package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
)

// `secret edit` is the one command that cannot be scripted, so what matters
// most about it is that it says so rather than hanging, and that it refuses to
// delete a secret the running release needs.

func TestSecretEditRefusesWithoutATerminal(t *testing.T) {
	app, _, _ := editApp(t, map[string]string{"db_password": "a"})
	app.Stream.In = strings.NewReader("")

	err := app.editSecrets(context.Background(), nil)
	require.Error(t, err, "an editor session was started with nothing to edit on")

	de := domain.AsError(err)
	assert.Equal(t, domain.CodeUsage, de.Code)
	assert.Contains(t, de.Message, "needs a terminal")
	assert.Contains(t, de.Hint, "secret set",
		"the refusal does not offer the scriptable alternative, which is the "+
			"whole reason anyone hits this")
}

// TestTheEditorIsChosenTheWayEveryOtherToolChoosesIt: $VISUAL before $EDITOR,
// which is the order git, crontab and sudoedit use.
func TestTheEditorIsChosenTheWayEveryOtherToolChoosesIt(t *testing.T) {
	t.Run("VISUAL wins", func(t *testing.T) {
		t.Setenv("VISUAL", "code --wait")
		t.Setenv("EDITOR", "vi")

		got, err := editorCommand()
		require.NoError(t, err)
		assert.Equal(t, []string{"code", "--wait"}, got,
			"an editor command with arguments was not split, so `code --wait` "+
				"would be looked up as one executable")
	})

	t.Run("EDITOR when VISUAL is unset", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "nano")

		got, err := editorCommand()
		require.NoError(t, err)
		assert.Equal(t, []string{"nano"}, got)
	})

	t.Run("whitespace is not a configured editor", func(t *testing.T) {
		t.Setenv("VISUAL", "   ")
		t.Setenv("EDITOR", "nano")

		got, err := editorCommand()
		require.NoError(t, err)
		assert.Equal(t, []string{"nano"}, got)
	})

	t.Run("neither set falls back to vi", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "")

		got, err := editorCommand()
		if err != nil {
			// A machine with no vi at all. POSIX says it should be
			// there; the refusal is what happens when it is not.
			assert.Contains(t, domain.AsError(err).Hint, "$EDITOR")
			return
		}
		assert.Equal(t, []string{"vi"}, got)
	})
}

// TestRemovingARequiredSecretIsRefused. Deleting a line in the editor deletes a
// secret, and deleting one the release requires makes the next `apply` fail --
// which is a failure the operator would meet later, somewhere else.
func TestRemovingARequiredSecretIsRefused(t *testing.T) {
	app, _, _ := editApp(t, map[string]string{"db_password": "a", "smtp_password": "b"})

	t.Run("nothing removed", func(t *testing.T) {
		require.NoError(t, app.checkRemovals(context.Background(), nil))
	})

	t.Run("with no release installed nothing is required", func(t *testing.T) {
		// A machine with no release declares no schema, so it cannot
		// know what is required, and guessing would refuse edits on a
		// machine that has not been set up yet.
		require.NoError(t, app.checkRemovals(context.Background(),
			[]string{"db_password"}))
	})

	t.Run("--force is the operator saying they mean it", func(t *testing.T) {
		forced := *app
		forced.Flags.force = true
		require.NoError(t, forced.checkRemovals(context.Background(),
			[]string{"db_password"}))
	})
}

func TestEditSummaryReadsAsASentence(t *testing.T) {
	cases := map[string]struct {
		added, changed, removed, restarted []string
		want                               string
	}{
		"one of each": {
			[]string{"a"}, []string{"b"}, []string{"c"}, []string{"app"},
			"added a; changed b; removed c; restarted app",
		},
		"only a change": {
			nil, []string{"b"}, nil, nil, "changed b",
		},
		"a change with no restart, because nothing declares it": {
			nil, []string{"b"}, nil, nil, "changed b",
		},
		"several at once": {
			[]string{"a", "b"}, nil, nil, []string{"app", "db"},
			"added a, b; restarted app, db",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want,
				editSummary(tc.added, tc.changed, tc.removed, tc.restarted))
		})
	}
}

func TestDefaultStreamsCarryTheProcessInput(t *testing.T) {
	s := ui.DefaultStreams()
	assert.NotNil(t, s.In, "a default App would dereference nil the first time "+
		"anyone piped a secret into it")
}
