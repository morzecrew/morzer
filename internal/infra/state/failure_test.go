package state_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
	"github.com/morzecrew/morzer/internal/ports"
)

// The state store is what `--resume` reads after a crash, so the interesting
// cases are the ones a crash produces: a half-written journal line, a file
// that was truncated, a schema from a manager that has not been installed yet.
// The contract suite covers the happy paths; these are what a machine looks
// like when something already went wrong.

func store(t *testing.T) (*state.Store, domain.Paths) {
	t.Helper()
	paths := domain.PathsUnder(t.TempDir(), "demo")
	return state.New(paths), paths
}

func installation() domain.Installation {
	return domain.Installation{
		ID:        "01JZZ0000000000000000000AB",
		Product:   "demo",
		CreatedAt: domain.NewTime(time.Now()),
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestLoadingAnInstallationThatWasNeverCreated(t *testing.T) {
	s, _ := store(t)

	exists, err := s.InstallationExists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("a fresh directory reported an installation")
	}

	_, err = s.LoadInstallation(context.Background())
	if err == nil {
		t.Fatal("a missing installation loaded")
	}
	de := domain.AsError(err)
	if !strings.Contains(de.Hint, "morzer init") {
		t.Errorf("hint %q does not tell a new operator what to run", de.Hint)
	}
}

func TestInstallationStateThatIsNotJSON(t *testing.T) {
	s, paths := store(t)
	write(t, paths.InstallationState(), "{this was half-written when the power went")

	_, err := s.LoadInstallation(context.Background())
	if err == nil {
		t.Fatal("a corrupt installation state loaded")
	}
	de := domain.AsError(err)
	if !strings.Contains(de.Message, "not valid JSON") {
		t.Errorf("message %q does not say what is wrong", de.Message)
	}
	if !strings.Contains(de.Hint, "backup") {
		t.Errorf("hint %q does not offer a way out", de.Hint)
	}
}

// TestAnInstallationFromANewerManagerIsRefusedClearly is half of the
// compatibility rule: a new manager works with an old installation, and an old
// manager refuses a new one rather than guessing.
func TestAnInstallationFromANewerManagerIsRefusedClearly(t *testing.T) {
	s, paths := store(t)
	write(t, paths.InstallationState(),
		`{"schema_version":99,"installation":{"schema_version":99,"id":"01JZZ0000000000000000000AB","product":"demo"}}`)

	_, err := s.LoadInstallation(context.Background())
	if err == nil {
		t.Fatal("an installation from the future was loaded and would then be " +
			"written back in the older shape")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the refusal does not name the schema it found: %v", err)
	}
}

// TestAnInstallationFromAnOlderSchemaHasNoMigrationPathYet records the state
// of the migration ladder: there is exactly one schema, so anything below it
// is refused rather than silently upgraded.
func TestAnInstallationFromAnOlderSchemaHasNoMigrationPathYet(t *testing.T) {
	if domain.InstallationSchemaVersion < 2 {
		t.Skip("only one schema version exists, so there is nothing older")
	}

	s, paths := store(t)
	write(t, paths.InstallationState(),
		`{"schema_version":1,"installation":{"id":"01JZZ0000000000000000000AB","product":"demo"}}`)

	_, err := s.LoadInstallation(context.Background())
	if err == nil {
		t.Fatal("a schema with no migration path was loaded anyway")
	}
	if !strings.Contains(err.Error(), "no migration path") {
		t.Errorf("the refusal does not say a migration is missing: %v", err)
	}
}

func TestSavingAnInvalidInstallationIsRefusedBeforeItReachesDisk(t *testing.T) {
	s, paths := store(t)

	err := s.SaveInstallation(context.Background(), domain.Installation{Product: "demo"})
	if err == nil {
		t.Fatal("an installation with no ID was written")
	}
	if _, statErr := os.Stat(paths.InstallationState()); statErr == nil {
		t.Error("the invalid state reached disk before being refused, so a " +
			"later read finds it")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	want := installation()
	if err := s.SaveInstallation(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadInstallation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Product != want.Product {
		t.Errorf("round trip lost data: %+v", got)
	}
	// Filled in on the way out, so an installation written by an older
	// manager gets a version rather than a zero.
	if got.SchemaVersion != domain.InstallationSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion,
			domain.InstallationSchemaVersion)
	}
}

// TestNoReleaseInstalledIsANormalState, not an error: `status` on a fresh
// machine has to work.
func TestNoReleaseInstalledIsANormalState(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	for name, read := range map[string]func() (domain.ReleaseRecord, error){
		"current":  func() (domain.ReleaseRecord, error) { return s.CurrentRelease(ctx) },
		"previous": func() (domain.ReleaseRecord, error) { return s.PreviousRelease(ctx) },
	} {
		rec, err := read()
		if err != nil {
			t.Errorf("%s release on a fresh machine failed: %v", name, err)
		}
		if !rec.IsZero() {
			t.Errorf("%s release returned something from nothing: %+v", name, rec)
		}
	}
}

func TestACorruptReleaseRecordIsReportedRatherThanIgnored(t *testing.T) {
	s, paths := store(t)
	write(t, paths.CurrentReleaseFile(), "not json at all")

	_, err := s.CurrentRelease(context.Background())
	if err == nil {
		t.Fatal("a corrupt release record was read as no release, which would " +
			"make `rollback` think there was nothing to roll back from")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("the failure does not say what is wrong: %v", err)
	}
}

// TestReapplyingTheSameReleaseKeepsThePreviousPointer. After `apply` runs
// twice, rollback must still reach the release before this one.
func TestReapplyingTheSameReleaseKeepsThePreviousPointer(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	v1 := domain.ReleaseRecord{Version: domain.MustParseVersion("1.0.0"), Root: "/opt/demo/1.0.0"}
	v2 := domain.ReleaseRecord{Version: domain.MustParseVersion("2.0.0"), Root: "/opt/demo/2.0.0"}

	if err := s.SetCurrentRelease(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrentRelease(ctx, v2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrentRelease(ctx, v2); err != nil {
		t.Fatal(err)
	}

	prev, err := s.PreviousRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := prev.Version.String(); got != "1.0.0" {
		t.Errorf("previous = %s, want 1.0.0: re-applying the same release "+
			"clobbered the rollback target", got)
	}
}

// TestTheFirstReleaseHasNoPrevious, and rollback has to be able to tell.
func TestTheFirstReleaseHasNoPrevious(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	if err := s.SetCurrentRelease(ctx,
		domain.ReleaseRecord{Version: domain.MustParseVersion("1.0.0")}); err != nil {
		t.Fatal(err)
	}

	prev, err := s.PreviousRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !prev.IsZero() {
		t.Errorf("a first install has a previous release: %+v", prev)
	}
}

// TestACorruptFinalJournalLineIsDiscardedNotFatal is the crash case: the
// operation that line described is, by definition, one that did not finish,
// and the record before it still says where things stood.
func TestACorruptFinalJournalLineIsDiscardedNotFatal(t *testing.T) {
	s, paths := store(t)
	ctx := context.Background()

	good := domain.OperationRecord{
		ID: "01JAAA0000000000000000000A", Type: domain.OpTypeUpdate,
		Status: domain.StatusSucceeded, StartedAt: domain.NewTime(time.Now()),
	}
	if err := s.AppendOperation(ctx, good); err != nil {
		t.Fatal(err)
	}

	// A partial line, exactly as a crash mid-append leaves one.
	f, err := os.OpenFile(paths.JournalFile(), os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"01JBBB000000000000000000` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	recs, err := s.Operations(ctx, ports.Filter{})
	if err != nil {
		t.Fatalf("one bad line made the whole journal unreadable, which breaks "+
			"`status` and `doctor` exactly when they are needed: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != good.ID {
		t.Errorf("the surviving record was lost: %+v", recs)
	}
}

func TestReadingAJournalThatDoesNotExistYet(t *testing.T) {
	s, _ := store(t)

	recs, err := s.Operations(context.Background(), ports.Filter{})
	if err != nil {
		t.Fatalf("a machine with no history failed: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("records appeared from nothing: %+v", recs)
	}

	if _, found, err := s.LastOperation(context.Background()); err != nil || found {
		t.Errorf("LastOperation on an empty journal: found=%v err=%v", found, err)
	}
}

// TestAnOversizeRecordIsTruncatedRatherThanDropped. A journal entry saying
// "this happened, details elided" is far more useful than a missing one.
func TestAnOversizeRecordIsTruncatedRatherThanDropped(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	steps := make([]domain.StepRecord, 0, 20000)
	for i := range 20000 {
		steps = append(steps, domain.StepRecord{
			ID:      strings.Repeat("a-very-long-step-name", 4),
			Status:  domain.StepSucceeded,
			Message: strings.Repeat("x", 64) + string(rune('a'+i%26)),
		})
	}

	rec := domain.OperationRecord{
		ID: "01JCCC0000000000000000000A", Type: domain.OpTypeUpdate,
		Status: domain.StatusSucceeded, StartedAt: domain.NewTime(time.Now()),
		Steps: steps,
	}
	if err := s.AppendOperation(ctx, rec); err != nil {
		t.Fatalf("an oversize record was dropped instead of truncated: %v", err)
	}

	got, err := s.Operations(ctx, ports.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the truncated record did not survive the round trip: %+v", got)
	}
	if len(got[0].Steps) != 0 {
		t.Error("the step list was not the thing dropped")
	}
	if got[0].Flags["truncated"] == "" {
		t.Error("the record does not say that detail was elided, so a reader " +
			"would think the operation had no steps")
	}
}

func TestOperationsCollapsesEachIDToItsLatestState(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	base := time.Now()
	for _, rec := range []domain.OperationRecord{
		{ID: "op-a", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
			StartedAt: domain.NewTime(base)},
		{ID: "op-a", Type: domain.OpTypeUpdate, Status: domain.StatusSucceeded,
			StartedAt: domain.NewTime(base)},
		{ID: "op-b", Type: domain.OpTypeBackup, Status: domain.StatusFailed,
			StartedAt: domain.NewTime(base.Add(time.Minute))},
	} {
		if err := s.AppendOperation(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.Operations(ctx, ports.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d operations for 2 ids: %+v", len(all), all)
	}
	// Newest first, which is the order `status` prints.
	if all[0].ID != "op-b" {
		t.Errorf("the newest operation is not first: %+v", all)
	}
	for _, rec := range all {
		if rec.ID == "op-a" && rec.Status != domain.StatusSucceeded {
			t.Errorf("op-a is reported as %s, not its latest state", rec.Status)
		}
	}
}

func TestOperationsFilters(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	base := time.Now()
	records := []domain.OperationRecord{
		{ID: "op-1", Type: domain.OpTypeUpdate, Status: domain.StatusSucceeded,
			StartedAt: domain.NewTime(base)},
		{ID: "op-2", Type: domain.OpTypeBackup, Status: domain.StatusFailed,
			StartedAt: domain.NewTime(base.Add(time.Minute))},
		{ID: "op-3", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
			StartedAt: domain.NewTime(base.Add(2 * time.Minute))},
	}
	for _, rec := range records {
		if err := s.AppendOperation(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	cases := map[string]struct {
		filter ports.Filter
		want   int
	}{
		"by id":                    {ports.Filter{ID: "op-2"}, 1},
		"by type":                  {ports.Filter{Type: domain.OpTypeUpdate}, 2},
		"by status":                {ports.Filter{Status: domain.StatusFailed}, 1},
		"limited":                  {ports.Filter{Limit: 2}, 2},
		"a filter nothing matches": {ports.Filter{ID: "op-never"}, 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := s.Operations(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d records, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

// TestUnfinishedOperationsIsWhatResumeActsOn.
func TestUnfinishedOperationsIsWhatResumeActsOn(t *testing.T) {
	s, _ := store(t)
	ctx := context.Background()

	base := time.Now()
	for _, rec := range []domain.OperationRecord{
		{ID: "done", Type: domain.OpTypeUpdate, Status: domain.StatusSucceeded,
			StartedAt: domain.NewTime(base)},
		{ID: "stopped", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
			StartedAt: domain.NewTime(base.Add(time.Minute))},
		{ID: "needs-a-human", Type: domain.OpTypeUpdate,
			Status: domain.StatusManualIntervention, StartedAt: domain.NewTime(base.Add(2 * time.Minute))},
		{ID: "compensated", Type: domain.OpTypeUpdate, Status: domain.StatusCompensated,
			StartedAt: domain.NewTime(base.Add(3 * time.Minute))},
	} {
		if err := s.AppendOperation(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	unfinished, err := s.UnfinishedOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, rec := range unfinished {
		got[rec.ID] = true
	}
	if !got["stopped"] {
		t.Error("an operation left running was not reported unfinished, so " +
			"--resume would find nothing after a crash")
	}
	if !got["needs-a-human"] {
		t.Error("an operation needing intervention was dropped, and doctor is " +
			"supposed to keep surfacing it until cleared")
	}
	if got["done"] || got["compensated"] {
		t.Errorf("a terminal operation was reported unfinished: %v", got)
	}
}

// TestAppendingToAJournalItCannotOpen is the fault-injection case: /var
// remounted read-only, or a full disk.
func TestAppendingToAJournalItCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	root := t.TempDir()
	s := state.New(domain.PathsUnder(root, "demo"))
	ctx := context.Background()

	// Create the manager directory, then make it unwritable.
	if err := s.AppendOperation(ctx, domain.OperationRecord{
		ID: "op-1", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
		StartedAt: domain.NewTime(time.Now()),
	}); err != nil {
		t.Fatal(err)
	}

	managerDir := domain.PathsUnder(root, "demo").ManagerDir()
	journal := domain.PathsUnder(root, "demo").JournalFile()
	if err := os.Chmod(journal, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(managerDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(managerDir, 0o750)
		_ = os.Chmod(journal, 0o640)
	})

	err := s.AppendOperation(ctx, domain.OperationRecord{
		ID: "op-2", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
		StartedAt: domain.NewTime(time.Now()),
	})
	if err == nil {
		t.Fatal("a journal append succeeded against a read-only file, so a " +
			"machine with a full or remounted /var would report operations " +
			"as recorded when they were not")
	}
	if !strings.Contains(err.Error(), "journal") {
		t.Errorf("the failure does not name the journal: %v", err)
	}
}

// TestReadingAJournalItCannotOpen: an unreadable journal is reported, not
// treated as an empty history, which would make `--resume` skip a half-done
// operation.
func TestReadingAJournalItCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}

	s, paths := store(t)
	ctx := context.Background()

	if err := s.AppendOperation(ctx, domain.OperationRecord{
		ID: "op-1", Type: domain.OpTypeUpdate, Status: domain.StatusRunning,
		StartedAt: domain.NewTime(time.Now()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.JournalFile(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(paths.JournalFile(), 0o640) })

	_, err := s.Operations(ctx, ports.Filter{})
	if err == nil {
		t.Fatal("an unreadable journal was reported as an empty history")
	}
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(err.Error(), "journal") {
		t.Errorf("the failure does not say what could not be read: %v", err)
	}
}
