package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/erengun/oge/internal/delivery"

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

// shownPath is a Candidate path as the review shows it: quoted, so every
// rune is visible, when it holds a control, bidirectional or invisible
// character. A Trojan Source name never reads other than it is.
func shownPath(p string) string {
	if strings.ContainsFunc(p, delivery.Hidden) {
		return strconv.QuoteToASCII(p)
	}
	return clean(pathText(p))
}

// inspectLineRunes bounds each line an inspect view shows.
const inspectLineRunes = 400

// shownLine is one line of a file in the inspect view: control characters
// dropped, and bidirectional and invisible ones escaped where they stand,
// so the human sees them rather than their effect.
func shownLine(l string) string {
	var b strings.Builder
	for _, r := range clean(strings.ReplaceAll(l, "\t", "    ")) {
		if delivery.Hidden(r) {
			fmt.Fprintf(&b, "<U+%04X>", r)
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if n := utf8.RuneCountInString(s); n > inspectLineRunes {
		s = string([]rune(s)[:inspectLineRunes]) + fmt.Sprintf("… (%d more characters)", n-inspectLineRunes)
	}
	return s
}
