package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCommandPrintsAReportItself is RFC 0019's boundary, enforced.
//
// The rule: a command decides *what* to say and hands the value to
// `app.render`; `internal/ui` decides how it looks. Before it existed this
// package printed directly 59 times against 5 renderer dispatches, so `--plain`
// and rich mode produced identical bytes for almost everything and the mode
// contract was kept by 8% of the output.
//
// What makes that state come back is not malice, it is that
// `fmt.Fprintf(app.Stream.Out, …)` compiles. This test is the thing that does
// not compile.
//
// Stderr is exempt and deliberately so: narration is not the result. A step
// line, a warning, a hint and the summary all belong on stderr, are already
// rendered by the presenter or by `App.notice`, and holding them to the report
// boundary would mean an envelope around a progress message.
func TestNoCommandPrintsAReportItself(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if why := writesToStdout(call); why != "" {
				t.Errorf("%s writes a report straight to stdout (%s).\n"+
					"Reports go through app.render, which is what makes --plain, "+
					"rich and --json one decision instead of fifteen.",
					fset.Position(call.Pos()), why)
			}
			return true
		})
	}
}

// stdoutStreams are the ways a command can name the stream a report goes to.
//
// Both spellings, because the receiver is `app` in the command constructors and
// `a` on App's own methods, and a rule that knew only one of them would be half
// a rule.
var stdoutStreams = map[string]bool{
	"app.Stream.Out": true,
	"a.Stream.Out":   true,
}

// writesToStdout names the way this call reaches stdout, or "" when it does not.
//
// Four ways, because a rule with one is a rule with a workaround. Writing to
// `app.Stream.Out` through `fmt.Fprint*` is the one that existed; `fmt.Print*`
// reaches the process's own stdout past the injected streams entirely, which is
// worse; `io.WriteString` and a direct `.Write` are the same act spelled
// differently; and cobra's `cmd.Print*` writes to a stream this program never
// configured.
//
// Stderr stays exempt throughout: narration is not the result.
func writesToStdout(call *ast.CallExpr) string {
	fn, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	receiver := render(fn.X)

	switch {
	// fmt.Fprint*(app.Stream.Out, …)
	case receiver == "fmt" && strings.HasPrefix(fn.Sel.Name, "Fprint"):
		if len(call.Args) > 0 && stdoutStreams[render(call.Args[0])] {
			return "fmt." + fn.Sel.Name
		}

	// fmt.Print* — the process's own stdout, past the injected streams.
	case receiver == "fmt" && strings.HasPrefix(fn.Sel.Name, "Print"):
		return "fmt." + fn.Sel.Name + " ignores the injected streams entirely"

	// io.WriteString(app.Stream.Out, …)
	case receiver == "io" && fn.Sel.Name == "WriteString":
		if len(call.Args) > 0 && stdoutStreams[render(call.Args[0])] {
			return "io.WriteString"
		}

	// app.Stream.Out.Write(…) / .WriteString(…)
	case stdoutStreams[receiver] && strings.HasPrefix(fn.Sel.Name, "Write"):
		return receiver + "." + fn.Sel.Name

	// cmd.Print* — cobra's own stream, which nothing here configures.
	case receiver == "cmd" && strings.HasPrefix(fn.Sel.Name, "Print"):
		return "cmd." + fn.Sel.Name + " writes to a stream this program does not configure"
	}
	return ""
}

// render spells an expression back out, for comparing against the names above.
//
// Only the shapes that can name a stream: an identifier and a chain of field
// selections. Anything else is not `app.Stream.Out` and does not need to be
// rendered to prove it.
func render(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return render(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}
