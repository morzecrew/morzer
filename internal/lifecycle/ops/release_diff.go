package ops

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// ReleaseDiff is what `release diff` reports: what one release declares
// differently from another, and which of the bundle's files differ. Each list
// is sorted by name and leaves out what did not change.
type ReleaseDiff struct {
	From string `json:"from"`
	To   string `json:"to"`

	Images        []Change `json:"images,omitempty"`
	Parameters    []Change `json:"parameters,omitempty"`
	Secrets       []Change `json:"secrets,omitempty"`
	Compatibility []Change `json:"compatibility,omitempty"`
	Tasks         []Change `json:"tasks,omitempty"`
	Preflight     []Change `json:"preflight,omitempty"`
	Profiles      []Change `json:"profiles,omitempty"`
	Files         []Change `json:"files,omitempty"`
}

// Same says whether the two releases declare the same and hold the same files.
func (d ReleaseDiff) Same() bool {
	for _, list := range [][]Change{
		d.Images, d.Parameters, d.Secrets, d.Compatibility, d.Tasks, d.Preflight, d.Profiles, d.Files,
	} {
		if len(list) > 0 {
			return false
		}
	}
	return true
}

// Change is one name two releases declare differently. From is what the first
// declares and To what the second does: empty on the side that does not declare
// it, and on both where only the name is compared.
type Change struct {
	Name string     `json:"name"`
	Kind ChangeKind `json:"change"`
	From string     `json:"from,omitempty"`
	To   string     `json:"to,omitempty"`
}

type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeRemoved ChangeKind = "removed"
	ChangeChanged ChangeKind = "changed"
)

// DiffReleases compares two releases as they were read: their manifests, their
// secret schemas, and their files by content. Secrets, tasks and profiles are
// compared by name, since a name is what an operator acts on.
func DiffReleases(from, to domain.Release) (ReleaseDiff, error) {
	fromSecrets, err := schemaSecretNames(from)
	if err != nil {
		return ReleaseDiff{}, err
	}
	toSecrets, err := schemaSecretNames(to)
	if err != nil {
		return ReleaseDiff{}, err
	}
	fromFiles, err := bundleFiles(from.Root)
	if err != nil {
		return ReleaseDiff{}, err
	}
	toFiles, err := bundleFiles(to.Root)
	if err != nil {
		return ReleaseDiff{}, err
	}

	a, b := from.Manifest, to.Manifest
	return ReleaseDiff{
		From:          from.String(),
		To:            to.String(),
		Images:        changes(a.Images, b.Images, func(i domain.ImageSpec) string { return i.Ref }),
		Parameters:    changes(a.Parameters, b.Parameters, describeParameter),
		Secrets:       changes(fromSecrets, toSecrets, identity),
		Compatibility: changes(compatibility(a.Compatibility), compatibility(b.Compatibility), identity),
		Tasks:         changes(a.Tasks, b.Tasks, func(domain.TaskSpec) string { return "" }),
		Preflight:     changes(a.Preflight, b.Preflight, describePreflight),
		Profiles:      profileChanges(a, b),
		Files:         changes(fromFiles, toFiles, identity),
	}, nil
}

// changes compares two declarations by name. describe is what of an entry is
// compared and reported.
func changes[V any](from, to map[string]V, describe func(V) string) []Change {
	var out []Change
	for name, v := range to {
		was, ok := from[name]
		switch {
		case !ok:
			out = append(out, Change{Name: name, Kind: ChangeAdded, To: describe(v)})
		case describe(was) != describe(v):
			out = append(out, Change{Name: name, Kind: ChangeChanged, From: describe(was), To: describe(v)})
		}
	}
	for name, v := range from {
		if _, ok := to[name]; !ok {
			out = append(out, Change{Name: name, Kind: ChangeRemoved, From: describe(v)})
		}
	}
	slices.SortFunc(out, func(a, b Change) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func identity(s string) string { return s }

// describeParameter is what of a parameter the comparison reads: its type and
// its default.
func describeParameter(p domain.ParameterSpec) string {
	if p.Default == "" {
		return string(p.Type) + ", no default"
	}
	// A line count and a digest of the value delivered: the whole of a
	// multi-line default does not fit a one-line row, and the digest keeps a
	// change on any line listed (RFC 0054 decision 14).
	if p.Type == domain.ParamText {
		value, err := p.Parse(p.Default)
		if err != nil {
			value = p.Default
		}
		sum := sha256.Sum256([]byte(value))
		return fmt.Sprintf("%s, default of %s (sha256 %x)",
			p.Type, domain.Lines(domain.LineCount(value)), sum[:6])
	}
	return string(p.Type) + ", default " + p.Default
}

// describePreflight is the whole of a check's declaration, so a change to any
// field is listed (RFC 0045 decision 14): whether it refuses, its timeout, its
// command, its description and its remedy. The command keeps its argument
// boundaries and the two texts are quoted, so two declarations that differ
// never describe the same.
func describePreflight(c domain.PreflightCheck) string {
	severity := "warns"
	if c.Fatal {
		severity = "fatal"
	}
	out := severity + ", " + c.EffectiveTimeout().String() + ", " + c.CommandLine() +
		": " + strconv.Quote(c.Description)
	if c.Remedy != "" {
		out += ", remedy " + strconv.Quote(c.Remedy)
	}
	return out
}

// compatibility is the declarations an update and a rollback gate on, keyed by
// their manifest names, each only when declared. rollback_safe always is: unset
// reads as false, which the rollback gate acts on.
func compatibility(c domain.Compatibility) map[string]string {
	out := map[string]string{"rollback_safe": strconv.FormatBool(c.RollbackSafe)}
	if !c.UpgradeFrom.IsZero() {
		out["upgrade_from"] = c.UpgradeFrom.String()
	}
	if !c.MinManagerVersion.IsZero() {
		out["min_manager_version"] = c.MinManagerVersion.String()
	}
	if c.Database != "" {
		out["database"] = string(c.Database)
	}
	for name, n := range map[string]int{
		"database_schema_min":      c.DatabaseSchemaMin,
		"database_schema_max":      c.DatabaseSchemaMax,
		"database_schema_produces": c.DatabaseSchemaProduces,
	} {
		if n > 0 {
			out[name] = strconv.Itoa(n)
		}
	}
	return out
}

// schemaSecretNames is declaredSecrets refusing a schema it cannot read, which
// would otherwise report every secret removed.
func schemaSecretNames(rel domain.Release) (map[string]string, error) {
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range schema.Secrets {
		out[s.Name] = ""
	}
	return out, nil
}

// profileChanges is the declared names, and `combinable` as one entry beside
// them in layering order, since reordering it changes which overlay wins
// (RFC 0053 decision 17).
func profileChanges(a, b domain.Manifest) []Change {
	from, to := nameSet(a.ProfileNames()), nameSet(b.ProfileNames())
	if names := a.CombinableNames(); len(names) > 0 {
		from["combinable"] = strings.Join(names, ", ")
	}
	if names := b.CombinableNames(); len(names) > 0 {
		to["combinable"] = strings.Join(names, ", ")
	}
	return changes(from, to, identity)
}

func nameSet(names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = ""
	}
	return out
}

// bundleFiles digests every file a bundle holds as WriteSums does, but the
// checksum list and its signature, which change whenever anything else does.
func bundleFiles(root string) (map[string]string, error) {
	entries, err := release.ArchiveEntries(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, rel := range entries {
		if rel == ports.SumsFileName || rel == ports.SignatureFileName {
			continue
		}
		digest, err := atomicfs.DigestFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		out[rel] = digest
	}
	return out, nil
}
