// Command docscheck fails the build when the documentation has drifted from the
// code it describes.
//
// A `pages/` tree is easier to leave stale than a README someone edits while
// working, and nothing else notices: a new command ships, its page is written a
// release or three later, or never. This is the gate that makes that a build
// failure rather than a discovery.
//
// Four properties, none of them a judgement about prose:
//
//  1. Link integrity. Every relative link between pages resolves, and every link
//     into the repository resolves.
//
//  2. Nav completeness, both directions. Every page appears in the nav, and
//     every nav entry names a page that exists. A page absent from the nav is a
//     page nobody finds.
//
//  3. Contract coverage. Every domain error code, every exit code, every field
//     of the manifest and secret schemas, and every hook ABI environment
//     variable is documented. These are the surfaces this project publishes as
//     stable; one that drifts from its documentation silently is worse than one
//     that was never documented.
//
//  4. Command coverage. Every command and every non-hidden flag, read out of the
//     cobra tree rather than out of a list someone maintains.
//
// The contracts are read from the source -- consts by AST, schema fields by
// reflection, the CLI surface by walking the real command tree -- so this cannot
// drift the way a hand-maintained checklist would.
//
// Mentions are counted only in a page's own prose: fenced code blocks are
// stripped before matching, which also excludes the `--8<--` snippets that pull
// files in from `testdata/`. An example that happens to contain a new field is
// not documentation of it.
//
// Usage, from anywhere in the repository:
//
//	go run ./tools/docscheck
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/morzecrew/morzer/internal/cli"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

const (
	docsDir      = "pages/docs"
	navConfig    = "pages/zensical.toml"
	manifestPage = "reference/manifest.md"
	exitCodePage = "reference/exit-codes.md"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	pages, err := loadPages(filepath.Join(root, docsDir))
	if err != nil {
		fail(err)
	}
	if len(pages) == 0 {
		fail(fmt.Errorf("no pages found under %s", docsDir))
	}

	var rep report
	checkLinks(&rep, root, pages)
	checkNav(&rep, root, pages)
	checkContracts(&rep, root, pages)
	checkCommands(&rep, pages)

	if rep.failed() {
		rep.print(os.Stderr)
		os.Exit(1)
	}
	fmt.Printf("docs-check: %d pages, %d checks, no drift\n", len(pages), rep.checks)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "docs-check: %v\n", err)
	os.Exit(1)
}

// ----------------------------------------------------------------------------
// Reporting

// report accumulates every problem so one run surfaces all of them. A checker
// that stopped at the first would turn a documentation sweep into a sequence of
// build failures.
type report struct {
	checks   int
	problems map[string][]string
}

func (r *report) add(group, format string, args ...any) {
	if r.problems == nil {
		r.problems = map[string][]string{}
	}
	r.problems[group] = append(r.problems[group], fmt.Sprintf(format, args...))
}

func (r *report) failed() bool { return len(r.problems) > 0 }

func (r *report) print(w *os.File) {
	groups := make([]string, 0, len(r.problems))
	for g := range r.problems {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	total := 0
	for _, g := range groups {
		items := r.problems[g]
		sort.Strings(items)
		total += len(items)
		fmt.Fprintf(w, "\n%s\n", g)
		for _, it := range items {
			fmt.Fprintf(w, "  - %s\n", it)
		}
	}
	fmt.Fprintf(w, "\ndocs-check: %d problems\n", total)
}

// ----------------------------------------------------------------------------
// Pages

// page is one markdown file, with its prose separated from its code.
type page struct {
	// Rel is the path relative to the docs root, e.g. "reference/hooks.md".
	Rel string
	// Abs is the path on disk.
	Abs string
	// Prose is the file with fenced code blocks removed. Every coverage
	// check matches against this, never against the raw file.
	Prose string
	// Code holds the inline code spans found in Prose. Coverage is asserted
	// against these rather than against free text: requiring a page to name
	// `rollback_safe` as an identifier is a meaningfully higher bar than
	// requiring the letters to appear somewhere.
	Code map[string]bool
}

var (
	fenceRe  = regexp.MustCompile("(?ms)^ {0,3}(```|~~~).*?^ {0,3}(```|~~~) *$")
	inlineRe = regexp.MustCompile("`([^`\n]+)`")
	linkRe   = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
)

// loadPages reads every markdown file under the docs root.
//
// The walk is confined by os.Root: a symlink out of `pages/docs` is not
// something this tool should follow, and the same containment the manager
// enforces on release bundles costs nothing here.
func loadPages(dir string) ([]page, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	var pages []page
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(rel, ".md") {
			return nil
		}
		data, err := fs.ReadFile(root.FS(), rel)
		if err != nil {
			return err
		}
		prose := fenceRe.ReplaceAllString(string(data), "")
		code := map[string]bool{}
		for _, m := range inlineRe.FindAllStringSubmatch(prose, -1) {
			code[m[1]] = true
		}
		pages = append(pages, page{
			Rel:   rel,
			Abs:   filepath.Join(dir, filepath.FromSlash(rel)),
			Prose: prose,
			Code:  code,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Rel < pages[j].Rel })
	return pages, nil
}

// mentioned reports whether any page names sym as an identifier.
func mentioned(pages []page, sym string) bool {
	for _, p := range pages {
		if p.Code[sym] {
			return true
		}
	}
	return false
}

// mentionedIn is mentioned, restricted to one page. Used where a contract has
// an obvious home and being named anywhere else would not help a reader
// looking for it.
func mentionedIn(pages []page, rel, sym string) bool {
	for _, p := range pages {
		if p.Rel == rel {
			return p.Code[sym]
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// 1. Links

func checkLinks(rep *report, root string, pages []page) {
	docsRoot := filepath.Join(root, docsDir)
	for _, p := range pages {
		rep.checks++
		for _, m := range linkRe.FindAllStringSubmatch(p.Prose, -1) {
			target := m[1]
			if isExternal(target) {
				continue
			}
			// Drop the fragment: anchor validity is a separate
			// problem, and the generator's own build reports it.
			if i := strings.IndexByte(target, '#'); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			resolved := filepath.Join(filepath.Dir(p.Abs), filepath.FromSlash(target))
			if _, err := os.Stat(resolved); err != nil {
				where, _ := filepath.Rel(docsRoot, resolved)
				rep.add("broken links", "%s → %s (resolves to %s)", p.Rel, m[1], where)
			}
		}
	}
}

func isExternal(target string) bool {
	for _, prefix := range []string{"http://", "https://", "mailto:", "//", "/", "#"} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// 2. Nav

func checkNav(rep *report, root string, pages []page) {
	rep.checks++

	navPath := filepath.Join(root, navConfig)
	data, err := os.ReadFile(navPath)
	if err != nil {
		rep.add("nav", "cannot read %s: %v", navConfig, err)
		return
	}
	entries, err := navEntries(string(data))
	if err != nil {
		rep.add("nav", "%v", err)
		return
	}
	if len(entries) == 0 {
		rep.add("nav", "no entries found in %s", navConfig)
		return
	}

	inNav := map[string]bool{}
	for _, e := range entries {
		inNav[e] = true
		if _, err := os.Stat(filepath.Join(root, docsDir, filepath.FromSlash(e))); err != nil {
			rep.add("nav", "entry %q names a page that does not exist", e)
		}
	}
	for _, p := range pages {
		if inNav[p.Rel] || isPartial(p.Rel) {
			continue
		}
		rep.add("nav", "%s is not in the nav, so nobody will find it", p.Rel)
	}
}

// isPartial reports whether a file is an include rather than a page. Anything
// under a directory starting with `_` is the generator's convention for assets
// and partials, and is legitimately absent from the nav.
func isPartial(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, "_") {
			return true
		}
	}
	return false
}

var navPageRe = regexp.MustCompile(`"([^"]+\.md)"`)

// navEntries extracts the page paths from the config's `nav` array.
//
// It scans the bracketed region rather than parsing TOML: a TOML decoder would
// be a module dependency for one array in one file this repository owns, and the
// nav is the only place `.md` strings appear inside brackets. Bracket depth is
// tracked so the scan cannot run past the array's end and pick up something
// else.
func navEntries(config string) ([]string, error) {
	start := strings.Index(config, "\nnav = [")
	if start < 0 {
		return nil, fmt.Errorf("no `nav = [` array in %s", navConfig)
	}
	body := config[start+len("\nnav = "):]

	depth, end := 0, -1
	for i, r := range body {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("the `nav` array in %s is not closed", navConfig)
	}

	var out []string
	for _, m := range navPageRe.FindAllStringSubmatch(body[:end], -1) {
		out = append(out, m[1])
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// 3. Contracts

func checkContracts(rep *report, root string, pages []page) {
	checkErrorCodes(rep, root, pages)
	checkExitCodes(rep, root, pages)
	checkSchemaFields(rep, pages)
	checkHookEnv(rep, pages)
	checkComposeVars(rep, pages)
	checkTemplateFields(rep, pages)
}

// checkErrorCodes asserts every domain.Code value is documented. They are part
// of the public contract -- operator scripts and monitoring match on them -- so
// one that exists only in the source is one nobody can rely on.
func checkErrorCodes(rep *report, root string, pages []page) {
	rep.checks++
	codes, _, err := domainConsts(root)
	if err != nil {
		rep.add("error codes", "%v", err)
		return
	}
	for _, c := range codes {
		if !mentioned(pages, c) {
			rep.add("error codes", "`%s` is not named by any page", c)
		}
	}
}

// checkExitCodes asserts the exit-code table has a row per constant.
//
// Exit codes are matched by their number in the table rather than by their Go
// identifier, because the documentation is for people reading `echo $?` and
// never writes `ExitPreflight`.
func checkExitCodes(rep *report, root string, pages []page) {
	rep.checks++
	_, exits, err := domainConsts(root)
	if err != nil {
		rep.add("exit codes", "%v", err)
		return
	}

	var table string
	for _, p := range pages {
		if p.Rel == exitCodePage {
			table = p.Prose
		}
	}
	if table == "" {
		rep.add("exit codes", "%s is missing", exitCodePage)
		return
	}

	for name, value := range exits {
		row := regexp.MustCompile(`(?m)^\|\s*` + strconv.Itoa(value) + `\s*\|`)
		if !row.MatchString(table) {
			rep.add("exit codes", "%s (%d) has no row in %s", name, value, exitCodePage)
		}
	}
}

// domainConsts reads the error codes and exit codes out of the domain package's
// source. They are untyped or string constants, so reflection cannot enumerate
// them and a hand-maintained list here would be the very drift this checks for.
func domainConsts(root string) (codes []string, exits map[string]int, err error) {
	dir := filepath.Join(root, "internal", "domain")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read internal/domain: %w", err)
	}

	fset := token.NewFileSet()
	exits = map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			return nil, nil, fmt.Errorf("cannot parse %s: %w", name, perr)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok {
					continue
				}

				if ident, ok := vs.Type.(*ast.Ident); ok && ident.Name == "Code" && lit.Kind == token.STRING {
					value, uerr := strconv.Unquote(lit.Value)
					if uerr == nil {
						codes = append(codes, value)
					}
					continue
				}
				if strings.HasPrefix(vs.Names[0].Name, "Exit") && lit.Kind == token.INT {
					value, cerr := strconv.Atoi(lit.Value)
					if cerr == nil {
						exits[vs.Names[0].Name] = value
					}
				}
			}
		}
	}
	if len(codes) == 0 || len(exits) == 0 {
		return nil, nil, fmt.Errorf("found %d error codes and %d exit codes in internal/domain; "+
			"the checker's assumptions about how they are declared have gone stale",
			len(codes), len(exits))
	}
	sort.Strings(codes)
	return codes, exits, nil
}

// checkSchemaFields asserts the manifest reference documents every field a
// bundle author can write.
//
// The field set comes from the structs the loader decodes into, so adding one
// fails this until the reference describes it. Scoped to the manifest page
// rather than to the whole site: a field named in passing on an unrelated page
// is not somewhere a vendor would look for it.
func checkSchemaFields(rep *report, pages []page) {
	rep.checks++
	fields := map[string]string{}
	collectYAMLFields(reflect.TypeOf(domain.Manifest{}), "manifest", fields)
	collectYAMLFields(reflect.TypeOf(domain.SecretSchema{}), "secret schema", fields)

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !mentionedIn(pages, manifestPage, name) {
			rep.add("schema fields", "%s field `%s` is not named in %s", fields[name], name, manifestPage)
		}
	}
}

// collectYAMLFields walks a struct graph gathering yaml tag names.
//
// Types without yaml-tagged fields -- the scalar wrappers, and semver's own
// internals -- contribute nothing and terminate the walk naturally, so no type
// needs listing here by name.
func collectYAMLFields(t reflect.Type, origin string, out map[string]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		if _, seen := out[tag]; !seen {
			out[tag] = origin
		}
		collectYAMLFields(f.Type, origin, out)
	}
}

// checkHookEnv asserts every variable the hook ABI defines is documented.
//
// The names come from the function that builds them, called with a populated
// environment, so a variable added there without a doc entry fails here. Pages
// write them with a placeholder prefix -- `<PRODUCT>_DATA_DIR` -- because the
// real prefix is the product's own name, so the suffix is what is matched.
func checkHookEnv(rep *report, pages []page) {
	rep.checks++

	version, _ := domain.ParseVersion("1.0.0")
	vars := ports.HookEnvVars(ports.HookEnv{
		Product:         "product",
		InstallationID:  "id",
		OperationID:     "op",
		OperationType:   domain.OpTypeApply,
		Phase:           ports.PhaseMigrate,
		ReleaseVersion:  version,
		ReleaseDir:      "dir",
		PreviousVersion: version,
		DataDir:         "data",
		BackupDir:       "backup",
		SecretsDir:      "secrets",
		ConfigFile:      "config",
		ComposeProject:  "project",
		LogLevel:        "info",
	})

	keys := make([]string, 0, len(vars))
	for full := range vars {
		keys = append(keys, strings.TrimPrefix(full, "PRODUCT_"))
	}
	sort.Strings(keys)

	for _, key := range keys {
		if !namedWithPrefix(pages, key) {
			rep.add("hook ABI", "environment variable `%s` is not documented", key)
		}
	}
}

// checkComposeVars asserts every variable a Compose file may interpolate is
// documented.
//
// The second of the three ABIs a bundle author writes against, and the one that
// went undocumented longest: `<PRODUCT>_VERSION`, `_PROFILE` and `_DOMAIN`
// appeared in no page at all while the hook ABI beside them was gated. A vendor
// had no complete list of what they could interpolate, and adding a variable
// could not fail the build.
//
// The names come from ports.ComposeVars, which a contract test holds to what
// the builder actually emits.
func checkComposeVars(rep *report, pages []page) {
	rep.checks++

	for _, suffix := range ports.ComposeVars {
		if !namedWithPrefix(pages, suffix) {
			rep.add("compose ABI", "interpolation variable `%s` is not documented", suffix)
		}
	}

	// The two manifest-driven families are documented as patterns, since
	// their tails are the vendor's own names. Matched as an infix -- a page
	// writes `<PRODUCT>_IMAGE_<NAME>`, so neither end is a fixed string.
	for _, pattern := range ports.ComposeVarPatterns {
		family, _, _ := strings.Cut(pattern, "<") // "IMAGE_"
		if !namesFamily(pages, family) {
			rep.add("compose ABI",
				"the `%s` variable family is not documented", pattern)
		}
	}
}

// namesFamily reports whether some page writes a variable in the given family,
// under any prefix and with any tail.
func namesFamily(pages []page, family string) bool {
	for _, p := range pages {
		for span := range p.Code {
			if strings.Contains(span, "_"+family) {
				return true
			}
		}
	}
	return false
}

// checkTemplateFields asserts every top-level name a configuration template may
// use is documented.
//
// The third ABI. A vendor writes `{{ .Paths.Data }}` against it and a rename
// breaks every bundle in the field, yet it appeared in no page until this
// check existed.
func checkTemplateFields(rep *report, pages []page) {
	rep.checks++

	for _, field := range ports.TemplateFields() {
		if !namesTemplateField(pages, field) {
			rep.add("template context",
				"render context field `.%s` is not documented", field)
		}
	}
}

// namesTemplateField looks for the field written as a template reference, so a
// page that merely uses the word "Release" in prose does not count as
// documenting `.Release`.
func namesTemplateField(pages []page, field string) bool {
	for _, p := range pages {
		for span := range p.Code {
			if span == "."+field || strings.HasPrefix(span, "."+field+".") {
				return true
			}
		}
	}
	return false
}

// namedWithPrefix reports whether some page names key, allowing a placeholder
// product prefix in front of it.
func namedWithPrefix(pages []page, key string) bool {
	for _, p := range pages {
		for span := range p.Code {
			if span == key || strings.HasSuffix(span, "_"+key) {
				return true
			}
		}
	}
	return false
}

// ----------------------------------------------------------------------------
// 4. Commands

func checkCommands(rep *report, pages []page) {
	rep.checks++

	root := cli.CommandTree()
	prose := allProse(pages)

	// Persistent flags are checked once, against the root, rather than
	// re-reported for every command that inherits them.
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		checkFlag(rep, pages, "morzer", f)
	})

	var walk func(cmd *cobra.Command, path []string)
	walk = func(cmd *cobra.Command, path []string) {
		for _, sub := range cmd.Commands() {
			name := strings.Fields(sub.Use)[0]
			// cobra generates `help` and `completion`; neither is
			// this project's surface to document.
			if name == "help" || name == "completion" || sub.Hidden {
				continue
			}
			full := append(append([]string(nil), path...), name)
			joined := strings.Join(full, " ")
			if !strings.Contains(prose, joined) {
				rep.add("commands", "`morzer %s` is not mentioned by any page", joined)
			}
			sub.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
				checkFlag(rep, pages, joined, f)
			})
			walk(sub, full)
		}
	}
	walk(root, nil)
}

// checkFlag asserts a flag is named as an identifier by some page. Hidden flags
// are exempt: `--root` exists for the test suite and is deliberately absent from
// `--help`, so documenting it would advertise something operators should not use.
func checkFlag(rep *report, pages []page, owner string, f *pflag.Flag) {
	if f.Hidden || f.Name == "help" {
		return
	}
	if mentioned(pages, "--"+f.Name) {
		return
	}
	rep.add("commands", "`--%s` (on `%s`) is not named by any page", f.Name, owner)
}

func allProse(pages []page) string {
	var b strings.Builder
	for _, p := range pages {
		b.WriteString(p.Prose)
		b.WriteByte('\n')
	}
	return b.String()
}

// ----------------------------------------------------------------------------
// Repository root

// repoRoot walks up from the working directory to the module root, so the
// checker behaves the same whether it is run by `just` from the top or by hand
// from a subdirectory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s; run this from inside the repository", dir)
		}
		dir = parent
	}
}
