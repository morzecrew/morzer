package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/ports"
)

// listSource is a ReleaseSource that only answers List.
type listSource struct {
	versions []domain.Version
	err      error
}

func (s listSource) Schemes() []string { return []string{"oci"} }
func (s listSource) Resolve(context.Context, ports.Ref) (ports.ResolvedRelease, error) {
	return ports.ResolvedRelease{}, errors.New("not used")
}
func (s listSource) Fetch(context.Context, ports.Ref, string) (ports.BundlePath, error) {
	return "", errors.New("not used")
}
func (s listSource) List(context.Context, ports.Ref) ([]domain.Version, error) {
	return s.versions, s.err
}

// checkDeps builds a Deps over a real state store in a temp root.
//
// The real store rather than a fake: it is the thing that has to agree about
// where a source ref is written, and a fake would agree with itself.
func checkDeps(t *testing.T, checkOn bool, current domain.ReleaseRecord, src ports.ReleaseSource) *Deps {
	t.Helper()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	store := state.New(paths)
	ctx := context.Background()

	inst := domain.Installation{
		SchemaVersion: domain.InstallationSchemaVersion,
		ID:            "inst_test",
		Product:       "demo",
		CreatedAt:     domain.NewTime(nowForTest()),
		Update:        domain.UpdateConfig{Check: checkOn},
	}
	if err := store.SaveInstallation(ctx, inst); err != nil {
		t.Fatalf("cannot save the installation: %v", err)
	}
	if !current.IsZero() {
		if err := store.SetCurrentRelease(ctx, current); err != nil {
			t.Fatalf("cannot record the current release: %v", err)
		}
	}
	return &Deps{Paths: paths, State: store, Source: src}
}

func rec(version, ref string) domain.ReleaseRecord {
	return domain.ReleaseRecord{
		SchemaVersion: domain.InstallationSchemaVersion,
		Name:          "demo",
		Version:       domain.MustParseVersion(version),
		Digest:        "sha256:" + strings.Repeat("0", 64),
		Root:          "/opt/demo/releases/" + version,
		SourceRef:     ref,
	}
}

// TestCheckForUpdateRefusesRatherThanGuessing.
//
// Every row is a way the answer could be unknown. None of them may report "up
// to date": that is the answer an operator acts on, and it would be one nobody
// gave. These are detection branches -- the code that only runs when something
// is missing -- which makes them exactly the code that must not be dead.
func TestCheckForUpdateRefusesRatherThanGuessing(t *testing.T) {
	cases := []struct {
		name    string
		checkOn bool
		current domain.ReleaseRecord
		ref     string
		src     ports.ReleaseSource
		want    string
	}{
		{
			name:    "checking is not enabled and nobody asked",
			checkOn: false,
			current: rec("1.2.0", "oci://r/demo"),
			src:     listSource{},
			want:    "not enabled",
		},
		{
			name:    "no release is installed",
			checkOn: true,
			src:     listSource{},
			want:    "no release is installed",
		},
		{
			name:    "nothing records where the release came from",
			checkOn: true,
			current: rec("1.2.0", ""),
			src:     listSource{},
			want:    "no recorded release source",
		},
		{
			name:    "the transport cannot enumerate",
			checkOn: true,
			current: rec("1.2.0", "https://releases.example/demo.tar.zst"),
			src:     listSource{err: domain.ValidationError(domain.ErrUnsupported, "nope")},
			want:    "cannot list available versions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := checkDeps(t, tc.checkOn, tc.current, tc.src)
			res, err := CheckForUpdate(context.Background(), d,
				UpdateCheckOptions{Ref: tc.ref})
			if err == nil {
				t.Fatalf("expected a refusal, got %+v", res)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in the refusal, got: %v", tc.want, err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "newest available") {
				t.Error("a refusal must never read as up to date")
			}
		})
	}
}

// TestCheckForUpdateExplicitBypassesTheSetting: typing the command is the
// consent. The row above proves the setting holds for unprompted callers; this
// proves it does not silence a direct instruction.
func TestCheckForUpdateExplicitBypassesTheSetting(t *testing.T) {
	d := checkDeps(t, false, rec("1.2.0", "oci://r/demo"),
		listSource{versions: []domain.Version{domain.MustParseVersion("1.5.0")}})

	res, err := CheckForUpdate(context.Background(), d,
		UpdateCheckOptions{Explicit: true})
	if err != nil {
		t.Fatalf("an explicit check was refused: %v", err)
	}
	if !res.Available || res.Latest.String() != "1.5.0" {
		t.Errorf("want 1.5.0 available, got %+v", res)
	}
}

// TestCheckForUpdateIgnoresOlderAndEqualVersions. A registry keeps every tag it
// was ever pushed, so most of what List returns is the past.
func TestCheckForUpdateIgnoresOlderAndEqualVersions(t *testing.T) {
	d := checkDeps(t, true, rec("1.2.0", "oci://r/demo"), listSource{versions: []domain.Version{
		domain.MustParseVersion("1.0.0"),
		domain.MustParseVersion("1.2.0"),
	}})

	res, err := CheckForUpdate(context.Background(), d, UpdateCheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Available {
		t.Errorf("nothing newer exists, but the check offered %s", res.Latest)
	}
	if res.Considered != 2 {
		t.Errorf("considered = %d, want 2", res.Considered)
	}
}

func nowForTest() time.Time { return time.Unix(1767225600, 0).UTC() }

// TestUpdateCheckSummaryNamesTheRightVersions.
//
// The one string an operator reads. Both branches, because a summary that says
// "available" while naming the installed version is the mistake worth catching.
func TestUpdateCheckSummaryNamesTheRightVersions(t *testing.T) {
	available := UpdateCheckResult{
		Installed: domain.MustParseVersion("1.2.0"),
		Latest:    domain.MustParseVersion("1.5.0"),
		Available: true,
	}
	if got := available.Summary(); !strings.Contains(got, "1.5.0 is available") ||
		!strings.Contains(got, "installed 1.2.0") {
		t.Errorf("summary = %q", got)
	}

	upToDate := UpdateCheckResult{Installed: domain.MustParseVersion("1.5.0")}
	if got := upToDate.Summary(); !strings.Contains(got, "nothing newer is offered") {
		t.Errorf("summary = %q", got)
	}
	if strings.Contains(upToDate.Summary(), "available") {
		t.Error("an up-to-date summary must not read as an offer")
	}
}

// TestRecordedSourceRefPreservesWhatUpdateWrote.
//
// `update --to <version>` installs from the store and introduces no new ref, so
// the record must inherit whatever was already known for that release rather
// than being blanked. This is the half of the `--to` path that *can* be
// answered; a release only ever fetched and never installed has no ref anywhere
// on the machine, and CheckForUpdate says so rather than guessing (see the
// "nothing records where the release came from" row above).
func TestRecordedSourceRefPreservesWhatUpdateWrote(t *testing.T) {
	paths := domain.PathsUnder(t.TempDir(), "demo")
	store := state.New(paths)
	ctx := context.Background()

	previous := rec("1.4.0", "oci://registry.example/demo/bundle")
	current := rec("1.5.0", "oci://registry.example/demo/bundle")
	if err := store.SetCurrentRelease(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentRelease(ctx, current); err != nil {
		t.Fatal(err)
	}

	d := &Deps{Paths: paths, State: store}

	if got := d.recordedSourceRef(ctx, current.Root); got != current.SourceRef {
		t.Errorf("current release ref = %q, want %q", got, current.SourceRef)
	}
	if got := d.recordedSourceRef(ctx, previous.Root); got != previous.SourceRef {
		t.Errorf("previous release ref = %q, want %q -- rolling back or "+
			"--to'ing back must not lose it", got, previous.SourceRef)
	}
	if got := d.recordedSourceRef(ctx, "/opt/demo/releases/9.9.9"); got != "" {
		t.Errorf("a release nothing has installed reported %q; it must not guess", got)
	}
}

// TestSourceRefIsStoredWithoutCredentials.
//
// SourceRef is read back into `status`, `doctor` and the JSON envelope, so a
// password in the ref an operator typed would surface in all three.
func TestSourceRefIsStoredWithoutCredentials(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://user:hunter2@releases.example/demo.tar.zst",
			"https://user@releases.example/demo.tar.zst"},
		{"https://releases.example/demo.tar.zst",
			"https://releases.example/demo.tar.zst"},
		{"oci://registry.example/demo/bundle", "oci://registry.example/demo/bundle"},
		{"./local/bundle", "./local/bundle"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ports.RedactRefCredentials(tc.in); got != tc.want {
			t.Errorf("RedactRefCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Contains(ports.RedactRefCredentials(tc.in), "hunter2") {
			t.Errorf("a credential survived redaction of %q", tc.in)
		}
	}
}
