// Command covmerge unions Go coverage profiles.
//
// Coverage is measured twice by different mechanisms. `go test` writes an
// `atomic` profile for the packages its suites exercise; the acceptance run
// drives the built binary, which writes `GOCOVERDIR` counter files that
// `go tool covdata textfmt` turns into a `set` profile. Neither alone describes
// what this project tests: measured on one tree, `go test` reports 59.5% and
// the acceptance run 47.6%, while their union is 70.0%.
//
// They cannot simply be concatenated. The modes differ, `go tool cover` refuses
// a profile with two mode lines, and the same block appears in both files with
// counts that mean different things. So blocks are unioned: a block executed by
// any input is covered in the output, and the result is written as `set`,
// because "how many times" stops being meaningful once two counting schemes are
// combined.
//
// Deliberately not `go tool covdata merge`: that works on counter directories,
// and the `go test` side is only ever a text profile.
package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: covmerge <out> <profile> [profile ...]")
		os.Exit(2)
	}
	out, inputs := os.Args[1], os.Args[2:]

	blocks := map[string]block{}
	for _, path := range inputs {
		// A missing input is not an error. CI merges whatever ran, and a
		// pull request that skipped the acceptance job still has to be
		// able to measure itself.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "covmerge: %s does not exist, skipping\n", path)
			continue
		}
		if err := read(path, blocks); err != nil {
			fmt.Fprintf(os.Stderr, "covmerge: %v\n", err)
			os.Exit(1)
		}
	}
	if len(blocks) == 0 {
		fmt.Fprintln(os.Stderr, "covmerge: no coverage data in any input")
		os.Exit(1)
	}

	if err := write(out, blocks); err != nil {
		fmt.Fprintf(os.Stderr, "covmerge: %v\n", err)
		os.Exit(1)
	}

	covered, total := 0, 0
	for _, b := range blocks {
		total += b.statements
		if b.covered {
			covered += b.statements
		}
	}
	fmt.Printf("covmerge: %d/%d statements covered (%.1f%%) from %d input(s)\n",
		covered, total, 100*float64(covered)/float64(total), len(inputs))
}

type block struct {
	statements int
	covered    bool
}

// read folds one profile into the accumulator.
func read(path string, into map[string]block) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	first := true
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
			return fmt.Errorf("%s: first line is not a mode declaration", path)
		}

		// "file.go:1.2,3.4 numStatements count"
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return fmt.Errorf("%s: cannot parse %q", path, line)
		}
		statements, err := strconv.Atoi(fields[1])
		if err != nil {
			return fmt.Errorf("%s: bad statement count in %q", path, line)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return fmt.Errorf("%s: bad execution count in %q", path, line)
		}

		b := into[fields[0]]
		b.statements = statements
		b.covered = b.covered || count > 0
		into[fields[0]] = b
	}
	return s.Err()
}

func write(path string, blocks map[string]block) error {
	keys := make([]string, 0, len(blocks))
	for k := range blocks {
		keys = append(keys, k)
	}
	// Sorted so the output is byte-identical between runs, which is what
	// makes a diff of two profiles readable.
	sort.Strings(keys)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "mode: set")
	for _, k := range keys {
		b := blocks[k]
		hit := 0
		if b.covered {
			hit = 1
		}
		fmt.Fprintf(w, "%s %d %d\n", k, b.statements, hit)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}
