package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// runtimeWords are the vocabularies of concrete runtimes.
//
// `podman` and `quadlet` are here before either exists, deliberately. The
// failure this guards against is not the Compose leak already measured -- that
// one is inventoried and shrinking -- it is the second adapter arriving and
// teaching the layers above it a second private language, one `if kind ==
// "quadlet"` at a time. A rule that only names the incumbent would permit
// exactly that.
// Two of these are also ordinary English. `compose` is a verb, so a
// `composeMessage` helper would be reported, and there is no allowlist entry
// that would be honest for it -- the inventory is a list of leaks, and that is
// not one. The intended answer is to rename it: in this codebase `compose`
// means Docker Compose, and a reader meeting the word has to disambiguate it
// whichever sense the author meant. There are none today; if one arrives and
// renaming it is genuinely wrong, that is the point to add a fourth class
// rather than to weaken the match.
var runtimeWords = []string{"compose", "docker", "podman", "quadlet"}

// namesARuntime returns the runtime word a name contains, or the empty string.
//
// The word rather than a bool, because every report says which one it matched:
// "names \"quadlet\"" tells a reader what rule fired on what, and a bare "this
// is a leak" makes them go looking.
func namesARuntime(s string) string {
	l := strings.ToLower(s)
	for _, w := range runtimeWords {
		if strings.Contains(l, w) {
			return w
		}
	}
	return ""
}

// guarded reports whether a file is above the adapter boundary.
//
// `internal/adapters` is where a runtime's vocabulary belongs: an adapter that
// could not say "compose" would be an adapter that could not do its job. Every
// other package is a place where naming one runtime is a claim that there is
// only one.
// This checker is excluded for the reason the adapters are: naming runtimes is
// its subject. Excluded in the walk rather than allowlisted so the number stays
// a count of the product's leaks -- an inventory that counted its own inventory
// would report progress for renaming a constant in here.
func guarded(path string) bool {
	p := filepath.ToSlash(path)
	if strings.HasPrefix(p, "internal/adapters/") || strings.HasPrefix(p, "tools/runtimecheck/") {
		return false
	}
	return strings.HasPrefix(p, "internal/") ||
		strings.HasPrefix(p, "cmd/") ||
		strings.HasPrefix(p, "tools/")
}

// Finding is one place a runtime is named above the boundary.
type Finding struct {
	Rule   string // "vocabulary", "literal" or "branch"
	File   string
	Line   int
	Symbol string
	Word   string
}

func (f Finding) Where() string { return fmt.Sprintf("%s:%d", f.File, f.Line) }

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s names %q (%s)", f.Where(), f.Symbol, f.Word, f.Rule)
}

// Check walks root and returns every finding, allowlisted or not, so the caller
// can both enforce and report.
func Check(root string) ([]Finding, error) {
	var found []Finding
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			// testdata holds fixtures that are supposed to fail.
			if d.Name() == "testdata" || d.Name() == "vendor" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || !guarded(rel) {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("%s: %w", rel, perr)
		}
		slash := filepath.ToSlash(rel)

		// The file's own name, and the package's. A name checker that read
		// only declarations would pass `internal/lifecycle/ops/podman.go`
		// with every symbol in it neutrally named -- and a file named for a
		// runtime is a stronger statement about where the boundary sits
		// than any single declaration inside it.
		if w := namesARuntime(filepath.Base(rel)); w != "" {
			found = append(found, Finding{
				Rule: "vocabulary", File: slash, Line: 1,
				Symbol: "file " + filepath.Base(rel), Word: w,
			})
		}
		if w := namesARuntime(f.Name.Name); w != "" {
			found = append(found, Finding{
				Rule: "vocabulary", File: slash, Line: fset.Position(f.Name.Pos()).Line,
				Symbol: "package " + f.Name.Name, Word: w,
			})
		}

		found = append(found, inspect(fset, f, slash, strings.HasSuffix(rel, "_test.go"))...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].File != found[j].File {
			return found[i].File < found[j].File
		}
		return found[i].Line < found[j].Line
	})
	return found, nil
}

func inspect(fset *token.FileSet, f *ast.File, rel string, isTest bool) []Finding {
	var out []Finding
	add := func(rule string, pos token.Pos, symbol, word string) {
		out = append(out, Finding{
			Rule:   rule,
			File:   rel,
			Line:   fset.Position(pos).Line,
			Symbol: symbol,
			Word:   word,
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {

		// Rule 1: a declared name that says which runtime this is.
		case *ast.TypeSpec:
			if w := namesARuntime(x.Name.Name); w != "" {
				add("vocabulary", x.Name.Pos(), "type "+x.Name.Name, w)
			}
		case *ast.FuncDecl:
			if w := namesARuntime(x.Name.Name); w != "" {
				add("vocabulary", x.Name.Pos(), "func "+x.Name.Name, w)
			}
		case *ast.Field:
			for _, nm := range x.Names {
				if w := namesARuntime(nm.Name); w != "" {
					add("vocabulary", nm.Pos(), "field "+nm.Name, w)
				}
			}
		case *ast.ValueSpec:
			for _, nm := range x.Names {
				if w := namesARuntime(nm.Name); w != "" {
					add("vocabulary", nm.Pos(), "name "+nm.Name, w)
				}
			}
		case *ast.AssignStmt:
			// `composeFiles := ...` is the same claim as a declared var,
			// and is how the vocabulary would creep back in below the
			// level anybody reviews.
			if x.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				if w := namesARuntime(id.Name); w != "" {
					add("vocabulary", id.Pos(), "name "+id.Name, w)
				}
			}

		// Rule 2: a decision made on which runtime this is. RFC 0023
		// decision 7 -- the port may grow methods, it may not grow a
		// `switch kind`.
		case *ast.BinaryExpr:
			if x.Op != token.EQL && x.Op != token.NEQ {
				return true
			}
			for _, side := range []ast.Expr{x.X, x.Y} {
				if w, lit := runtimeLiteral(side); w != "" {
					add("branch", x.Pos(), "comparison against "+lit, w)
				}
			}
		case *ast.CaseClause:
			for _, e := range x.List {
				if w, lit := runtimeLiteral(e); w != "" {
					add("branch", e.Pos(), "case "+lit, w)
				}
			}

		// Rule 3: the runtime's name written down as a value.
		//
		// Rule 2 sees `kind == "compose"` and misses
		// `const defaultRuntime = "compose"` followed by `kind ==
		// defaultRuntime` -- the name is neutral, so rule 1 is silent, and
		// the comparison is against an identifier, so rule 2 is too.
		// Resolving constants would need type information and would still
		// miss one imported across packages; flagging the value itself
		// needs neither and cannot be routed around.
		//
		// Not in tests, and this is the one place the rules differ. A test
		// *name* saying "compose" establishes vocabulary, which is why rule
		// 1 covers tests; a test *value* saying "compose" is the fixture's
		// subject -- a manifest whose runtime is Compose has to say so, and
		// a rule flagging that would be a rule demanding tests describe
		// something they are not testing.
		case *ast.BasicLit:
			if isTest || x.Kind != token.STRING {
				return true
			}
			if w, lit := runtimeLiteral(x); w != "" {
				add("literal", x.Pos(), "literal "+lit, w)
			}
		}
		return true
	})
	return dedupe(out)
}

// dedupe drops a literal finding on a line another rule already covers.
//
// `kind == "compose"` is one problem that rules 2 and 3 both see, and
// `const Docker = "docker"` is one problem that rules 1 and 3 both see. One
// line is one place a decision was made, so it earns one inventory entry and
// one classification; reporting it twice would make the number measure the
// checker rather than the code.
//
// The literal is the one dropped because it is the least specific: "a runtime's
// name appears here" says less than "this branches on it" or "this is called
// that".
func dedupe(in []Finding) []Finding {
	covered := map[string]bool{}
	for _, f := range in {
		if f.Rule != "literal" {
			covered[fmt.Sprintf("%s:%d", f.File, f.Line)] = true
		}
	}
	out := in[:0]
	for _, f := range in {
		if f.Rule == "literal" && covered[fmt.Sprintf("%s:%d", f.File, f.Line)] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// runtimeLiteral reports whether an expression is a string literal that *is* a
// runtime's name, rather than prose that mentions one.
//
// The distinction is the whole of rule 2's precision. `"compose"` in a
// comparison is a decision about which runtime is running. A help string
// reading "deployed with Docker Compose on one Linux machine" is documentation,
// and it reaches this function only because Go spells string concatenation with
// the same node type as comparison -- which is a mistake this made once.
func runtimeLiteral(e ast.Expr) (word, literal string) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", ""
	}
	// Unquoted rather than trimmed: `"\x64ocker"` is the string `docker`,
	// and trimming the quotes off leaves the escape intact and the match
	// failing. A bypass nobody would write by accident, which is the kind a
	// checker has to close anyway -- the rule is worth what its weakest
	// spelling is worth.
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", ""
	}
	// Exactly the name, not a sentence containing it.
	for _, w := range runtimeWords {
		if strings.EqualFold(value, w) {
			return w, lit.Value
		}
	}
	return "", ""
}
