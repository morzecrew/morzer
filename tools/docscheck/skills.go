package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/cli"
)

// ----------------------------------------------------------------------------
// 6. Agent skills
//
// The skills under skills/ are installed into somebody else's project and read
// by an agent in place of these pages, where no link checker follows them. Two
// things in one go stale without a page changing: a command or flag it tells the
// agent to run, and a link into the published documentation. Both are checked
// against what the pages are checked against -- the command tree, and the page
// files the site is built from, down to the heading a link names.

const (
	skillsDir = "skills"

	// docsSite is the prefix a skill links the documentation under. `latest`
	// rather than a version, so a link does not need moving at every release.
	docsSite = "https://morzecrew.github.io/morzer/latest/"
)

var (
	siteLinkRe  = regexp.MustCompile(regexp.QuoteMeta(docsSite) + `([^)\s#"]*)(?:#([^)\s"]*))?`)
	fenceBodyRe = regexp.MustCompile("(?ms)^ {0,3}(?:```|~~~)[^\n]*\n(.*?)^ {0,3}(?:```|~~~) *$")
	nameLineRe  = regexp.MustCompile(`(?m)^name:\s*(\S+)\s*$`)
	frontRe     = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
)

// checkSkills runs every skill check over each directory under skills/: one
// with no SKILL.md is not a skill the installer can load, and the rest have
// each markdown file's commands and links resolved.
func checkSkills(rep *report, root string) {
	dirs, err := os.ReadDir(filepath.Join(root, skillsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		rep.add("skills", "cannot read %s: %v", skillsDir, err)
		return
	}
	tree := cli.CommandTree()
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, skillsDir, d.Name(), "SKILL.md")); err != nil {
			rep.add("skills", "%s/%s has no SKILL.md, so it is not a skill the installer can load", skillsDir, d.Name())
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, skillsDir, d.Name()), func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(path, ".md") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rep.checks++
			rel, _ := filepath.Rel(root, path)
			if filepath.Base(path) == "SKILL.md" {
				checkSkillName(rep, rel, d.Name(), string(raw))
			}
			checkSiteLinks(rep, "skills", root, rel, string(raw))
			checkSkillCommands(rep, tree, rel, string(raw))
			return nil
		})
		if err != nil {
			rep.add("skills", "cannot read %s: %v", d.Name(), err)
		}
	}
}

// checkSkillName holds the frontmatter's name to the directory's. The installer
// takes the skill's name from the frontmatter and the reader from the path, and
// two names for one skill is a skill nobody can ask for by name.
func checkSkillName(rep *report, rel, dir, raw string) {
	var m []string
	if front := frontRe.FindStringSubmatch(raw); front != nil {
		m = nameLineRe.FindStringSubmatch(front[1])
	}
	if m == nil || m[1] != dir {
		rep.add("skills", "%s: frontmatter `name` must be %q, the directory's name", rel, dir)
	}
}

// checkSiteLinks resolves every link into the published documentation to the
// page it is built from, and its fragment to a heading on that page. Group names
// where the link was found: a skill, or a command's help.
func checkSiteLinks(rep *report, group, root, rel, raw string) {
	for _, m := range siteLinkRe.FindAllStringSubmatch(raw, -1) {
		page, ok := sitePage(root, m[1])
		if !ok {
			rep.add(group, "%s links %s%s, which no page under %s builds", rel, docsSite, m[1], docsDir)
			continue
		}
		if m[2] == "" {
			continue
		}
		body, err := os.ReadFile(page)
		if err != nil || !anchorsIn(fenceRe.ReplaceAllString(string(body), ""))[m[2]] {
			rep.add(group, "%s links %s%s#%s, and that page has no such heading", rel, docsSite, m[1], m[2])
		}
	}
}

// checkHelpLinks resolves the documentation links in every command's help, so
// a page that moves fails the build instead of a link printed by the binary.
func checkHelpLinks(rep *report, root string) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		rep.checks++
		checkSiteLinks(rep, "help", root, "`"+cmd.CommandPath()+"` help", cmd.Long)
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(cli.CommandTree())
}

// sitePage maps a path under the site to the page file it is built from:
// `authoring/hooks/` is `authoring/hooks.md`, and a directory is its index.
func sitePage(root, path string) (string, bool) {
	path = strings.Trim(path, "/")
	candidates := []string{filepath.Join(root, docsDir, "index.md")}
	if path != "" {
		base := filepath.Join(root, docsDir, filepath.FromSlash(path))
		candidates = []string{base + ".md", filepath.Join(base, "index.md")}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return "", false
}

// checkSkillCommands resolves every `morzer …` a skill shows as code to a
// command in the tree, and every long flag after it to a flag that command
// takes. Prose is left alone: "the morzer repository" names no command.
func checkSkillCommands(rep *report, tree *cobra.Command, rel, raw string) {
	var code []string
	for _, m := range fenceBodyRe.FindAllStringSubmatch(raw, -1) {
		code = append(code, m[1])
	}
	for _, m := range inlineRe.FindAllStringSubmatch(fenceRe.ReplaceAllString(raw, ""), -1) {
		code = append(code, m[1])
	}
	for _, text := range code {
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "morzer" {
					if problem := resolveInvocation(tree, fields[i+1:]); problem != "" {
						rep.add("skills", "%s: `%s`: %s", rel, strings.TrimSpace(line), problem)
					}
				}
			}
		}
	}
}

// resolveInvocation walks the words after `morzer` down the command tree,
// checking each long flag against the command reached so far: a flag the root
// takes may come before the subcommand, as in `morzer --json release verify`.
// It returns what is wrong, or "".
func resolveInvocation(tree *cobra.Command, words []string) string {
	cmd := tree
	resolving := true
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "--" || slices.Contains([]string{"|", "||", "&&", ";", ">", ">>", "\\"}, w) {
			break
		}
		if strings.HasPrefix(w, "--") {
			name, _, hasValue := strings.Cut(strings.TrimPrefix(w, "--"), "=")
			// Cobra adds these two when the binary runs, not when the
			// tree is built, so the tree this walks does not carry them.
			if name == "help" || name == "version" {
				continue
			}
			f := cmd.Flag(name)
			if f == nil {
				return "`--" + name + "` is not a flag of `" + strings.Join(commandPath(cmd), " ") + "`"
			}
			// A flag that takes a value and was not given one with `=`
			// takes the next word, which is then not a subcommand.
			if !hasValue && f.NoOptDefVal == "" {
				i++
			}
			continue
		}
		if !resolving || strings.HasPrefix(w, "-") || strings.ContainsAny(w, "<[./$") {
			resolving = false
			continue
		}
		sub := subcommand(cmd, w)
		if sub == nil {
			// A command that only groups others takes no arguments,
			// so a word it does not know is a misspelt subcommand.
			if !cmd.Runnable() {
				return "`" + strings.Join(append(commandPath(cmd), w), " ") + "` is not a command"
			}
			resolving = false
			continue
		}
		cmd = sub
	}
	return ""
}

// subcommand is cmd's child named name, by its name or an alias, or nil.
func subcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name || slices.Contains(sub.Aliases, name) {
			return sub
		}
	}
	return nil
}

// commandPath is cmd's path from the root, `morzer` included, as words.
func commandPath(cmd *cobra.Command) []string {
	return strings.Fields(cmd.CommandPath())
}
