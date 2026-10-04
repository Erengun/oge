package pipeline

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Proposal is a config Öge detected for a repository with none, to be shown
// to the human and saved only on their confirmation (ADR-0019).
type Proposal struct {
	DetectedFrom string // the marker file, e.g. "go.mod"
	TOML         string
}

// Propose detects likely Check commands, test globs and project-specific
// output globs from marker files at root. ok is false when no ecosystem is
// recognised. Only Go is recognised so far.
func Propose(root string) (p Proposal, ok bool) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return Proposal{}, false
	}
	return Proposal{DetectedFrom: "go.mod", TOML: goProposal(goOutputGlobs(root))}, true
}

// goOutputGlobs proposes output globs from where the project already keeps
// Go code, so a new .go file there reaches the verifier while notes and
// other new files stay Ambiguous.
// TODO(#39-decision): Go output globs are "<top-level dir with Go code>/**/*.go"
// (plus "*.go" for root files); confirm this is what ADR-0019 means by
// project-specific output globs.
func goOutputGlobs(root string) []string {
	set := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		top, _, nested := strings.Cut(filepath.ToSlash(rel), "/")
		if nested {
			set[top+"/**/*.go"] = true
		} else {
			set["*.go"] = true
		}
		return nil
	})
	globs := make([]string, 0, len(set))
	for g := range set {
		globs = append(globs, g)
	}
	sort.Strings(globs)
	return globs
}

func goProposal(outputGlobs []string) string {
	return fmt.Sprintf(`# Öge project config. Check commands and test globs are the Oracle: they
# decide acceptance, and no agent can change them during a Run.
schema = 1

[project]
test_globs   = ["**/*_test.go", "**/testdata/**"]
test_config  = ["go.mod", "go.sum"]
output_globs = %s   # other new files are Ambiguous until you decide

[[check.commands]]
run     = "go test -json ./..."
report  = "go-test-json"
timeout = "10m"
`, tomlList(outputGlobs))
}

func tomlList(s []string) string {
	q := make([]string, len(s))
	for i, v := range s {
		q[i] = fmt.Sprintf("%q", v)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
