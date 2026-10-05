package delivery

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/workspace"
)

// Held back from delivery (ADR-0015; #105, #107): Candidate changes that
// oge apply and oge branch leave out unless the human says otherwise.
// oge diff shows them, marked.
const (
	// HeldUnresolved is an Ambiguous file a Run that isn't Accepted ended
	// with, never promoted or dropped. Delivery needs an explicit include
	// or exclude; there is no default.
	HeldUnresolved = "unresolved"
	// HeldAgentConfig is a change to an Excluded agent-config file the
	// Run didn't declare as output. It stays out unless asked for.
	HeldAgentConfig = "agent-config"
)

// Held is one Candidate change delivery holds back.
type Held struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// What to do with the unresolved Ambiguous files.
const (
	Include = "with"
	Exclude = "without"
)

// Choice is a delivery's explicit choices, each one a flag named after
// what it delivers. The zero Choice delivers an Accepted Candidate
// without its held-back agent configuration.
type Choice struct {
	// Outcome is FlagOverridden or FlagRejected for a Run that isn't
	// Accepted (ADR-0015).
	Outcome string
	// Unresolved is Include or Exclude for the unresolved Ambiguous files
	// (--with-unresolved, --without-unresolved).
	Unresolved string
	// AgentConfig delivers the held-back agent-config changes
	// (--with-agent-config).
	AgentConfig bool
}

// HeldBack sorts changed paths into what delivery holds back by default:
// the unresolved Ambiguous files, and agent-config changes no output glob
// declares. Other paths aren't listed.
func HeldBack(paths, outputGlobs, unresolved []string) []Held {
	open := map[string]bool{}
	for _, p := range unresolved {
		open[p] = true
	}
	var out []Held
	for _, p := range paths {
		switch {
		case open[p]:
			out = append(out, Held{p, HeldUnresolved})
		case workspace.Excluded(p) && !run.DeclaredAgentConfig(outputGlobs, p):
			out = append(out, Held{p, HeldAgentConfig})
		}
	}
	return out
}

// Declared lists the changed paths that are agent configuration the Run
// declared as output: delivered like any other change.
func Declared(paths, outputGlobs []string) []string {
	var out []string
	for _, p := range paths {
		if run.DeclaredAgentConfig(outputGlobs, p) {
			out = append(out, p)
		}
	}
	return out
}

// held is what r's Candidate holds back. An Accepted Run has no
// unresolved files: the review resolves each one first (#97).
func (r *Run) held() ([]Held, error) {
	changes, err := r.repo().Changes(r.Snapshot, r.Candidate)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	unresolved := r.Unresolved
	if r.Outcome == run.Accepted {
		unresolved = nil
	}
	return HeldBack(paths, r.OutputGlobs, unresolved), nil
}

// Holding is what a delivery leaves out, and what it includes on request.
type Holding struct {
	Left, Included []Held
}

func (h *Holding) skip() map[string]bool {
	m := map[string]bool{}
	for _, x := range h.Left {
		m[x.Path] = true
	}
	return m
}

// Count is how many of hs are of kind.
func Count(hs []Held, kind string) int {
	n := 0
	for _, h := range hs {
		if h.Kind == kind {
			n++
		}
	}
	return n
}

// Hold works out what a delivery of r leaves out under c, and refuses c
// when it leaves a choice unmade or asks for one there is nothing to make.
func Hold(r *Run, c Choice) (*Holding, error) {
	held, err := r.held()
	if err != nil {
		return nil, err
	}
	var open []string
	for _, h := range held {
		if h.Kind == HeldUnresolved {
			open = append(open, h.Path)
		}
	}
	nConfig := len(held) - len(open)
	switch {
	case len(open) > 0 && c.Unresolved == "":
		return nil, refuse("Candidate %s has %s nobody promoted or dropped: %s. Pass --with-unresolved to deliver them or --without-unresolved to leave them out",
			Short(r.Candidate), plural(len(open), "Ambiguous file", "Ambiguous files"), shownList(open))
	case len(open) == 0 && c.Unresolved != "":
		return nil, refuse("Run %s has no unresolved Ambiguous files; --%s-unresolved is only for a Run that has", r.ID, c.Unresolved)
	case nConfig == 0 && c.AgentConfig:
		return nil, refuse("Run %s holds back no agent-config change; --with-agent-config is only for a Run that does", r.ID)
	}
	h := &Holding{}
	for _, x := range held {
		if x.Kind == HeldUnresolved && c.Unresolved == Include || x.Kind == HeldAgentConfig && c.AgentConfig {
			h.Included = append(h.Included, x)
		} else {
			h.Left = append(h.Left, x)
		}
	}
	return h, nil
}

// LeftLine says what a delivery left out, or "" when nothing.
func (h *Holding) LeftLine() string {
	var parts []string
	if n := Count(h.Left, HeldUnresolved); n > 0 {
		parts = append(parts, plural(n, "Ambiguous file", "Ambiguous files")+" left out")
	}
	if n := Count(h.Left, HeldAgentConfig); n > 0 {
		parts = append(parts, plural(n, "agent-config change", "agent-config changes")+" held back (--with-agent-config delivers them)")
	}
	return strings.Join(parts, " · ")
}

// shownList names paths safely for the terminal, at most five.
func shownList(ps []string) string {
	var out []string
	for i, p := range ps {
		if i == 5 {
			out = append(out, fmt.Sprintf("and %d more", len(ps)-5))
			break
		}
		out = append(out, Shown(p))
	}
	return strings.Join(out, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// heldMarks are the lines oge diff puts before each held-back path.
func heldMarks(held []Held) map[string]string {
	m := map[string]string{}
	for _, h := range held {
		switch h.Kind {
		case HeldUnresolved:
			m[h.Path] = fmt.Sprintf("# Öge: %s is an Ambiguous file nobody promoted or dropped; oge apply and oge branch need --with-unresolved or --without-unresolved\n", Shown(h.Path))
		case HeldAgentConfig:
			m[h.Path] = fmt.Sprintf("# Öge: %s is an agent-config change, held back: oge apply and oge branch leave it out unless --with-agent-config\n", Shown(h.Path))
		}
	}
	return m
}

// note adds to a Delivery record what it held back and what it included
// on request.
func (h *Holding) note(rec map[string]any) map[string]any {
	if len(h.Left) > 0 {
		rec["held_back"] = h.Left
	}
	if len(h.Included) > 0 {
		rec["included"] = h.Included
	}
	return rec
}
