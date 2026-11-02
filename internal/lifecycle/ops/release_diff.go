package ops

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"

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
	Profiles      []Change `json:"profiles,omitempty"`
	Files         []Change `json:"files,omitempty"`
}

// Same says whether the two releases declare the same and hold the same files.
func (d ReleaseDiff) Same() bool {
	for _, list := range [][]Change{d.Images, d.Parameters, d.Secrets, d.Compatibility, d.Tasks, d.Profiles, d.Files} {
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
		Profiles:      changes(nameSet(a.ProfileNames()), nameSet(b.ProfileNames()), identity),
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
	return string(p.Type) + ", default " + p.Default
}

// compatibility is the declarations an update and a rollback gate on, keyed by
// their manifest names, each only when declared.
func compatibility(c domain.Compatibility) map[string]string {
	out := map[string]string{}
	if !c.UpgradeFrom.IsZero() {
		out["upgrade_from"] = c.UpgradeFrom.String()
	}
	if !c.MinManagerVersion.IsZero() {
		out["min_manager_version"] = c.MinManagerVersion.String()
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
