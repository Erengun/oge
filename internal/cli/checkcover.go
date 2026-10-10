package cli

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/workspace"
)

// checkCoverage is a static check over the Snapshot's paths, the frozen
// test_globs and the Check commands. It executes nothing. When every Check
// command is a plain `go test` and some protected test directory matches none
// of their package patterns, the Run would end in an Infrastructure stop after
// the agent had worked (#114), so it returns that as a refusal. Opaque
// commands (make test, scripts) get no claim.
func checkCoverage(root string, paths, globs, runs []string) (refusal string, warns []string) {
	protected := map[string][]string{} // package dir -> protected _test.go files
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") || !oracle.MatchAny(globs, p) {
			continue
		}
		if dir := path.Dir(p); oracle.GoPackageDir(dir) {
			protected[dir] = append(protected[dir], p)
		}
	}
	if len(protected) == 0 {
		return "", nil
	}

	var cmds []goTestCmd
	allPlain := len(runs) > 0
	filtered := false
	for _, r := range runs {
		c, ok := parseGoTest(r)
		if !ok {
			allPlain = false
			continue
		}
		cmds = append(cmds, c)
		filtered = filtered || c.filters
	}
	if filtered {
		warns = append(warns, "the Check filters tests; protected tests outside the filter will stop the Run")
	}

	// A protected package's testdata that no test_globs entry matches is
	// writable by the Candidate. One pass: testdata's parent dirs whose
	// files the globs leave out.
	looseDirs := map[string]bool{}
	for _, p := range paths {
		dir, _, ok := strings.Cut(p, "/testdata/")
		if !ok {
			if !strings.HasPrefix(p, "testdata/") {
				continue
			}
			dir = "."
		}
		if !oracle.MatchAny(globs, p) {
			looseDirs[dir] = true
		}
	}
	var loose []string
	for dir := range looseDirs {
		if _, ok := protected[dir]; ok {
			loose = append(loose, dir)
		}
	}
	if len(loose) > 0 {
		sort.Strings(loose)
		warns = append(warns, fmt.Sprintf("%s has testdata/ that no test_globs entry matches: its goldens would be writable by the agent", dirList(loose)))
	}

	if !allPlain {
		return "", warns
	}
	var uncovered []string
	files := 0
	for dir, fs := range protected {
		covered := false
		for _, c := range cmds {
			if c.covers(dir) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		// Files that never run here (build-constrained) or expect nothing
		// (helpers) don't make the Check unable to run the protected tests.
		n := 0
		for _, f := range fs {
			if sf, err := workspace.ReadSnapshotFile(root, f); err != nil || !sf.InSnapshot || oracle.PlainlyExpected(f, sf.Data) {
				n++
			}
		}
		if n > 0 {
			uncovered = append(uncovered, dir)
			files += n
		}
	}
	if len(uncovered) == 0 {
		return "", warns
	}
	sort.Strings(uncovered)
	scope := ""
	if len(cmds) == 1 && len(cmds[0].patterns) == 1 {
		p := cmds[0].patterns[0]
		switch {
		case p == "...":
			scope = "**/*_test.go"
		case strings.HasSuffix(p, "/..."):
			scope = strings.TrimSuffix(p, "...") + "**/*_test.go"
		case p == ".":
			scope = "*_test.go"
		default:
			scope = p + "/*_test.go"
		}
		scope = " (e.g. " + scope + ")"
	}
	return fmt.Sprintf("the Check can't run %d protected test %s in %s; the Run would end in an Infrastructure stop. "+
		"Scope project.test_globs to what the Check runs%s, or widen the Check to ./...",
		files, covPlural(files, "file", "files"), dirList(uncovered), scope), warns
}

func covPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func dirList(dirs []string) string {
	shown, more := dirs, ""
	if len(dirs) > 3 {
		shown, more = dirs[:3], fmt.Sprintf(" and %d more", len(dirs)-3)
	}
	return fmt.Sprintf("%d package %s (%s%s)", len(dirs), covPlural(len(dirs), "dir", "dirs"), strings.Join(shown, ", "), more)
}

// goTestCmd is a parsed plain `go test` command.
type goTestCmd struct {
	patterns []string // cleaned package patterns; "..." is all
	filters  bool     // -run or -skip
}

func (c goTestCmd) covers(dir string) bool {
	for _, p := range c.patterns {
		switch {
		case p == "...":
			return true
		case strings.HasSuffix(p, "/..."):
			base := strings.TrimSuffix(p, "/...")
			if dir == base || strings.HasPrefix(dir, base+"/") {
				return true
			}
		case p == dir:
			return true
		}
	}
	return false
}

// Flags that take a value in the next word (when not given as -flag=value),
// and flags that don't. Any other flag makes the command unparseable, so
// Öge makes no claim.
var (
	goTestValueFlags = map[string]bool{
		"run": true, "skip": true, "tags": true, "timeout": true, "count": true, "p": true,
		"parallel": true, "cpu": true, "bench": true, "benchtime": true, "covermode": true,
		"coverpkg": true, "coverprofile": true, "mod": true, "modfile": true, "gcflags": true,
		"ldflags": true, "shuffle": true, "fuzz": true, "fuzztime": true, "list": true,
		"exec": true, "vet": true, "overlay": true, "o": true,
	}
	goTestBoolFlags = map[string]bool{
		"v": true, "json": true, "race": true, "short": true, "cover": true, "failfast": true,
		"x": true, "n": true, "a": true, "work": true, "trimpath": true, "benchmem": true,
		"msan": true, "asan": true,
	}
)

// parseGoTest parses a plain `go test` command: no shell metacharacters, no
// quoting, no environment prefix. ok is false when it can't tell confidently.
func parseGoTest(run string) (c goTestCmd, ok bool) {
	if strings.ContainsAny(run, "|;&$()<>`'\"\\*?[]{}~!#\n") {
		return c, false
	}
	w := strings.Fields(run)
	if len(w) < 2 || w[0] != "go" || w[1] != "test" {
		return c, false
	}
	for i := 2; i < len(w); i++ {
		a := w[i]
		if a == "-args" || a == "--args" {
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			// Only directory patterns under the current one: an import
			// path, all, std or cmd could name anything.
			if a != "." && !strings.HasPrefix(a, "./") {
				return c, false
			}
			p := path.Clean(a)
			switch {
			case strings.Contains(strings.TrimSuffix(strings.TrimSuffix(p, "..."), "/"), "..."):
				return c, false
			case strings.HasSuffix(p, "...") && p != "..." && !strings.HasSuffix(p, "/..."):
				return c, false
			}
			c.patterns = append(c.patterns, p)
			continue
		}
		name := strings.TrimLeft(a, "-")
		val := strings.Contains(name, "=")
		if val {
			name = name[:strings.Index(name, "=")]
		}
		switch {
		case goTestValueFlags[name]:
			if name == "run" || name == "skip" {
				c.filters = true
			}
			if !val {
				i++
				if i >= len(w) {
					return c, false
				}
			}
		case goTestBoolFlags[name]:
		default:
			return c, false
		}
	}
	if len(c.patterns) == 0 {
		c.patterns = []string{"."}
	}
	// "./x" cleans to "x"; "./..." to "..."; "." stays ".".
	return c, true
}

// checkCoverageOf runs checkCoverage over the Snapshot at root and the
// frozen Pipeline. A Snapshot that can't be listed makes no claim.
func checkCoverageOf(root string, f *pipeline.Frozen) (string, []string) {
	paths, err := workspace.SnapshotPaths(root)
	if err != nil {
		return "", nil
	}
	var runs []string
	for _, c := range f.Checks {
		runs = append(runs, c.Run)
	}
	return checkCoverage(root, paths, f.Project.TestGlobs, runs)
}

// joinWarnings joins warnings for the summary's "! " line.
func joinWarnings(ws ...string) string {
	var out []string
	for _, w := range ws {
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, "\n! ")
}
