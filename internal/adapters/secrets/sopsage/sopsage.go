// Package sopsage implements ports.SecretStore over the sops binary and age
// keys.
//
// SOPS is subprocessed rather than imported. The library drags in the AWS, GCP
// and Azure KMS SDKs -- tens of megabytes and a large attack surface for a
// deployment that only ever uses age. The port makes the decision reversible:
// if removing the install-time dependency ever matters more, the library goes
// behind this same interface and nothing above it changes.
//
// The rules this adapter exists to enforce:
//   - secret values never appear in argv (they go over stdin);
//   - the encrypted file is replaced atomically;
//   - rendered files are 0400 inside a 0700 directory;
//   - the age identity never leaves this process's environment.
package sopsage

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// Name is the provider name a manifest selects with providers.secrets.name.
const Name = "sops-age"

// Store is the SOPS+age secret store.
type Store struct {
	runner exec.Runner

	// file is the encrypted state, /etc/<product>/secrets.sops.yaml.
	file string

	// identityFile holds the machine's age private key.
	identityFile string

	sopsBinary string

	// now is injectable so metadata timestamps are deterministic in tests.
	now func() time.Time
}

type Option func(*Store)

func WithSOPSBinary(path string) Option {
	return func(s *Store) { s.sopsBinary = path }
}

func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// New returns a secret store backed by file, decrypting with identityFile.
func New(runner exec.Runner, file, identityFile string, opts ...Option) *Store {
	s := &Store{
		runner:       runner,
		file:         file,
		identityFile: identityFile,
		sopsBinary:   "sops",
		now:          time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

var (
	_ ports.SecretStore            = (*Store)(nil)
	_ ports.RecoverableSecretStore = (*Store)(nil)
)

// document is the decrypted shape of the SOPS file.
//
// Values and metadata are separate maps rather than one map of structs,
// because SOPS encrypts leaf values: keeping `changed_at` out of the encrypted
// tree means metadata can be read without decrypting, and means a metadata
// update does not rewrite every ciphertext.
type document struct {
	Values map[string]string    `json:"values" yaml:"values"`
	Meta   map[string]entryMeta `json:"meta,omitempty" yaml:"meta,omitempty"`
}

type entryMeta struct {
	ChangedAt string `json:"changed_at,omitempty" yaml:"changed_at,omitempty"`
}

// sopsEnv builds the environment for a sops invocation.
//
// The age identity is passed by file path through SOPS_AGE_KEY_FILE rather
// than by value through SOPS_AGE_KEY: an environment variable is visible to
// anything that can read /proc/<pid>/environ, and the key is the one secret
// that decrypts all the others.
func (s *Store) sopsEnv() map[string]string {
	return map[string]string{
		"SOPS_AGE_KEY_FILE": s.identityFile,
		// SOPS phones home for version checks by default. An operation
		// on an air-gapped host must not stall on that.
		"SOPS_DISABLE_VERSION_CHECK": "true",
	}
}

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	return atomicfs.Exists(s.file)
}

// Load decrypts the whole secret set.
func (s *Store) Load(ctx context.Context) (domain.SecretSet, error) {
	doc, err := s.load(ctx)
	if err != nil {
		return domain.SecretSet{}, err
	}
	values := make(map[string]domain.Secret, len(doc.Values))
	for k, v := range doc.Values {
		values[k] = domain.NewSecret(v)
	}
	return domain.NewSecretSet(values), nil
}

func (s *Store) load(ctx context.Context) (document, error) {
	exists, err := atomicfs.Exists(s.file)
	if err != nil {
		return document{}, err
	}
	if !exists {
		// An absent file is an empty store, not an error: `init` needs
		// to write the first secret into something.
		return document{Values: map[string]string{}, Meta: map[string]entryMeta{}}, nil
	}

	res, err := s.runner.Run(ctx, exec.Command{
		Argv:          []string{s.sopsBinary, "--decrypt", "--output-type", "json", s.file},
		Env:           exec.BaseEnv(s.sopsEnv()),
		Timeout:       60 * time.Second,
		CaptureOutput: true,
	})
	if err != nil {
		return document{}, s.decryptError(err)
	}

	var doc document
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		return document{}, domain.SecretsError(err,
			"the decrypted secret state is not in the expected format").
			WithHint("%s should contain a top-level `values` mapping", s.file)
	}
	if doc.Values == nil {
		doc.Values = map[string]string{}
	}
	if doc.Meta == nil {
		doc.Meta = map[string]entryMeta{}
	}
	return doc, nil
}

// decryptError turns SOPS's failure modes into messages that name a remedy.
func (s *Store) decryptError(err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return domain.SecretsError(err, "cannot decrypt %s", s.file)
	}

	stderr := strings.ToLower(exitErr.Stderr)
	switch {
	case strings.Contains(stderr, "no identity matched") ||
		strings.Contains(stderr, "could not decrypt data key"):
		return domain.SecretsError(err,
			"cannot decrypt %s: this machine's age identity is not a recipient", s.file).
			WithHint("the identity at %s does not match any recipient of the file. "+
				"Restore the original identity, or re-encrypt using the offline recovery key.",
				s.identityFile)
	case strings.Contains(stderr, "no such file"):
		return domain.SecretsError(err,
			"cannot decrypt %s: the age identity at %s is missing", s.file, s.identityFile).
			WithHint("restore %s from your backup; without it the secret state cannot be read",
				s.identityFile)
	default:
		return domain.SecretsError(err, "cannot decrypt %s", s.file).
			WithHint("run `morzer doctor` to check the secret configuration")
	}
}

// save encrypts and writes the document atomically.
//
// The plaintext goes to sops over stdin and the ciphertext comes back over
// stdout, so at no point does a decrypted file exist on disk -- not even
// briefly in a temp file that a crash could leave behind.
func (s *Store) save(ctx context.Context, doc document, redact []string) error {
	return s.saveWithRecipients(ctx, doc, nil, redact)
}

// saveWithRecipients encrypts for an explicit recipient list. Passing nil
// reuses the file's current recipients, which is what every ordinary write
// wants: a `secret set` must never silently drop the offline recovery key.
func (s *Store) saveWithRecipients(ctx context.Context, doc document, recipients []string, redact []string) error {
	plaintext, err := json.Marshal(doc)
	if err != nil {
		return domain.Internal(err, "cannot serialise secret state")
	}

	if len(recipients) == 0 {
		recipients, err = s.recipientsForEncrypt(ctx)
		if err != nil {
			return err
		}
	}

	argv := []string{
		s.sopsBinary, "--encrypt",
		"--age", strings.Join(recipients, ","),
		"--input-type", "json",
		"--output-type", "yaml",
		// Only leaf values under `values` are encrypted, leaving key
		// names and metadata readable. An operator can then see which
		// secrets exist without holding a key.
		"--encrypted-regex", "^(values)$",
		"/dev/stdin",
	}

	res, err := s.runner.Run(ctx, exec.Command{
		Argv:          argv,
		Env:           exec.BaseEnv(s.sopsEnv()),
		Stdin:         strings.NewReader(string(plaintext)),
		Timeout:       60 * time.Second,
		Redact:        redact,
		CaptureOutput: true,
	})
	if err != nil {
		return domain.SecretsError(err, "cannot encrypt the secret state").
			WithHint("check that the recipients in %s are valid age public keys", s.file)
	}

	// 0600: the ciphertext is not readable by group or others. Encryption
	// is not a licence to widen filesystem permissions.
	return atomicfs.WriteFile(s.file, []byte(res.Stdout), 0o600)
}

// recipientsForEncrypt returns the age recipients to encrypt for.
//
// On an existing file the current recipient list is reused, so a `secret set`
// never silently drops the offline recovery key. On a new file the machine
// identity's own public key is used, and AddRecipient adds the rest.
func (s *Store) recipientsForEncrypt(ctx context.Context) ([]string, error) {
	existing, err := s.Recipients(ctx)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		out := make([]string, 0, len(existing))
		for _, r := range existing {
			out = append(out, r.PublicKey)
		}
		return out, nil
	}

	pub, err := PublicKeyFromIdentityFile(s.identityFile)
	if err != nil {
		return nil, err
	}
	return []string{pub}, nil
}

func (s *Store) Set(ctx context.Context, name string, value domain.Secret) error {
	if err := validateName(name); err != nil {
		return err
	}
	if value.IsEmpty() {
		return domain.SecretsError(nil, "refusing to store an empty value for secret %q", name).
			WithHint("use `morzer secret remove %s` to delete it", name)
	}

	doc, err := s.load(ctx)
	if err != nil {
		return err
	}
	doc.Values[name] = value.Reveal()
	doc.Meta[name] = entryMeta{ChangedAt: s.now().UTC().Format(time.RFC3339)}

	return s.save(ctx, doc, []string{value.Reveal()})
}

func (s *Store) Generate(ctx context.Context, name string, spec ports.GenSpec) error {
	if err := validateName(name); err != nil {
		return err
	}

	doc, err := s.load(ctx)
	if err != nil {
		return err
	}
	if _, exists := doc.Values[name]; exists && !spec.Overwrite {
		// Silently regenerating a live credential is the kind of
		// accident that takes a product down at the next restart.
		return domain.SecretsError(nil, "secret %q already exists", name).
			WithHint("use `morzer secret rotate %s` to replace it deliberately", name)
	}

	value, err := Generate(spec.Generator)
	if err != nil {
		return err
	}

	doc.Values[name] = value
	doc.Meta[name] = entryMeta{ChangedAt: s.now().UTC().Format(time.RFC3339)}
	return s.save(ctx, doc, []string{value})
}

func (s *Store) Rotate(ctx context.Context, name string, spec ports.GenSpec) error {
	doc, err := s.load(ctx)
	if err != nil {
		return err
	}
	if _, exists := doc.Values[name]; !exists {
		return domain.SecretsError(domain.ErrSecretNotFound, "secret %q does not exist", name).
			WithHint("run `morzer secret list` to see what is defined")
	}
	spec.Overwrite = true
	return s.Generate(ctx, name, spec)
}

func (s *Store) Remove(ctx context.Context, name string) error {
	doc, err := s.load(ctx)
	if err != nil {
		return err
	}
	if _, exists := doc.Values[name]; !exists {
		// Removing an absent secret is not an error: the postcondition
		// the caller wanted already holds, which is what idempotence
		// means.
		return nil
	}
	delete(doc.Values, name)
	delete(doc.Meta, name)
	return s.save(ctx, doc, nil)
}

func (s *Store) Metadata(ctx context.Context) ([]ports.SecretMetadata, error) {
	doc, err := s.load(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]ports.SecretMetadata, 0, len(doc.Values))
	for name, value := range doc.Values {
		m := ports.SecretMetadata{
			Name: name,
			// A truncated hash lets two installations be compared
			// without either of them printing a value.
			Fingerprint: atomicfs.FingerprintSecret(value),
			Length:      len(value),
		}
		if meta, ok := doc.Meta[name]; ok && meta.ChangedAt != "" {
			if t, err := time.Parse(time.RFC3339, meta.ChangedAt); err == nil {
				m.LastChanged = domain.NewTime(t)
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Render writes each declared secret to its own file under targetDir.
//
// Two properties matter and are both enforced here rather than assumed: the
// directory is 0700 and each file is 0400, and files not backed by a
// declaration are deleted. A stale secret file left behind after a schema
// change is a credential on disk with nobody responsible for rotating it.
func (s *Store) Render(ctx context.Context, targetDir string, schema domain.SecretSchema) ([]ports.RenderedFile, error) {
	set, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}

	if err := atomicfs.MkdirExact(targetDir, 0o700); err != nil {
		return nil, err
	}

	root, err := atomicfs.OpenRoot(targetDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var rendered []ports.RenderedFile
	expected := make(map[string]bool, len(schema.Secrets))

	for _, decl := range schema.Secrets {
		value, ok := set.Get(decl.Name)
		if !ok || value.IsEmpty() {
			if decl.Required {
				return nil, domain.SecretsError(domain.ErrSecretNotFound,
					"required secret %q is not set", decl.Name).
					WithHint("run `morzer secret set %s`, or `morzer secret generate %s` "+
						"if the release declares a generator", decl.Name, decl.Name)
			}
			continue
		}

		fileName := decl.FileName()
		expected[fileName] = true

		if err := atomicfs.WriteFileIn(root, fileName, []byte(value.Reveal()), 0o400); err != nil {
			return nil, domain.SecretsError(err, "cannot render secret %q", decl.Name)
		}

		rendered = append(rendered, ports.RenderedFile{
			Name: decl.Name,
			Path: filepath.Join(targetDir, fileName),
			Mode: 0o400,
			Size: value.Len(),
		})
	}

	if err := pruneStale(targetDir, expected); err != nil {
		return nil, err
	}
	return rendered, nil
}

// pruneStale removes files in the render directory that no declaration backs.
func pruneStale(dir string, expected map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return domain.SecretsError(err, "cannot list the secret render directory")
	}

	for _, e := range entries {
		if e.IsDir() || expected[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return domain.SecretsError(err, "cannot remove stale secret file %q", e.Name())
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return domain.SecretsError(nil, "secret name is empty")
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return domain.SecretsError(nil, "secret name %q is not usable as a filename", name)
	}
	return nil
}
