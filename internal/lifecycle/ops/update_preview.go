package ops

import (
	"cmp"
	"slices"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// UpdatePreview is what `update --dry-run` adds to the plan: what the update
// risks, beside what it would run.
type UpdatePreview struct {
	// Rollback is what returning to the release installed now would take
	// once the new one is installed. Nil on a first install, or when the
	// installed release cannot be read.
	Rollback *UpdateRollback `json:"rollback,omitempty"`

	// Images lists the images whose reference the update changes, adds or
	// removes, by name. Unchanged images are left out.
	Images []ImageChange `json:"image_changes,omitempty"`

	// Notes is the new release's notes, as the vendor wrote them.
	Notes string `json:"release_notes,omitempty"`
}

// UpdateRollback is the rollback assessment run before the update, as the
// unattended gate runs it (domain.AssessUnattended).
type UpdateRollback struct {
	domain.RollbackAssessment

	To string `json:"to"`

	// SchemaChecked says whether the releases declare enough to compare the
	// database schema: the schema the new one's migrations produce, and the
	// highest the installed one reads. Without both, SchemaCompatible is
	// not a finding.
	SchemaChecked bool `json:"schema_checked"`
}

// ImageChange is one image the update moves. From is empty for an image the
// new release adds, and To for one it drops.
type ImageChange struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// previewUpdate reads the new release where it is now: a plan stages nothing,
// so the copy the store would hold is not there to read the notes from.
func previewUpdate(fallback *fallbackRelease, running, next domain.Release) UpdatePreview {
	out := UpdatePreview{Notes: release.Notes(next)}
	if fallback == nil || !fallback.Known {
		return out
	}

	target := next.Manifest.Compatibility
	out.Rollback = &UpdateRollback{
		RollbackAssessment: domain.AssessRollback(target, fallback.Compatibility, target.DatabaseSchemaProduces),
		To:                 fallback.Version.String(),
		SchemaChecked:      target.DatabaseSchemaProduces > 0 && fallback.Compatibility.DatabaseSchemaMax > 0,
	}

	before, after := running.Manifest.Images, next.Manifest.Images
	for name, img := range after {
		if was, ok := before[name]; !ok || was.Ref != img.Ref {
			out.Images = append(out.Images, ImageChange{Name: name, From: was.Ref, To: img.Ref})
		}
	}
	for name, img := range before {
		if _, ok := after[name]; !ok {
			out.Images = append(out.Images, ImageChange{Name: name, From: img.Ref})
		}
	}
	slices.SortFunc(out.Images, func(a, b ImageChange) int { return cmp.Compare(a.Name, b.Name) })
	return out
}
