package domain

import (
	"path/filepath"
	"regexp"
)

// productNamePattern constrains what may appear in a filesystem path derived
// from a manifest. The manifest is release-supplied input, and its name lands
// in /etc, /var/lib and /run -- so it is validated as a path component, not
// merely as a string.
//
// The name must also start with a letter, because it is not only a path
// component: uppercased, it is the environment-variable namespace every hook
// and every Compose interpolation reads (ports.HookEnv.Prefix). A product
// called "3cx" would produce ${3CX_PARAM_...}, which no POSIX shell and no
// Compose file can reference -- every parameter would silently resolve to its
// `:-` fallback, which is a working deployment configured with none of the
// operator's values.
var productNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidateProductName rejects names that would escape or collide with the
// directories the manager owns, or that cannot be an environment-variable
// prefix.
func ValidateProductName(name string) error {
	if !productNamePattern.MatchString(name) {
		return ValidationError(nil, "invalid product name %q", name).
			WithHint("names are lowercase alphanumeric with dashes, 1-63 characters, " +
				"starting with a letter -- the name is also the environment-variable " +
				"prefix hooks read, and a variable cannot start with a digit")
	}
	return nil
}

// Paths is the on-disk layout for one product. Every directory the manager
// touches is derived here, so relocating the layout (for tests, for a
// rootless install) is a single constructor call rather than a grep.
type Paths struct {
	Product string

	// EtcDir holds machine-specific configuration and the encrypted secret
	// state. Persistent, backed up, operator-editable.
	EtcDir string

	// VarDir holds persistent runtime state: application data, backups, and
	// the manager's own journal.
	VarDir string

	// RunDir is ephemeral and expected to be tmpfs. Cleared on reboot;
	// decrypted secrets live here and nowhere else.
	RunDir string

	// OptDir holds unpacked releases and the current/previous symlinks.
	OptDir string
}

// DefaultPaths returns the production layout for a product.
func DefaultPaths(product string) Paths {
	return Paths{
		Product: product,
		EtcDir:  filepath.Join("/etc", product),
		VarDir:  filepath.Join("/var/lib", product),
		RunDir:  filepath.Join("/run", product),
		OptDir:  filepath.Join("/opt", product),
	}
}

// PathsUnder returns the same layout rooted at an arbitrary prefix. This is
// what makes every operation testable without root: the tests run the real
// code against a temp directory, not against a mock filesystem.
func PathsUnder(root, product string) Paths {
	p := DefaultPaths(product)
	p.EtcDir = filepath.Join(root, p.EtcDir)
	p.VarDir = filepath.Join(root, p.VarDir)
	p.RunDir = filepath.Join(root, p.RunDir)
	p.OptDir = filepath.Join(root, p.OptDir)
	return p
}

// Configuration and secret state.

// InstallationFileName is the operator-facing state file inside EtcDir. Named
// rather than repeated: --config identifies an installation by this path, and
// the CLI has to recognise the same name the layout produces.
const InstallationFileName = "installation.yaml"

func (p Paths) InstallationFile() string { return filepath.Join(p.EtcDir, InstallationFileName) }
func (p Paths) ApplicationFile() string  { return filepath.Join(p.EtcDir, "application.yaml") }
func (p Paths) SecretsFile() string      { return filepath.Join(p.EtcDir, "secrets.sops.yaml") }
func (p Paths) AgeDir() string           { return filepath.Join(p.EtcDir, "age") }
func (p Paths) AgeIdentityFile() string  { return filepath.Join(p.EtcDir, "age", "identity") }

// Persistent state.

func (p Paths) DataDir() string    { return filepath.Join(p.VarDir, "data") }
func (p Paths) BackupsDir() string { return filepath.Join(p.VarDir, "backups") }
func (p Paths) ManagerDir() string { return filepath.Join(p.VarDir, "manager") }
func (p Paths) StagingDir() string { return filepath.Join(p.VarDir, "manager", "staging") }
func (p Paths) InstallationState() string {
	return filepath.Join(p.ManagerDir(), "installation.json")
}
func (p Paths) CurrentReleaseFile() string {
	return filepath.Join(p.ManagerDir(), "current-release.json")
}
func (p Paths) PreviousReleaseFile() string {
	return filepath.Join(p.ManagerDir(), "previous-release.json")
}

// UpdateCandidateFile records what a followed channel last pointed at.
//
// Beside the release pointers rather than in the installation state, because it
// is derived: a poll writes it, `status` reads it, and deleting it costs one
// fetch. Nothing about what is deployed depends on it.
func (p Paths) UpdateCandidateFile() string {
	return filepath.Join(p.ManagerDir(), "update-candidate.json")
}

func (p Paths) JournalFile() string { return filepath.Join(p.ManagerDir(), "operations.jsonl") }
func (p Paths) LockDir() string     { return filepath.Join(p.ManagerDir(), "locks") }
func (p Paths) LockFile(name string) string {
	return filepath.Join(p.LockDir(), name+".lock")
}

// Ephemeral state. Decrypted secrets never leave RunDir.

func (p Paths) SecretsRenderDir() string { return filepath.Join(p.RunDir, "secrets") }
func (p Paths) GeneratedDir() string     { return filepath.Join(p.RunDir, "generated") }

// Release storage.

func (p Paths) ReleasesDir() string { return filepath.Join(p.OptDir, "releases") }
func (p Paths) ReleaseDir(version string) string {
	return filepath.Join(p.OptDir, "releases", version)
}
func (p Paths) CurrentLink() string  { return filepath.Join(p.OptDir, "current") }
func (p Paths) PreviousLink() string { return filepath.Join(p.OptDir, "previous") }

// ManagedDirs enumerates the directories `init` creates and `doctor` audits,
// paired with the mode each must carry. Ordered parent-first so a single pass
// creates them correctly.
func (p Paths) ManagedDirs() []ManagedDir {
	return []ManagedDir{
		{Path: p.EtcDir, Mode: 0o750},
		{Path: p.AgeDir(), Mode: 0o700},
		{Path: p.VarDir, Mode: 0o750},
		{Path: p.DataDir(), Mode: 0o750},
		{Path: p.BackupsDir(), Mode: 0o700},
		{Path: p.ManagerDir(), Mode: 0o750},
		{Path: p.StagingDir(), Mode: 0o750},
		{Path: p.LockDir(), Mode: 0o750},
		{Path: p.RunDir, Mode: 0o750},
		{Path: p.SecretsRenderDir(), Mode: 0o700},
		{Path: p.GeneratedDir(), Mode: 0o750},
		{Path: p.OptDir, Mode: 0o755},
		{Path: p.ReleasesDir(), Mode: 0o755},
	}
}

// ManagedDir is a directory the manager owns, with the permissions it must
// have. `doctor` compares reality against this list.
type ManagedDir struct {
	Path string
	Mode uint32
}
