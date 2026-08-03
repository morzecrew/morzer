package sopsage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// sopsFileHeader is the unencrypted metadata block SOPS writes into every
// file. Reading it directly is how recipients are listed without holding a key
// -- `doctor` must be able to report "no recovery recipient" on a machine
// whose identity is missing, which is exactly when it matters most.
type sopsFileHeader struct {
	SOPS struct {
		Age []struct {
			Recipient string `yaml:"recipient"`
		} `yaml:"age"`
		LastModified string `yaml:"lastmodified"`
	} `yaml:"sops"`
}

// recipientAnnotations is the sidecar recording what each recipient is *for*.
//
// SOPS stores only the public keys. The distinction between the machine key,
// an offline recovery key, and an operator key is a manager-level concept, and
// it has to be durable: RemoveRecipient refuses to remove the machine's own
// key, and doctor warns when no recovery key is present. Deriving that from
// the keys alone is impossible, so it is recorded alongside.
type recipientAnnotations struct {
	Recipients map[string]recipientAnnotation `yaml:"recipients"`
}

type recipientAnnotation struct {
	Kind    ports.RecipientKind `yaml:"kind"`
	Comment string              `yaml:"comment,omitempty"`
	AddedAt string              `yaml:"added_at,omitempty"`
}

func (s *Store) annotationsPath() string {
	return filepath.Join(filepath.Dir(s.file), "secrets.recipients.yaml")
}

func (s *Store) loadAnnotations() recipientAnnotations {
	out := recipientAnnotations{Recipients: map[string]recipientAnnotation{}}

	data, err := os.ReadFile(s.annotationsPath())
	if err != nil {
		// Absent or unreadable annotations degrade the display, never
		// the operation: the keys themselves are the source of truth.
		return out
	}
	if err := yaml.Unmarshal(data, &out); err != nil || out.Recipients == nil {
		return recipientAnnotations{Recipients: map[string]recipientAnnotation{}}
	}
	return out
}

func (s *Store) saveAnnotations(a recipientAnnotations) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return domain.Internal(err, "cannot serialise recipient annotations")
	}
	return atomicfs.WriteFile(s.annotationsPath(), data, 0o640)
}

// Recipients lists the public keys that can decrypt the state.
func (s *Store) Recipients(ctx context.Context) ([]ports.Recipient, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, domain.SecretsError(err, "cannot read %s", s.file)
	}

	var header sopsFileHeader
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, domain.SecretsError(err,
			"cannot read the SOPS metadata in %s", s.file).
			WithHint("the file may be corrupt; restore it from a backup")
	}

	annotations := s.loadAnnotations()
	machinePub, _ := PublicKeyFromIdentityFile(s.identityFile)

	out := make([]ports.Recipient, 0, len(header.SOPS.Age))
	for _, entry := range header.SOPS.Age {
		key := strings.TrimSpace(entry.Recipient)
		if key == "" {
			continue
		}
		r := ports.Recipient{PublicKey: key, Kind: ports.RecipientOperator}

		if ann, ok := annotations.Recipients[key]; ok {
			r.Kind = ann.Kind
			r.Comment = ann.Comment
		}
		// The machine's own key is identified by comparison, not by
		// annotation, so a lost or hand-edited sidecar cannot make the
		// manager think it is safe to remove its own access.
		if machinePub != "" && key == machinePub {
			r.Kind = ports.RecipientMachine
		}
		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindRank(out[i].Kind) < kindRank(out[j].Kind)
		}
		return out[i].PublicKey < out[j].PublicKey
	})
	return out, nil
}

func kindRank(k ports.RecipientKind) int {
	switch k {
	case ports.RecipientMachine:
		return 0
	case ports.RecipientRecovery:
		return 1
	default:
		return 2
	}
}

// AddRecipient adds a public key and re-encrypts the state for it.
func (s *Store) AddRecipient(ctx context.Context, r ports.Recipient) error {
	if err := ValidateRecipient(r.PublicKey); err != nil {
		return err
	}

	existing, err := s.Recipients(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(existing)+1)
	for _, e := range existing {
		if e.PublicKey == r.PublicKey {
			return nil // already a recipient; adding again is a no-op
		}
		keys = append(keys, e.PublicKey)
	}
	keys = append(keys, r.PublicKey)

	if err := s.reencrypt(ctx, keys); err != nil {
		return err
	}

	annotations := s.loadAnnotations()
	if r.Kind == "" {
		r.Kind = ports.RecipientOperator
	}
	annotations.Recipients[r.PublicKey] = recipientAnnotation{
		Kind:    r.Kind,
		Comment: r.Comment,
		AddedAt: s.now().UTC().Format(time.RFC3339),
	}
	return s.saveAnnotations(annotations)
}

// RemoveRecipient drops a public key and re-encrypts without it.
//
// Two removals are refused outright: the last recipient, and this machine's
// own identity. Either would leave the manager unable to read state it is
// responsible for, and the operator would not discover it until the next
// `apply` -- possibly after a reboot, with the product down.
func (s *Store) RemoveRecipient(ctx context.Context, r ports.Recipient) error {
	existing, err := s.Recipients(ctx)
	if err != nil {
		return err
	}

	var remaining []string
	var found bool
	for _, e := range existing {
		if e.PublicKey == r.PublicKey {
			found = true
			if e.Kind == ports.RecipientMachine {
				return domain.SecretsError(nil,
					"refusing to remove this machine's own recipient key").
					WithHint("removing it would lock the manager out of its own secret state")
			}
			continue
		}
		remaining = append(remaining, e.PublicKey)
	}

	if !found {
		return domain.SecretsError(domain.ErrNotFound,
			"%s is not a recipient of %s", truncateKey(r.PublicKey), s.file)
	}
	if len(remaining) == 0 {
		return domain.SecretsError(nil, "refusing to remove the last recipient").
			WithHint("the secret state would become permanently undecryptable")
	}

	if err := s.reencrypt(ctx, remaining); err != nil {
		return err
	}

	annotations := s.loadAnnotations()
	delete(annotations.Recipients, r.PublicKey)
	return s.saveAnnotations(annotations)
}

// ReencryptFor replaces the recipient set wholesale.
//
// The refusals RemoveRecipient enforces -- never the last recipient, never this
// machine's own key -- are deliberately not applied here beyond the empty-set
// check. This is the method recovery uses, and recovery's whole job is to drop
// a machine key that belongs to a host that no longer exists.
//
// The annotations are replaced rather than merged: a leftover annotation for a
// key that is no longer a recipient would make `secret recipients list`
// describe access nobody has.
func (s *Store) ReencryptFor(ctx context.Context, recipients []ports.Recipient) error {
	if len(recipients) == 0 {
		return domain.SecretsError(nil, "refusing to re-encrypt for an empty recipient set").
			WithHint("the secret state would become permanently undecryptable")
	}

	keys := make([]string, 0, len(recipients))
	annotations := recipientAnnotations{Recipients: map[string]recipientAnnotation{}}
	now := s.now().UTC().Format(time.RFC3339)

	for _, r := range recipients {
		key := strings.TrimSpace(r.PublicKey)
		// Validated before anything is written. A malformed key that
		// happened to parse would encrypt the state to nobody, and the
		// operator would not find out until the next read.
		if err := ValidateRecipient(key); err != nil {
			return err
		}
		if _, seen := annotations.Recipients[key]; seen {
			continue
		}
		kind := r.Kind
		if kind == "" {
			kind = ports.RecipientOperator
		}
		annotations.Recipients[key] = recipientAnnotation{
			Kind:    kind,
			Comment: r.Comment,
			AddedAt: now,
		}
		keys = append(keys, key)
	}

	if err := s.reencrypt(ctx, keys); err != nil {
		return err
	}
	return s.saveAnnotations(annotations)
}

// WithIdentity returns a view of the same encrypted state that decrypts using
// a different age identity, satisfying ports.RecoverableSecretStore.
//
// Everything else is shared: same file, same runner, same clock. Only the key
// used to open it differs, which is exactly what reading state on a rebuilt
// machine requires.
func (s *Store) WithIdentity(identityFile string) ports.SecretStore {
	clone := *s
	clone.identityFile = identityFile
	return &clone
}

// ExportState returns the encrypted document verbatim.
//
// It never decrypts. An export is only ever as readable as the recipients the
// state already had, so decrypting in order to re-encrypt would put plaintext
// in a process that has no need for it and no way to benefit from it.
func (s *Store) ExportState(ctx context.Context) ([]byte, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, domain.SecretsError(domain.ErrNotFound,
				"there is no secret state at %s to export", s.file).
				WithHint("run `morzer init` first, or set a secret to create it")
		}
		return nil, domain.SecretsError(err, "cannot read %s", s.file)
	}
	return data, nil
}

// ImportState replaces the encrypted state with bytes from an export.
//
// The shape is checked first. Writing something unreadable over the secret
// state is the one failure here that re-running cannot fix, so a document with
// no age recipients is refused before the existing file is touched.
func (s *Store) ImportState(ctx context.Context, state []byte) error {
	if len(strings.TrimSpace(string(state))) == 0 {
		return domain.SecretsError(nil, "refusing to import empty secret state")
	}

	var header sopsFileHeader
	if err := yaml.Unmarshal(state, &header); err != nil {
		return domain.SecretsError(err, "the exported secret state is not a SOPS document").
			WithHint("the export may be truncated; check the file it came from")
	}
	if len(header.SOPS.Age) == 0 {
		return domain.SecretsError(nil,
			"the exported secret state names no age recipients").
			WithHint("nothing would be able to decrypt it; the export is unusable")
	}

	// 0600, the same mode saveWithRecipients writes. Encryption is not a
	// licence to widen filesystem permissions.
	return atomicfs.WriteFile(s.file, state, 0o600)
}

// reencrypt rewrites the file for a new recipient set.
//
// It decrypts and re-encrypts through this package rather than using `sops
// updatekeys`, because updatekeys reads its recipient list from a .sops.yaml
// config file. Introducing a second source of truth for who can decrypt would
// mean the manager and SOPS could disagree about it.
func (s *Store) reencrypt(ctx context.Context, recipients []string) error {
	doc, err := s.load(ctx)
	if err != nil {
		return err
	}
	return s.saveWithRecipients(ctx, doc, recipients, nil)
}

// PublicKeyFromIdentityFile derives the age public key from a private
// identity file.
//
// The file is read and parsed in process: shelling out to `age-keygen -y`
// would put the identity through another process's argv and stdout for no
// benefit, and would add age as a required binary.
func PublicKeyFromIdentityFile(path string) (string, error) {
	if path == "" {
		return "", domain.SecretsError(nil, "no age identity file is configured")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", domain.SecretsError(domain.ErrNotFound,
				"the age identity file %s does not exist", path).
				WithHint("run `morzer init`, or restore the identity from your backup")
		}
		return "", domain.SecretsError(err, "cannot read the age identity at %s", path)
	}

	identities, err := age.ParseIdentities(strings.NewReader(string(data)))
	if err != nil {
		return "", domain.SecretsError(err,
			"the age identity at %s is not valid", path).
			WithHint("the file should contain a line starting with AGE-SECRET-KEY-")
	}
	if len(identities) == 0 {
		return "", domain.SecretsError(nil, "the age identity at %s contains no keys", path)
	}

	x25519, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return "", domain.SecretsError(nil,
			"the age identity at %s is not an X25519 key", path)
	}
	return x25519.Recipient().String(), nil
}

// GenerateIdentity creates a new age identity and writes it to path with 0400
// permissions.
//
// The file is created before it is written and never widened afterwards: a
// private key that exists for even a moment at 0644 is a private key that
// could have been read.
func GenerateIdentity(path string) (publicKey string, err error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", domain.SecretsError(err, "cannot generate an age identity")
	}

	if err := atomicfs.MkdirExact(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}

	contents := "# created by morzer\n" +
		"# public key: " + identity.Recipient().String() + "\n" +
		identity.String() + "\n"

	if err := atomicfs.WriteFile(path, []byte(contents), 0o400); err != nil {
		return "", domain.SecretsError(err, "cannot write the age identity to %s", path)
	}
	return identity.Recipient().String(), nil
}

// ValidateRecipient checks that a string is a usable age public key, before it
// is written anywhere.
//
// Catching a typo here matters more than usual: a malformed recipient added to
// the file would either fail the re-encryption, or -- worse, if it happened to
// parse -- silently encrypt the state to a key nobody holds.
func ValidateRecipient(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.SecretsError(nil, "the recipient key is empty")
	}
	if _, err := age.ParseX25519Recipient(key); err != nil {
		return domain.SecretsError(err, "%q is not a valid age recipient", truncateKey(key)).
			WithHint("age public keys start with `age1` and are 62 characters long")
	}
	return nil
}

func truncateKey(k string) string {
	if len(k) <= 20 {
		return k
	}
	return k[:12] + "…" + k[len(k)-6:]
}

// EnsureIdentity creates the machine's age identity if it is absent and
// returns its public half.
//
// Idempotent by necessity: regenerating an identity would leave every existing
// encrypted value unreadable, so an existing one is returned untouched.
func (s *Store) EnsureIdentity(ctx context.Context) (string, error) {
	if pub, err := PublicKeyFromIdentityFile(s.identityFile); err == nil {
		return pub, nil
	}

	exists, err := atomicfs.Exists(s.identityFile)
	if err != nil {
		return "", err
	}
	if exists {
		// Present but unreadable or malformed. Overwriting it would
		// destroy the only key that can read the secret state.
		return "", domain.SecretsError(nil,
			"the age identity at %s exists but cannot be parsed", s.identityFile).
			WithHint("refusing to replace it; restore the original from your backup")
	}

	return GenerateIdentity(s.identityFile)
}

// IdentityPublicKey returns the machine's public key without creating one.
func (s *Store) IdentityPublicKey(ctx context.Context) (string, error) {
	return PublicKeyFromIdentityFile(s.identityFile)
}

// ValidateRecipient checks that a string is a usable age public key.
func (s *Store) ValidateRecipient(key string) error { return ValidateRecipient(key) }
