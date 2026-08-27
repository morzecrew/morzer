package exec

import (
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// minRedactLength is the shortest value worth scrubbing. Redacting a
// three-character value would replace common substrings throughout a log,
// destroying its readability while protecting nothing an attacker could not
// guess anyway.
//
// The same number bounds what the manager will generate -- see
// domain.MinRedactableLength -- so a generated secret is never below the floor
// of the thing that keeps it out of the logs.
const minRedactLength = domain.MinRedactableLength

// redactor replaces known secret values with a placeholder.
//
// This is defence in depth, not the primary control. Secrets are supposed to
// reach tools as files; this catches the case where one nonetheless appears in
// a tool's own output -- a database URL echoed in a connection error, say.
type redactor struct {
	values []string
}

func newRedactor(values []string) *redactor {
	seen := make(map[string]bool, len(values))
	kept := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) < minRedactLength || seen[v] {
			continue
		}
		seen[v] = true
		kept = append(kept, v)
	}
	// Longest first: a short secret that is a substring of a long one must
	// not chop the long one into fragments that then fail to match.
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	return &redactor{values: kept}
}

func (r *redactor) string(s string) string {
	if len(r.values) == 0 || s == "" {
		return s
	}
	for _, v := range r.values {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, domain.Redacted)
		}
	}
	return s
}

func (r *redactor) strings(in []string) []string {
	if len(r.values) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = r.string(s)
	}
	return out
}
