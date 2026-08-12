// Command runtimecheck enforces the runtime boundary that depguard cannot see.
//
// RFC 0023 §2 measured that the layering rules are import rules: `.golangci.yml`
// stops `internal/lifecycle` importing `internal/adapters`, and that is a real
// guarantee about dependencies. It says nothing about *vocabulary*, and the leak
// RFC 0023 is looking for is vocabulary — a ports file whose exported API is the
// Compose interpolation contract imports nothing at all.
//
// Two rules:
//
//   - vocabulary — a declared name above `internal/adapters` that says which
//     runtime this is. Allowlisted by the inventory in inventory.go, which is
//     RFC 0023 P1's deliverable: every existing leak, classified, with the thing
//     that would remove it.
//
//   - branch — a comparison or case against a runtime's name. RFC 0023
//     decision 7: the port may grow methods, it may not grow a `switch kind`.
//     There is no allowlist for this one and there are no findings today, which
//     is the state it exists to preserve.
//
// Run with -list to print the inventory rather than check it.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	var (
		root = flag.String("root", ".", "repository root to walk")
		list = flag.Bool("list", false, "print the inventory instead of checking")
	)
	flag.Parse()

	if *list {
		printInventory(os.Stdout)
		return
	}

	if err := run(*root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, out *os.File) error {
	found, err := Check(root)
	if err != nil {
		return err
	}

	unexpected, stale := reconcile(found)

	for _, f := range unexpected {
		fmt.Fprintf(out, "%s\n", f)
	}
	for _, key := range stale {
		fmt.Fprintf(out, "inventory: %s is listed and no longer exists\n", key)
	}

	if len(unexpected) > 0 || len(stale) > 0 {
		return fmt.Errorf("runtimecheck: %d unexpected, %d stale inventory entries\n%s",
			len(unexpected), len(stale), guidance(unexpected, stale))
	}

	byClass := counts()
	parts := make([]string, 0, len(byClass))
	for _, c := range byClass {
		parts = append(parts, fmt.Sprintf("%d %s", c.N, c.Class))
	}
	fmt.Fprintf(out, "runtimecheck: %d known mentions above internal/adapters (%s), 0 runtime branches\n",
		len(inventory), strings.Join(parts, ", "))
	return nil
}

// reconcile compares what the tree has against what the inventory claims.
//
// Both directions, because an allowlist checked in one direction only ever
// grows: the day somebody deletes `ComposeFilePaths`, the entry describing it
// should fail rather than sit there making the number look worse than it is.
func reconcile(found []Finding) (unexpected []Finding, stale []string) {
	known := allowed()
	seen := map[string]bool{}

	for _, f := range found {
		key := f.File + "\x00" + f.Symbol
		if f.Rule == "branch" {
			// No allowlist. A branch on runtime kind is the thing
			// decision 7 forbids outright.
			unexpected = append(unexpected, f)
			continue
		}
		if _, ok := known[key]; ok {
			seen[key] = true
			continue
		}
		unexpected = append(unexpected, f)
	}

	for key := range known {
		if !seen[key] {
			stale = append(stale, strings.ReplaceAll(key, "\x00", ": "))
		}
	}
	sort.Strings(stale)
	return unexpected, stale
}

func guidance(unexpected []Finding, stale []string) string {
	var b strings.Builder
	if len(unexpected) > 0 {
		b.WriteString("\nA name above internal/adapters that says which runtime is running is\n" +
			"RFC 0023's leak. Either move it into the adapter, or -- if the concept\n" +
			"really is runtime-neutral and only the name is borrowed -- rename it.\n" +
			"If it is neither, add it to tools/runtimecheck/inventory.go with the\n" +
			"classification and what would remove it. A branch on a runtime's name\n" +
			"has no third option: decision 7 forbids it.\n")
	}
	if len(stale) > 0 {
		b.WriteString("\nA stale entry means a leak was fixed and the inventory was not\n" +
			"updated. Delete the entry -- the number is meant to fall.\n")
	}
	return b.String()
}

func printInventory(out *os.File) {
	fmt.Fprintf(out, "# The runtime leak inventory (RFC 0023 P1)\n\n")
	for _, c := range counts() {
		fmt.Fprintf(out, "%-16s %d\n", string(c.Class), c.N)
	}
	fmt.Fprintf(out, "%-16s %d\n\n", "total", len(inventory))

	fmt.Fprintf(out, "| Where | Symbol | Class | Why | What removes it |\n")
	fmt.Fprintf(out, "| --- | --- | --- | --- | --- |\n")
	for _, e := range sortedInventory() {
		removes := e.Removes
		if removes == "" {
			removes = "nothing — and nothing should"
		}
		fmt.Fprintf(out, "| `%s` | `%s` | %s | %s | %s |\n",
			e.File, e.Symbol, e.Class, e.Why, removes)
	}
}
