package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// fakeSecrets answers Load and nothing else. The notify wiring only ever reads.
type fakeSecrets struct {
	values map[string]domain.Secret
	err    error
}

func (f fakeSecrets) Load(context.Context) (domain.SecretSet, error) {
	if f.err != nil {
		return domain.SecretSet{}, f.err
	}
	return domain.NewSecretSet(f.values), nil
}
func secrets(values map[string]string) fakeSecrets {
	out := map[string]domain.Secret{}
	for k, v := range values {
		out[k] = domain.NewSecret(v)
	}
	return fakeSecrets{values: out}
}

// TestBuildNotifiersDropsRatherThanFails.
//
// Notification must never decide whether an operation runs. A target whose
// secret was deleted is dropped with a warning; the ones that resolve still
// receive events. Refusing to run `apply` because a webhook secret went missing
// would make a deployment depend on the health of its alerting.
func TestBuildNotifiersDropsRatherThanFails(t *testing.T) {
	app := &App{}
	inst := domain.Installation{Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
		{Name: "gone", URLSecret: "missing-url"},
		{URL: "https://good.example/hook"},
	}}}

	n := app.buildNotifiers(context.Background(), inst, secrets(nil))
	if n == nil {
		t.Fatal("one bad target removed the good one")
	}
	fan, ok := n.(ports.Notifiers)
	if !ok || len(fan) != 1 {
		t.Fatalf("want exactly the resolvable target, got %#v", n)
	}
}

// TestBuildNotifiersIsNilWhenNothingResolves keeps Deps.Notifier nil, which is
// the no-op path every installation that configured nothing exercises.
func TestBuildNotifiersIsNilWhenNothingResolves(t *testing.T) {
	app := &App{}

	none := app.buildNotifiers(context.Background(), domain.Installation{}, secrets(nil))
	if none != nil {
		t.Error("an installation with no targets got a notifier")
	}

	allBad := domain.Installation{Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
		{URL: "http://plaintext.example"},
	}}}
	if got := app.buildNotifiers(context.Background(), allBad, secrets(nil)); got != nil {
		t.Error("a target that cannot be built still produced a notifier")
	}
}

// TestBuildNotifierRefusesAnIncompleteCredential.
//
// A document missing either field builds a notifier that sends no
// authentication at all, so the endpoint rejects every delivery while the
// installation looks correct -- a channel silently off, which is the failure
// notifications exist to remove.
func TestBuildNotifierRefusesAnIncompleteCredential(t *testing.T) {
	app := &App{}
	cases := map[string]string{
		"no value":  "header: Authorization\n",
		"no header": "value: Bearer hunter2\n",
		"empty":     "header: \"\"\nvalue: \"\"\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := domain.NotifyTargetConfig{URL: "https://a.example", Credentials: "creds"}
			_, err := app.buildNotifier(context.Background(), cfg, secrets(map[string]string{"creds": doc}))
			if err == nil || !strings.Contains(err.Error(), "no header and value") {
				t.Errorf("want a refusal naming the missing fields, got %v", err)
			}
		})
	}
}

// TestBuildNotifierResolvesTheURLFromASecret is the form that exists because a
// webhook URL can itself be the credential.
func TestBuildNotifierResolvesTheURLFromASecret(t *testing.T) {
	app := &App{}
	cfg := domain.NotifyTargetConfig{Name: "chat", URLSecret: "chat-url"}
	n, err := app.buildNotifier(context.Background(), cfg,
		secrets(map[string]string{"chat-url": "https://hooks.example/T0/B1/XXsecretXX"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(n.Name(), "secret") || strings.Contains(n.Name(), "hooks.example") {
		t.Errorf("the notifier names its endpoint: %q", n.Name())
	}
}

// TestReadSecretValueNamesWhatIsMissing.
func TestReadSecretValueNamesWhatIsMissing(t *testing.T) {
	if _, err := readSecretValue(context.Background(), secrets(nil), "absent"); err == nil ||
		!strings.Contains(err.Error(), "absent") {
		t.Errorf("want a refusal naming the secret, got %v", err)
	}
	if _, err := readSecretValue(context.Background(), nil, "absent"); err == nil {
		t.Error("a nil secret store must be reported, not dereferenced")
	}
}
