package gotemplate

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// didYouMean names the candidate a misspelt name most likely meant, or
// nothing when none is near: within a third of the name's length, at least one
// edit, ignoring case. A wrong suggestion costs more than none.
//
// Nothing longer than maxSuggestible is compared: no name that long is a typo
// of a declared one, and the distance costs the product of the two lengths.
func didYouMean(name string, candidates []string) string {
	n := len([]rune(name))
	if n > maxSuggestible {
		return ""
	}
	limit := max(1, n/3)
	best, bestDistance := "", limit+1
	for _, c := range slices.Sorted(slices.Values(candidates)) {
		// Two names further apart in length than the limit are
		// further apart than it in edits, so they are not compared.
		if d := len([]rune(c)) - n; d > limit || -d > limit {
			continue
		}
		if d := distance(strings.ToLower(name), strings.ToLower(c)); d < bestDistance {
			best, bestDistance = c, d
		}
	}
	if best == "" {
		return ""
	}
	return "; did you mean " + best + "?"
}

const maxSuggestible = 64

// distance is the optimal string alignment distance: edits, with a swap of
// two neighbours counted as one, because that is the commonest typo.
func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}

// memberNames are what a template can name on a value of this type: its
// exported fields and its value methods. The view is executed by value, so
// nothing in it is addressable and a pointer method is not callable.
func memberNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		if f := t.Field(i); f.IsExported() {
			out = append(out, f.Name)
		}
	}
	for i := range t.NumMethod() {
		out = append(out, t.Method(i).Name)
	}
	return out
}

func mapKeys(v reflect.Value) []string {
	out := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		out = append(out, k.String())
	}
	return out
}

// owners maps each struct type a template can reach to the path it reaches it
// by, so an error names `.Paths` rather than `ports.PathInfo`. Walked from the
// view, so it cannot name a type the view stopped holding.
var owners = func() map[string]reflect.Type {
	out := map[string]reflect.Type{}
	paths := map[reflect.Type]string{}
	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		if t.Kind() != reflect.Struct {
			return
		}
		if _, seen := paths[t]; seen {
			return
		}
		paths[t] = path
		out[path] = t
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(view{}), "")
	return out
}()

// ownerOf names the struct type a Go error calls `pkg.type` by its template
// path, and lists what can be named on it.
func ownerOf(goType string) (string, []string, bool) {
	for _, path := range slices.Sorted(maps.Keys(owners)) {
		if t := owners[path]; t.String() == goType {
			return parentName(cmp.Or(path, ".")), memberNames(t), true
		}
	}
	return "", nil, false
}

var (
	// (?s): a `required` message may hold a newline, and so may its cause.
	execFailure = regexp.MustCompile(`(?s)^(\d+:\d+): executing "[^"]*" at <(.*?)>: (.*)$`)
	noField     = regexp.MustCompile(`^can't evaluate field (\w+) in type (\S+)$`)
	noKey       = regexp.MustCompile(`^map has no entry for key "(.*)"$`)
	rootChain   = regexp.MustCompile(`^\$?((?:\.\w+)+)$`)
)

// describe restates text/template's execution error in the template's terms:
// where, the action as written, and what it named that is not there, with the
// name it most likely meant. A failure it cannot read is quoted as it came.
func describe(name string, err error, v view) string {
	detail := executionDetail(name, err)
	m := execFailure.FindStringSubmatch(detail)
	if m == nil {
		return detail
	}
	location, action, cause := m[1], m[2], m[3]
	if f := noField.FindStringSubmatch(cause); f != nil {
		if owner, members, ok := ownerOf(f[2]); ok {
			cause = fmt.Sprintf("%s has no field %s%s", owner, f[1], didYouMean(f[1], members))
		}
	} else if k := noKey.FindStringSubmatch(cause); k != nil {
		if top, keys, ok := mapAt(v, action); ok {
			return fmt.Sprintf("%s: %s names %s%s", location, action, undeclared(top, k[1]), didYouMean(k[1], keys))
		}
	}
	return fmt.Sprintf("%s: %s: %s", location, action, cause)
}

// mapAt finds the map a root chain such as `.Parameters.key` looked its last
// name up in. A chain from a moved dot does not resolve from the root, and is
// not guessed at.
func mapAt(v view, action string) (string, []string, bool) {
	m := rootChain.FindStringSubmatch(action)
	if m == nil {
		return "", nil, false
	}
	idents := strings.Split(strings.TrimPrefix(m[1], "."), ".")
	cur := reflect.ValueOf(v)
	for _, id := range idents[:len(idents)-1] {
		switch cur.Kind() {
		case reflect.Struct:
			cur = cur.FieldByName(id)
		case reflect.Map:
			cur = cur.MapIndex(reflect.ValueOf(id))
		default:
			return "", nil, false
		}
		if !cur.IsValid() {
			return "", nil, false
		}
	}
	if cur.Kind() != reflect.Map {
		return "", nil, false
	}
	return idents[0], mapKeys(cur), true
}
