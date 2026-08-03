package domain

import (
	"strings"
)

// KindInstallationExport is the document kind of an installation export.
const KindInstallationExport = "installation-export"

// InstallationExport is everything a rebuilt machine needs in order to become
// the machine that was lost: the installation's identity and configuration, the
// encrypted secret state, and a note of which release was running.
//
// It is a fourth versioned contract alongside the manifest, the installation
// and the backup manifest, and for the same reason: an export written today has
// to be readable by whatever manager an operator installs during an incident,
// which may be older or newer than the one that wrote it.
//
// What it deliberately does not contain is machine data. That is what `backup`
// is for. Two artifacts describing the same deployment with different freshness
// would force an operator, mid-incident, to work out which one is current.
type InstallationExport struct {
	APIVersion APIVersion `yaml:"api_version" json:"api_version"`
	Kind       string     `yaml:"kind" json:"kind"`

	ExportedAt Time `yaml:"exported_at" json:"exported_at"`

	// ManagerVersion is the manager that produced this. Recorded for
	// diagnosis, never enforced: refusing an export because it was written
	// by a different manager is a refusal at the worst possible moment.
	ManagerVersion Version `yaml:"manager_version" json:"manager_version"`

	// SourceHost is the hostname the export was taken on. It is here so an
	// operator holding several exports can tell them apart, and so `import`
	// can say which machine's identity it is about to assume.
	SourceHost string `yaml:"source_host" json:"source_host,omitempty"`

	Installation Installation `yaml:"installation" json:"installation"`

	Secrets ExportedSecrets `yaml:"secrets" json:"secrets"`

	// Release records what was running, by identity rather than by path. A
	// release root is a path on a machine that no longer exists; the
	// version and digest are what let an operator fetch the same bundle
	// again and know they got the same bytes.
	Release ExportedRelease `yaml:"release" json:"release,omitempty"`
}

// ExportedSecrets carries the encrypted state, never plaintext.
type ExportedSecrets struct {
	// State is the encrypted document, byte for byte as it sits in
	// secrets.sops.yaml. It is copied rather than re-encrypted: the export
	// is only ever as readable as the recipients the state already had, and
	// re-encrypting would mean decrypting first, which an export has no
	// reason to do.
	State string `yaml:"state" json:"state"`

	// Recipients records who could decrypt the state when it was exported,
	// with the role of each. Import needs the roles: it drops the machine
	// key of the host that is gone and keeps everything else, which it
	// cannot do from the public keys alone.
	Recipients []ExportedRecipient `yaml:"recipients" json:"recipients"`
}

// ExportedRecipient is one public key and what it is for.
//
// The kind is a plain string rather than ports.RecipientKind because domain
// imports nothing from this repository, and the export format is a document
// schema rather than an interface. The two vocabularies are the same by
// construction and asserted equal by the export round-trip test.
type ExportedRecipient struct {
	PublicKey string `yaml:"public_key" json:"public_key"`
	Kind      string `yaml:"kind" json:"kind"`
	Comment   string `yaml:"comment,omitempty" json:"comment,omitempty"`
}

// ExportedRelease identifies the release that was installed.
type ExportedRelease struct {
	Name    string  `yaml:"name,omitempty" json:"name,omitempty"`
	Version Version `yaml:"version,omitempty" json:"version,omitempty"`
	Digest  string  `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// IsZero reports whether no release was recorded.
func (r ExportedRelease) IsZero() bool { return r.Name == "" && r.Version.IsZero() }

// RecipientKindMachine and friends are the kind values an export uses. They
// mirror the port's recipient kinds; see ExportedRecipient.
const (
	RecipientKindMachine  = "machine"
	RecipientKindRecovery = "recovery"
	RecipientKindOperator = "operator"
)

// Validate checks an export is usable before anything on the target machine is
// touched.
//
// Every problem is reported at once. An operator rebuilding a machine during an
// incident should learn about all of them in one run rather than discovering
// the next one after each fix.
func (e InstallationExport) Validate() error {
	var v validationErrors

	if e.APIVersion == "" {
		v.add("api_version", "is required")
	} else if !isSupportedAPIVersion(e.APIVersion) {
		return IncompatibleError(ErrUnknownAPIVersion,
			"export api_version %q is not supported", e.APIVersion).
			WithHint("this manager reads: %s", joinAPIVersions(SupportedAPIVersions))
	}
	if e.Kind != KindInstallationExport {
		v.add("kind", "must be %q, got %q", KindInstallationExport, e.Kind)
	}

	if err := e.Installation.Validate(); err != nil {
		v.add("installation", "%s", AsError(err).Message)
	}

	if strings.TrimSpace(e.Secrets.State) == "" {
		// An export with no secret state describes an installation
		// whose secrets cannot be recovered, which is the one thing the
		// format exists to carry.
		v.add("secrets.state", "is required; an export without it cannot restore any secret")
	}
	if len(e.Secrets.Recipients) == 0 {
		v.add("secrets.recipients", "is required; without it import cannot tell "+
			"the lost machine's key from the recovery key")
	}

	var recovery, other int
	for i, r := range e.Secrets.Recipients {
		if strings.TrimSpace(r.PublicKey) == "" {
			v.add("secrets.recipients", "entry %d has no public key", i)
			continue
		}
		switch r.Kind {
		case RecipientKindMachine:
		case RecipientKindRecovery:
			recovery++
		default:
			other++
		}
	}
	// The whole point of an export is that some key which is not the lost
	// machine's can open it. One that carries only the dead host's key is a
	// file nobody can ever read, and saying so here is far better than
	// saying it during the recovery it was taken for.
	if recovery+other == 0 && len(e.Secrets.Recipients) > 0 {
		v.add("secrets.recipients", "the only recipient is the exporting machine's own key, "+
			"so nothing else can decrypt this export")
	}

	return v.err()
}

// NonMachineRecipients returns the recipients that survive an import: everyone
// except the machine whose host is being replaced.
func (e InstallationExport) NonMachineRecipients() []ExportedRecipient {
	out := make([]ExportedRecipient, 0, len(e.Secrets.Recipients))
	for _, r := range e.Secrets.Recipients {
		if r.Kind == RecipientKindMachine {
			continue
		}
		out = append(out, r)
	}
	return out
}
