package suite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/source"
	"github.com/morzecrew/morzer/internal/adapters/source/https"
	"github.com/morzecrew/morzer/internal/adapters/source/local"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

const vendorSecretValue = "s3cr3t-vendor-token"

// protectedServer serves archive only to a request carrying the vendor's
// token, as a download service for paying customers does. Its address is
// 127.0.0.1:<port>, so the entry names that.
func protectedServer(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+vendorSecretValue {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withCredentialedHTTPS wires the harness with the real HTTPS source asking
// the real credential lookup, as root.go wires a command.
func withCredentialedHTTPS(t *testing.T, h *harness, srv *httptest.Server) *ops.SourceCredentials {
	t.Helper()
	creds := ops.NewSourceCredentials(func() *ops.Deps { return h.Deps })
	src := https.New(https.WithTransport(srv.Client().Transport), https.WithCredentials(creds))
	t.Cleanup(func() { _ = src.Close() })
	sources, err := source.NewRegistry(local.New(), src)
	require.NoError(t, err)
	h.Deps.Source = sources
	return creds
}

func packedRelease(t *testing.T, dir string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeTarZst(t, dir, path)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

// RFC 0038 §6: `update` from an authenticated URL succeeds, and the journal,
// `status` and `describe` hold no part of the value.
func TestAnUpdateFromAServerThatWantsACredentialSendsTheConfiguredOne(t *testing.T) {
	h := newHarness(t)
	h.install()
	h.setHookEnv()
	applyBaseline(t, h)
	ctx := context.Background()

	srv := protectedServer(t, packedRelease(t, stageUpgradeSource(t, h)))
	withCredentialedHTTPS(t, h, srv)
	ref := srv.URL + "/bundle-1.3.0.tar.zst"

	// Nothing configured, and a password typed into the reference: warned
	// about (decision 16), refused, and the hint says how to configure it.
	authority := strings.TrimPrefix(srv.URL, "https://")
	_, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: "https://someone:hunter2@" + authority + "/bundle-1.3.0.tar.zst"})
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Hint, "release source add https://"+authority)
	assert.NotContains(t, err.Error(), "hunter2")
	warned := false
	for _, e := range h.Events.Events() {
		warned = warned || (e.Level == events.LevelWarn && strings.Contains(e.Message, "carries a password"))
	}
	assert.True(t, warned, "a password in the reference was not warned about")

	// Configured, with the secret not set yet: refused, naming the secret.
	_, err = ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: srv.URL, Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.NoError(t, err)
	_, err = ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: ref})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vendor_token")
	assert.Contains(t, domain.AsError(err).Hint, "morzer secret set vendor_token")

	require.NoError(t, h.Secrets.Set(ctx, "vendor_token", domain.NewSecret(vendorSecretValue+"\n")))
	result, err := ops.Update(ctx, h.Deps, ops.UpdateOptions{Ref: ref})
	require.NoError(t, err)
	assert.Equal(t, "1.3.0", result.Record.To.String())

	journal, err := h.Deps.State.Operations(ctx, ports.Filter{})
	require.NoError(t, err)
	status, err := ops.GetStatus(ctx, h.Deps)
	require.NoError(t, err)
	described, err := ops.Describe(ctx, h.Deps)
	require.NoError(t, err)
	for name, v := range map[string]any{"journal": journal, "status": status, "describe": described} {
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "s3cr3t", "%s carries the credential", name)
	}
	assert.Equal(t, []domain.HTTPSSource{{Host: authority, Auth: domain.SourceAuthBearer, Secret: "vendor_token"}},
		described.Sources.HTTPS, "describe does not name the entry")
}

// `init` installs from a server that wants a credential, before any secret
// store exists, and leaves the entry and the secret for the next update
// (RFC 0038 decision 12).
func TestInitFromAServerThatWantsACredentialKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	ctx := context.Background()

	staged := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), staged)
	retargetManifest(t, staged, h.Root)
	srv := protectedServer(t, packedRelease(t, staged))
	creds := withCredentialedHTTPS(t, h, srv)
	ref := srv.URL + "/bundle-1.2.0.tar.zst"

	src, err := ops.NewInitSource(ref, domain.SourceAuthBearer, "", vendorSecretValue+"\n", "/tmp/token")
	require.NoError(t, err)
	creds.Hold(src)
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: ref, GenerateSecrets: true, Source: src,
	})
	require.NoError(t, err)

	inst, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	authority := strings.TrimPrefix(srv.URL, "https://")
	secret := domain.DefaultSourceSecret(authority)
	assert.Equal(t, []domain.HTTPSSource{{Host: authority, Auth: domain.SourceAuthBearer, Secret: secret}},
		inst.Sources.HTTPS)
	set, err := h.Deps.Secrets.Load(ctx)
	require.NoError(t, err)
	stored, ok := set.Get(secret)
	require.True(t, ok, "init did not store the credential")
	assert.Equal(t, vendorSecretValue+"\n", stored.Reveal())

	// A later command holds nothing: the stored secret answers.
	withCredentialedHTTPS(t, h, srv)
	resolved, err := h.Deps.Source.Resolve(ctx, mustRef(t, ref))
	require.NoError(t, err, "the stored credential was not sent")
	assert.Equal(t, "1.2.0", resolved.Version.String())

	// And `init --repair` keeps the entry, as it keeps the other settings.
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{Product: "demo", NoRecoveryKey: true, Repair: true})
	require.NoError(t, err)
	repaired, err := h.Deps.State.LoadInstallation(ctx)
	require.NoError(t, err)
	assert.Equal(t, inst.Sources, repaired.Sources, "a repair dropped the source credentials")
}

func mustRef(t *testing.T, raw string) ports.Ref {
	t.Helper()
	ref, err := ports.ParseRef(raw)
	require.NoError(t, err)
	return ref
}

// A credential named after a secret the release declares would overwrite it.
func TestInitRefusesACredentialSecretTheReleaseDeclares(t *testing.T) {
	h := newHarness(t)
	h.setHookEnv()
	realIdentity(t, h)
	ctx := context.Background()

	staged := filepath.Join(t.TempDir(), "bundle-1.2.0")
	copyBundle(t, testBundlePath(t), staged)
	retargetManifest(t, staged, h.Root)
	srv := protectedServer(t, packedRelease(t, staged))
	creds := withCredentialedHTTPS(t, h, srv)
	ref := srv.URL + "/bundle-1.2.0.tar.zst"

	src, err := ops.NewInitSource(ref, domain.SourceAuthBearer, "db_password", vendorSecretValue, "/tmp/token")
	require.NoError(t, err)
	creds.Hold(src)
	_, err = ops.Init(ctx, h.Deps, ops.InitOptions{
		Product: "demo", NoRecoveryKey: true, ReleasePath: ref, GenerateSecrets: true, Source: src,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares a secret named db_password")
}

// The entries are changed under the lock, journaled with the server, the
// scheme and the secret's name, and never the value (decision 11).
func TestTheSourceCommandsJournalWhatTheyChange(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()
	opsOf := func() []domain.OperationRecord {
		recs, err := h.Deps.State.Operations(ctx, ports.Filter{})
		require.NoError(t, err)
		return recs
	}
	// Records written in one second have no order between them, so a
	// record is found by what it says.
	recordWith := func(flag string) domain.OperationRecord {
		for _, rec := range opsOf() {
			if rec.Flags[flag] != "" {
				return rec
			}
		}
		t.Fatalf("no journal record has %s", flag)
		return domain.OperationRecord{}
	}
	before := len(opsOf())

	plan, err := ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		Options: ops.Options{DryRun: true},
		URL:     "https://Downloads.Vendor.Example:443", Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.NoError(t, err)
	assert.Contains(t, plan.Summary, "would configure")
	assert.Len(t, opsOf(), before, "a plan journaled")

	_, err = ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: "https://Downloads.Vendor.Example:443", Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.NoError(t, err)
	require.Len(t, opsOf(), before+1)
	added := recordWith("source_add")
	assert.Equal(t, domain.OpTypeConfig, added.Type)
	assert.Equal(t, map[string]string{
		"source_add": "downloads.vendor.example", "auth": "bearer", "secret": "vendor_token",
	}, added.Flags)

	// The same entry again changes nothing; another scheme replaces it.
	again, err := ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: "https://downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.NoError(t, err)
	assert.Contains(t, again.Summary, "as it already did")
	assert.Len(t, opsOf(), before+1)
	replaced, err := ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: "https://downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "vendor_login",
	})
	require.NoError(t, err)
	assert.Contains(t, replaced.Summary, "in place of the bearer credential in vendor_token")
	report, err := ops.SourceList(ctx, h.Deps)
	require.NoError(t, err)
	assert.Equal(t, []domain.HTTPSSource{
		{Host: "downloads.vendor.example", Auth: domain.SourceAuthBasic, Secret: "vendor_login"},
	}, report.HTTPS)

	removed, err := ops.SourceRemove(ctx, h.Deps, ops.Options{}, "https://downloads.vendor.example/")
	require.NoError(t, err)
	assert.Contains(t, removed.Summary, "the secret vendor_login is kept")
	recs := opsOf()
	assert.Equal(t, "downloads.vendor.example", recordWith("source_remove").Flags["source_remove"])
	report, err = ops.SourceList(ctx, h.Deps)
	require.NoError(t, err)
	assert.Empty(t, report.HTTPS)

	nothing, err := ops.SourceRemove(ctx, h.Deps, ops.Options{}, "https://downloads.vendor.example")
	require.NoError(t, err)
	assert.Contains(t, nothing.Summary, "no credential is configured")
	assert.Len(t, opsOf(), len(recs), "removing nothing journaled")

	_, err = ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: "https://user:pw@downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "pw@")
}

// A configured server whose secret is not set is a fetch that will be
// refused, which `doctor` says before it is tried (decision 15).
func TestDoctorWarnsWhenASourceCredentialIsNotSet(t *testing.T) {
	h := newHarness(t)
	h.install()
	ctx := context.Background()
	_, err := ops.SourceAdd(ctx, h.Deps, ops.SourceAddOptions{
		URL: "https://downloads.vendor.example", Auth: domain.SourceAuthBearer, Secret: "vendor_token",
	})
	require.NoError(t, err)

	check := doctorCheck(t, h, "release.source-credentials")
	assert.Equal(t, "warn", string(check.Status))
	assert.Contains(t, check.Message, "vendor_token (for downloads.vendor.example)")

	require.NoError(t, h.Secrets.Set(ctx, "vendor_token", domain.NewSecret("t")))
	check = doctorCheck(t, h, "release.source-credentials")
	assert.Equal(t, "ok", string(check.Status))
}

// A reference ParseRef accepts is warned about whatever its scheme's case,
// and with the space around it ParseRef trims (decision 16).
func TestAPasswordInAnyReferenceParseRefAcceptsIsWarnedAbout(t *testing.T) {
	for _, ref := range []string{
		"https://someone:hunter2@downloads.vendor.example/b.tar.zst",
		"HTTPS://someone:hunter2@downloads.vendor.example/b.tar.zst",
		"  https://someone:hunter2@downloads.vendor.example/b.tar.zst",
	} {
		t.Run(ref, func(t *testing.T) {
			_, err := ports.ParseRef(ref)
			require.NoError(t, err, "the reference is not one the manager fetches")
			h := newHarness(t)
			ops.WarnPasswordInRef(h.Deps, ref)
			warned := false
			for _, e := range h.Events.Events() {
				warned = warned || (e.Level == events.LevelWarn && strings.Contains(e.Message, "carries a password"))
				assert.NotContains(t, e.Message, "hunter2")
			}
			assert.True(t, warned)
		})
	}
}
