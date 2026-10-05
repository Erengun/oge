package cli

import (
	"fmt"
	"strings"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
)

// resolvedText is the line once the Ambiguous-file review has resolved
// every file: what it did, the new Candidate and what runs next.
func resolvedText(ev run.Event) string {
	var promoted, dropped int
	for _, r := range ev.Result.Resolutions {
		switch r.Choice {
		case "promote":
			promoted += len(r.Files)
		case "drop":
			dropped += len(r.Files)
		}
	}
	var parts []string
	if promoted > 0 {
		parts = append(parts, fmt.Sprintf("%d promoted", promoted))
	}
	if dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d dropped", dropped))
	}
	parts = append(parts, "Candidate "+short(ev.Result.Candidate))
	if ev.Next == "verify" {
		parts = append(parts, "fresh QA, then the final Check")
	} else {
		parts = append(parts, "the final Check")
	}
	return strings.Join(parts, " · ")
}

// resolvedStages are the stages an Ambiguous-file review's resolution
// runs next: QA, when a file was promoted and the mode has a verifier,
// then the Check.
func resolvedStages(f *pipeline.Frozen, next string) []pipeline.Stage {
	var out []pipeline.Stage
	if next == "verify" {
		for _, s := range f.Stages {
			if s.Role == "verifier" {
				out = append(out, s)
			}
		}
	}
	return out
}
