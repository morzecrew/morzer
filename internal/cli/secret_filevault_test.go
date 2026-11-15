package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/test/fakes"
)

// A secret change on a Mac with FileVault off is refused before anything is
// stored or decrypted, so the stored value stays as it was and nothing runs
// on a value the render then refuses (RFC 0055 decision 5).
func TestASecretChangeWithFileVaultOffStoresNothing(t *testing.T) {
	for _, args := range [][]string{
		{"generate", "smtp_password"},
		{"rotate", "db_password"},
		{"set", "db_password"},
		{"edit", "db_password"},
	} {
		t.Run(args[0], func(t *testing.T) {
			app, _ := restartApp(t)
			app.Deps.DiskEncrypted = func(context.Context) (bool, error) { return false, nil }
			store, ok := app.Deps.Secrets.(*fakes.SecretStore)
			require.True(t, ok)
			before, err := store.Load(context.Background())
			require.NoError(t, err)

			if args[0] == "edit" {
				// The command asks for a terminal first; the session
				// behind it is what decrypts.
				err = app.editSecretsWith(context.Background(), args[1:],
					scriptedEditor(`echo changed > "$0"`))
			} else {
				cmd := newSecretCommand(app)
				cmd.SetArgs(args)
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				cmd.SetContext(context.Background())
				err = cmd.Execute()
			}

			require.ErrorContains(t, err, "FileVault is off")
			after, err := store.Load(context.Background())
			require.NoError(t, err)
			assert.Equal(t, before.Names(), after.Names())
			was, _ := before.Get("db_password")
			now, _ := after.Get("db_password")
			assert.Equal(t, was.Reveal(), now.Reveal(), "a refused change stored a value")
		})
	}
}

// The gate is asked again once the value or the edit is in, before it is
// stored: what changed while the prompt or the editor was open is honoured.
func TestASecretChangeIsCheckedAgainOnceItsInputIsIn(t *testing.T) {
	for _, command := range []string{"set", "edit"} {
		t.Run(command, func(t *testing.T) {
			app, _ := restartApp(t)
			asked := 0
			app.Deps.DiskEncrypted = func(context.Context) (bool, error) { asked++; return asked == 1, nil }
			store, ok := app.Deps.Secrets.(*fakes.SecretStore)
			require.True(t, ok)
			before, err := store.Load(context.Background())
			require.NoError(t, err)

			if command == "edit" {
				err = app.editSecretsWith(context.Background(), []string{"db_password"},
					scriptedEditor(`echo changed > "$0"`))
			} else {
				app.Stream.In = strings.NewReader("changed\n")
				cmd := newSecretCommand(app)
				cmd.SetArgs([]string{"set", "db_password"})
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				cmd.SetContext(context.Background())
				err = cmd.Execute()
			}

			require.ErrorContains(t, err, "FileVault is off")
			after, err := store.Load(context.Background())
			require.NoError(t, err)
			was, _ := before.Get("db_password")
			now, _ := after.Get("db_password")
			assert.Equal(t, was.Reveal(), now.Reveal(), "a change was stored after the gate closed")
		})
	}
}
