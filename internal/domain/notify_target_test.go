package domain_test

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestNotifyTargetEndpointRefusesTheAmbiguousForms.
//
// A target has exactly one endpoint. Both forms set is a contradiction the
// manager must not pick a winner for, and neither set is a target that cannot
// be delivered to. Both were previously accepted into installation state and
// dropped later with a log line, leaving notification silently off.
func TestNotifyTargetEndpointRefusesTheAmbiguousForms(t *testing.T) {
	cases := []struct {
		name   string
		target domain.NotifyTargetConfig
		want   string
	}{
		{"neither form", domain.NotifyTargetConfig{}, "neither url nor url_secret"},
		{
			"both forms",
			domain.NotifyTargetConfig{URL: "https://a.example", URLSecret: "s"},
			"both url and url_secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tc.target.Endpoint(); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("Endpoint() = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

// TestNotifyTargetEndpointTrimsWhatItReturns.
//
// The presence check trimmed and the return did not, so a padded name passed
// validation and was then looked up with its spaces -- a target dropped for a
// secret that exists.
func TestNotifyTargetEndpointTrimsWhatItReturns(t *testing.T) {
	secret, _, err := domain.NotifyTargetConfig{URLSecret: "  chat  ", Name: "chat"}.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	if secret != "chat" {
		t.Errorf("secret name = %q, want %q", secret, "chat")
	}

	_, direct, err := domain.NotifyTargetConfig{URL: " https://a.example "}.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	if direct != "https://a.example" {
		t.Errorf("url = %q", direct)
	}
}

// TestNotifyTargetValidate covers what installation state will now refuse,
// rather than accept and drop at wiring time.
func TestNotifyTargetValidate(t *testing.T) {
	cases := []struct {
		name    string
		target  domain.NotifyTargetConfig
		wantErr string
	}{
		{"a plain url is fine", domain.NotifyTargetConfig{URL: "https://a.example"}, ""},
		{
			"warn is a severity",
			domain.NotifyTargetConfig{URL: "https://a.example", MinLevel: "warn"},
			"",
		},
		{
			"so is an empty min_level",
			domain.NotifyTargetConfig{URL: "https://a.example", MinLevel: ""},
			"",
		},
		{
			"but debug is not",
			domain.NotifyTargetConfig{URL: "https://a.example", MinLevel: "debug"},
			"min_level",
		},
		{
			"url_secret without a name leaves nothing safe to print",
			domain.NotifyTargetConfig{URLSecret: "chat-url"},
			"must set a name",
		},
		{
			"url_secret with a name is fine",
			domain.NotifyTargetConfig{URLSecret: "chat-url", Name: "chat"},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want a refusal naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestNotifyTargetLabelNeverPrintsASecretURL.
//
// Label reaches diagnostics and log lines. A target using url_secret has a URL
// that *is* the credential, so the label must be the name or a placeholder --
// never the value it stands for.
func TestNotifyTargetLabelNeverPrintsASecretURL(t *testing.T) {
	withName := domain.NotifyTargetConfig{URLSecret: "chat-url", Name: "chat"}
	if withName.Label() != "chat" {
		t.Errorf("Label() = %q", withName.Label())
	}

	withoutName := domain.NotifyTargetConfig{URLSecret: "chat-url"}
	if strings.Contains(withoutName.Label(), "https") {
		t.Errorf("Label() leaked an endpoint: %q", withoutName.Label())
	}

	plain := domain.NotifyTargetConfig{URL: "https://a.example/hook"}
	if plain.Label() != "https://a.example/hook" {
		t.Errorf("a non-secret URL should still identify itself: %q", plain.Label())
	}
}

// TestInstallationValidateRejectsABadNotifyTarget is the half that matters:
// the per-target rules above only help if the installation actually applies
// them.
func TestInstallationValidateRejectsABadNotifyTarget(t *testing.T) {
	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_1",
		Product:       "demo",
		Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
			{URL: "https://a.example"},
			{URL: "https://b.example", URLSecret: "s"},
		}},
	}
	err := inst.Validate()
	if err == nil {
		t.Fatal("a contradictory notify target was accepted into installation state")
	}
	if !strings.Contains(err.Error(), "notify.targets[1]") {
		t.Errorf("the refusal should name which target: %v", err)
	}
}
