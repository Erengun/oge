package run

import (
	"strings"

	"github.com/erengun/oge/internal/oracle"
	"github.com/erengun/oge/internal/workspace"
)

// DeclaredAgentConfig reports whether an Excluded agent-config path p is
// declared as output (#106, #107): an output glob matches it and names
// the agent-config entry that makes it Excluded literally, as a path
// segment, ignoring case (CLAUDE.md, **/AGENTS.md, .claude/**). A declared file is an
// ordinary deliverable: in the Promoted view QA sees, checked and
// delivered. Any other Excluded change is held back from delivery.
// Intent is never inferred from the Task's text.
//
// TODO(#107-decision): a broad glob that only happens to match (**,
// **/*.md, docs/**) doesn't declare agent configuration; the glob must
// name it. The declaration may come from --output or the project's
// output_globs alike.
func DeclaredAgentConfig(outputGlobs []string, p string) bool {
	if !workspace.Excluded(p) {
		return false
	}
	entry := workspace.ExcludedEntry(p)
	for _, g := range outputGlobs {
		if !oracle.Match(g, p) {
			continue
		}
		for _, seg := range strings.Split(g, "/") {
			if strings.EqualFold(seg, entry) {
				return true
			}
		}
	}
	return false
}

// checkedTree is the tree the Check judges for cand: the Candidate with
// each agent-config change the Run didn't declare at the Snapshot's
// version, the tree oge apply delivers by default (#107). One whose
// Snapshot version can't stand beside the rest keeps the Candidate's;
// delivery then refuses it unless --with-agent-config.
func (w *walk) checkedTree(cand string) (string, error) {
	changed, err := w.repo.ChangedFiles(w.snap, cand)
	if err != nil {
		return "", err
	}
	var held []string
	for _, p := range changed {
		if workspace.Excluded(p) && !DeclaredAgentConfig(w.p.Frozen.Project.OutputGlobs, p) {
			held = append(held, p)
		}
	}
	checked, _, err := w.repo.Restore(w.snap, cand, held, "Checked tree of "+cand[:12]+": held-back agent configuration at the Snapshot's version")
	return checked, err
}
