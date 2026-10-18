package clitest

import (
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// The notify target commands as an operator types them. Delivery itself is
// TestANotifyTargetAddedByCommandIsTold, in the suite.

func TestNotifyTargetAddListAndRemove(t *testing.T) {
	r := NewInstalled(t)

	r.Run("notify", "target", "list").ExitCode(0).StdoutContains("no notify targets")

	r.Run("notify", "target", "add", "--dry-run", "https://hooks.example/morzer").
		ExitCode(0).OutputContains("would add")
	r.Run("notify", "target", "list", "--json").ExitCode(0).FieldLen("data", 0)

	r.Run("notify", "target", "add", "https://hooks.example/morzer").ExitCode(0).OutputContains("added")
	r.Run("notify", "target", "list").ExitCode(0).StdoutContains("https://hooks.example/morzer", "error")
	r.Run("status").OutputContains("notify", "https://hooks.example/morzer")

	r.Run("notify", "target", "add", "https://hooks.example/morzer").
		ExitCode(domain.ExitUsage).OutputContains("already a notify target")

	r.Run("notify", "target", "remove", "https://hooks.example/morzer").ExitCode(0).OutputContains("removed")
	r.Run("notify", "target", "list", "--json").ExitCode(0).FieldLen("data", 0)
	r.Run("notify", "target", "remove", "https://hooks.example/morzer").
		ExitCode(domain.ExitUsage).OutputContains("not a notify target")
}

// TestNotifyTargetAddRefusesWhatTheWiringWouldDrop: each of these used to be
// accepted by a hand edit and then dropped with a log line, so the operator was
// told nothing about the channel that was meant to tell them things.
func TestNotifyTargetAddRefusesWhatTheWiringWouldDrop(t *testing.T) {
	r := NewInstalled(t)
	r.SetSecret("half_creds", "header: Authorization\n")
	r.SetSecret("plain_url", "http://hooks.example/T0/B1/XXsecretXX")
	r.SetSecret("bad_header", "header: Authorization Bearer\nvalue: x\n")
	r.SetSecret("bad_value", "header: Authorization\nvalue: \"Bearer a\\nX-Injected: b\"\n")

	for name, tc := range map[string]struct {
		args []string
		says string
	}{
		"plaintext":          {[]string{"http://hooks.example/morzer"}, "must be https"},
		"no host":            {[]string{"https:///morzer"}, "is not a URL"},
		"a port and no host": {[]string{"https://:443/morzer"}, "is not a URL"},
		// The parser finds no host and no userinfo, so the password sat
		// in the path, and the refusal printed it.
		"a password and no host":    {[]string{"https:////ops:hunter2@hooks.example/morzer"}, "is not a URL"},
		"a password and no //":      {[]string{"https:/ops:hunter2@hooks.example/morzer"}, "is not a URL"},
		"a password in the url":     {[]string{"https://ops:hunter2@hooks.example/morzer"}, "carries a credential"},
		"no endpoint at all":        {nil, "neither url nor url_secret"},
		"a url secret with no name": {[]string{"--url-secret", "plain_url"}, "must set a name"},
		"a url secret not set":      {[]string{"--name", "chat", "--url-secret", "absent"}, "not set"},
		"a plaintext url secret":    {[]string{"--name", "chat", "--url-secret", "plain_url"}, "must be https"},
		"half a credential":         {[]string{"https://hooks.example/a", "--credentials", "half_creds"}, "no header and value"},
		"an unknown level":          {[]string{"https://hooks.example/a", "--min-level", "info"}, "min_level"},
		// Validate runs before the credential refusal and names the target.
		"a password and an unknown level": {
			[]string{"https://ops:hunter2@hooks.example/morzer", "--min-level", "info"}, "min_level",
		},
		// net/http refuses these at every delivery, which drops them.
		"a header name http refuses":  {[]string{"https://hooks.example/a", "--credentials", "bad_header"}, "not a valid HTTP header"},
		"a header value http refuses": {[]string{"https://hooks.example/a", "--credentials", "bad_value"}, "not a valid HTTP header"},
	} {
		t.Run(name, func(t *testing.T) {
			res := r.Run(append([]string{"notify", "target", "add"}, tc.args...)...).
				Failed().OutputContains(tc.says)
			res.NoOutputContains("hunter2", "XXsecretXX")
		})
	}
	r.Run("notify", "target", "list", "--json").ExitCode(0).FieldLen("data", 0)
}

// TestANotifyURLInASecretIsNeverShown. Decision 10: a webhook URL can be the
// credential, so the target is named by its name everywhere it is printed.
func TestANotifyURLInASecretIsNeverShown(t *testing.T) {
	r := NewInstalled(t)
	r.SetSecret("chat_webhook", "https://hooks.example/T0/B1/XXsecretXX")

	r.Run("notify", "target", "add", "--name", "chat", "--url-secret", "chat_webhook", "--min-level", "warn").
		ExitCode(0).NoOutputContains("XXsecretXX")
	r.Run("notify", "target", "list").ExitCode(0).
		StdoutContains("chat", "warn", "chat_webhook").NoOutputContains("XXsecretXX")
	r.Run("status").OutputContains("chat").NoOutputContains("XXsecretXX")

	// A second target reading the same secret would deliver every event
	// twice, under whatever name it was given.
	r.Run("notify", "target", "add", "--name", "chat-again", "--url-secret", "chat_webhook").
		ExitCode(domain.ExitUsage).OutputContains("already a notify target")

	// Removed by its name, because it has no URL an operator could type.
	r.Run("notify", "target", "remove", "chat").ExitCode(0)
}

// TestDoctorNamesANotifyTargetThatStoppedResolving. The wiring drops a target
// whose secret is gone; doctor is where that becomes visible.
func TestDoctorNamesANotifyTargetThatStoppedResolving(t *testing.T) {
	r := NewInstalled(t)
	r.SetSecret("chat_webhook", "https://hooks.example/T0/B1/XXsecretXX")
	r.Run("notify", "target", "add", "--name", "chat", "--url-secret", "chat_webhook").ExitCode(0)

	r.Run("doctor").NoOutputContains("dropped: chat")

	r.Run("secret", "remove", "chat_webhook", "--force").ExitCode(0)
	r.Run("doctor").OutputContains("dropped: chat").NoOutputContains("XXsecretXX")
}
