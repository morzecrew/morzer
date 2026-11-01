package gotemplate

import (
	"fmt"
	"reflect"
	"strings"
	tparse "text/template/parse"

	"github.com/morzecrew/morzer/internal/domain"
)

// CheckReferences refuses a template that names something no installation's
// render context has: a secret the schema does not declare, a parameter the
// manifest does not declare, or a field the context does not carry.
//
// It reads the parse tree and renders nothing, which is why it runs in plain
// `release verify` while rendering stays behind `--render-check` (RFC 0013
// decision 12). It invents no values, so it chooses no branch: every branch is
// read, including the ones a synthetic render would skip. What it gives up is a
// chain whose dot `range`, `with` or a `define` has moved, because only the
// value can say what the dot is then.
//
// The context it resolves names against is the synthetic one, used for its
// shape and its keys: the keys are the bundle's own declarations, and the shape
// is the view a real render executes against.
func CheckReferences(rel domain.Release, schema domain.SecretSchema, name string, raw []byte) error {
	tmpl, err := parse(name, raw)
	if err != nil {
		return err
	}

	w := refWalker{root: reflect.ValueOf(newView(SyntheticData(rel, schema)))}
	for _, t := range tmpl.Templates() {
		if t.Tree == nil || t.Root == nil {
			continue
		}
		w.tree = t.Tree
		// A define's dot, and its $, are whatever it was passed.
		top := t.Name() == name
		w.walk(t.Root, top, top)
	}
	if len(w.problems) > 0 {
		return domain.ValidationError(fmt.Errorf("%w: %s", domain.ErrTemplateReference, name),
			"%s", strings.Join(w.problems, "; "))
	}
	return nil
}

type refWalker struct {
	root     reflect.Value
	tree     *tparse.Tree
	problems []string
}

// walk reads one node. dotIsRoot says whether `.` is still the whole context,
// and rootKnown whether `$` is.
func (w *refWalker) walk(node tparse.Node, dotIsRoot, rootKnown bool) {
	switch n := node.(type) {
	case *tparse.ListNode:
		if n == nil {
			return
		}
		for _, child := range n.Nodes {
			w.walk(child, dotIsRoot, rootKnown)
		}
	case *tparse.ActionNode:
		w.pipe(n.Pipe, dotIsRoot, rootKnown)
	case *tparse.IfNode:
		w.branch(&n.BranchNode, dotIsRoot, dotIsRoot, rootKnown)
	case *tparse.RangeNode:
		w.branch(&n.BranchNode, dotIsRoot, false, rootKnown)
	case *tparse.WithNode:
		w.branch(&n.BranchNode, dotIsRoot, false, rootKnown)
	case *tparse.TemplateNode:
		w.pipe(n.Pipe, dotIsRoot, rootKnown)
	}
}

// branch reads a control structure. Its body sees inner as its dot; its else
// keeps the dot it was entered with.
func (w *refWalker) branch(b *tparse.BranchNode, dotIsRoot, inner, rootKnown bool) {
	w.pipe(b.Pipe, dotIsRoot, rootKnown)
	w.walk(b.List, inner, rootKnown)
	w.walk(b.ElseList, dotIsRoot, rootKnown)
}

func (w *refWalker) pipe(p *tparse.PipeNode, dotIsRoot, rootKnown bool) {
	if p == nil {
		return
	}
	for _, cmd := range p.Cmds {
		w.secretFileName(cmd)
		if chain, key, ok := indexed(cmd, dotIsRoot, rootKnown); ok {
			w.index(cmd, chain, key)
			continue
		}
		for _, arg := range cmd.Args {
			w.arg(arg, dotIsRoot, rootKnown)
		}
	}
}

// indexed recognises `index <chain> "key"` with the chain from the root
// context, bare or in parentheses, and the key a literal. Unlike a field, a key
// a map does not hold renders as nothing rather than failing, so it is the
// lookup most worth reading.
func indexed(cmd *tparse.CommandNode, dotIsRoot, rootKnown bool) ([]string, string, bool) {
	if len(cmd.Args) != 3 {
		return nil, "", false
	}
	fn, ok := cmd.Args[0].(*tparse.IdentifierNode)
	if !ok || fn.Ident != "index" {
		return nil, "", false
	}
	key, ok := cmd.Args[2].(*tparse.StringNode)
	if !ok {
		return nil, "", false
	}
	receiver := cmd.Args[1]
	if p, ok := receiver.(*tparse.PipeNode); ok && len(p.Decl) == 0 &&
		len(p.Cmds) == 1 && len(p.Cmds[0].Args) == 1 {
		receiver = p.Cmds[0].Args[0]
	}
	switch n := receiver.(type) {
	case *tparse.FieldNode:
		if dotIsRoot {
			return n.Ident, key.Text, true
		}
	case *tparse.VariableNode:
		if rootKnown && n.Ident[0] == "$" && len(n.Ident) > 1 {
			return n.Ident[1:], key.Text, true
		}
	}
	return nil, "", false
}

// index checks the key against what the chain reached: a map must hold it, and
// a struct cannot be indexed at all.
func (w *refWalker) index(node tparse.Node, chain []string, key string) {
	v, ok := w.resolve(node, chain)
	if !ok {
		return
	}
	path := "." + strings.Join(chain, ".")
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String &&
			!v.MapIndex(reflect.ValueOf(key).Convert(v.Type().Key())).IsValid() {
			w.report(node, fmt.Sprintf("index %s %q names %s", path, key, undeclared(chain[0], key)))
		}
	case reflect.Struct:
		w.report(node, fmt.Sprintf("index cannot look up %q in %s, which is not a map; write %s.%s", key, path, path, key))
	}
}

func (w *refWalker) arg(node tparse.Node, dotIsRoot, rootKnown bool) {
	switch n := node.(type) {
	case *tparse.FieldNode:
		if dotIsRoot {
			w.resolve(n, n.Ident)
		}
	case *tparse.VariableNode:
		if rootKnown && n.Ident[0] == "$" && len(n.Ident) > 1 {
			w.resolve(n, n.Ident[1:])
		}
	case *tparse.PipeNode:
		w.pipe(n, dotIsRoot, rootKnown)
	case *tparse.ChainNode:
		// (pipeline).Field: the pipeline is read, the field is not,
		// since its receiver is a value.
		w.arg(n.Node, dotIsRoot, rootKnown)
	}
}

// secretFileName checks the literal name in `secretFile .Secrets "name"`,
// wherever it appears: the name is a schema entry whatever the dot is.
func (w *refWalker) secretFileName(cmd *tparse.CommandNode) {
	if len(cmd.Args) < 3 {
		return
	}
	fn, ok := cmd.Args[0].(*tparse.IdentifierNode)
	if !ok || fn.Ident != "secretFile" {
		return
	}
	lit, ok := cmd.Args[2].(*tparse.StringNode)
	if !ok {
		return
	}
	secrets := w.root.FieldByName("Secrets")
	if !secrets.MapIndex(reflect.ValueOf(lit.Text)).IsValid() {
		w.report(cmd, fmt.Sprintf("secretFile names secret %q, which the secret schema does not declare", lit.Text))
	}
}

// resolve follows a chain of names from the root context and reports the first
// one that resolves to nothing. It returns what the chain reached, and false
// when it reported or cannot follow further.
func (w *refWalker) resolve(node tparse.Node, idents []string) (reflect.Value, bool) {
	v := w.root
	for i, id := range idents {
		path := "." + strings.Join(idents[:i+1], ".")
		parent := "." + strings.Join(idents[:i], ".")
		switch v.Kind() {
		case reflect.Struct:
			// A method is callable as a field; what it returns is
			// not followed.
			if _, ok := reflect.PointerTo(v.Type()).MethodByName(id); ok {
				return reflect.Value{}, false
			}
			field, ok := v.Type().FieldByName(id)
			if !ok || !field.IsExported() {
				w.report(node, fmt.Sprintf("%s: %s has no field %s", path, parentName(parent), id))
				return reflect.Value{}, false
			}
			v = v.FieldByIndex(field.Index)
		case reflect.Map:
			if v.Type().Key().Kind() != reflect.String {
				return reflect.Value{}, false
			}
			entry := v.MapIndex(reflect.ValueOf(id).Convert(v.Type().Key()))
			if !entry.IsValid() {
				w.report(node, fmt.Sprintf("%s names %s", path, undeclared(idents[0], id)))
				return reflect.Value{}, false
			}
			v = entry
		default:
			w.report(node, fmt.Sprintf("%s: %s is a %s, which has no fields", path, parentName(parent), v.Kind()))
			return reflect.Value{}, false
		}
	}
	return v, true
}

// parentName is how a message names the receiver: by the path a template
// author wrote, never by the Go type behind it.
func parentName(path string) string {
	if path == "." {
		return "the render context"
	}
	return path
}

func undeclared(top, key string) string {
	switch top {
	case "Secrets":
		return fmt.Sprintf("secret %q, which the secret schema does not declare", key)
	case "Parameters":
		return fmt.Sprintf("parameter %q, which the manifest does not declare", key)
	default:
		return fmt.Sprintf("%q, which the render context does not have", key)
	}
}

func (w *refWalker) report(node tparse.Node, msg string) {
	location, _ := w.tree.ErrorContext(node)
	w.problems = append(w.problems, location+": "+msg)
}
