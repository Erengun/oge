package claude

import (
	"path/filepath"
	"strings"
)

// Recovery hints (#90): when Öge denies a common shell idiom, the deny
// reason says in one line what to do instead, so Claude recovers on its
// first retry. A hint never changes a decision; it is chosen only after
// the command was already denied as not pre-authorised, and only when it
// recognises the idiom for sure.
const (
	hintGrep = "Use the Grep tool."
	hintGlob = "Use the Glob tool."
)

// globPipes are the commands a find is commonly piped into to list
// files, which Glob does.
var globPipes = map[string]bool{"head": true, "tail": true, "wc": true, "xargs": true, "sort": true}

// findWrites are find's actions that run, delete or write something.
var findWrites = map[string]bool{"-delete": true, "-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true}

// xargsRuns are the commands a hinted find may hand to xargs: read-only.
var xargsRuns = map[string]bool{"grep": true, "wc": true, "head": true, "tail": true}

// hint is the recovery hint for a denied Bash command, or "".
//   - find … | … grep …: the Grep tool;
//   - find … | head (tail, wc, xargs, sort): the Glob tool;
//   - cd <the Workspace> && <rest>: run <rest> directly.
//
// A hint never points at something Öge would refuse: <rest> must itself
// be pre-authorised, and a find pipeline must be read-only, with every
// path it names inside the Workspace. Every segment must lex as simple
// words; anything else gets no hint.
func (p *policy) hint(cmd string) string {
	if strings.ContainsAny(cmd, "\n\r") {
		return ""
	}
	if dir, rest, ok := cutTopLevel(cmd, "&&"); ok {
		words, ok := lex(dir)
		rest = strings.TrimSpace(rest)
		if ok && len(words) == 2 && words[0].text == "cd" && !words[0].quoted && !words[1].glob &&
			p.isWorkspace(words[1].text) && rest != "" && p.bash(rest) == vOK {
			return "Run " + target("Bash", map[string]any{"command": rest}, p.ws) + " directly; the working directory is already the Workspace."
		}
		return ""
	}
	segs := splitTopLevel(cmd, "|")
	if len(segs) < 2 {
		return ""
	}
	grep, glob := false, false
	for i, s := range segs {
		words, ok := lex(s)
		if !ok || len(words) == 0 || words[0].quoted {
			return ""
		}
		for _, w := range words {
			if w.text == "grep" {
				grep = true
			}
			// Any path-like word must be inside the Workspace.
			if (strings.HasPrefix(w.text, "/") || strings.HasPrefix(w.text, "~") || hasDotDot(w.text)) && p.path(w.text, false) != vOK {
				return ""
			}
		}
		name := words[0].text
		switch {
		case i == 0:
			if name != "find" {
				return ""
			}
			for _, w := range words {
				if findWrites[w.text] {
					return ""
				}
			}
		case name == "grep":
		case name == "xargs":
			glob = true
			run := ""
			for _, w := range words[1:] {
				if !strings.HasPrefix(w.text, "-") {
					run = w.text
					break
				}
			}
			if run != "" && !xargsRuns[run] {
				return ""
			}
		case name == "sort":
			glob = true
			for _, w := range words[1:] {
				if w.text == "-o" || strings.HasPrefix(w.text, "--output") || (strings.HasPrefix(w.text, "-") && !strings.HasPrefix(w.text, "--") && strings.Contains(w.text, "o")) {
					return ""
				}
			}
		case globPipes[name]:
			glob = true
		default:
			return "" // piped into something else
		}
	}
	switch {
	case grep:
		return hintGrep
	case glob:
		return hintGlob
	}
	return ""
}

// isWorkspace reports whether a cd target is the Workspace itself.
func (p *policy) isWorkspace(dir string) bool {
	if p.path(dir, false) != vOK {
		return false
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(p.ws[0], dir)
	}
	dir = filepath.Clean(dir)
	for _, w := range p.ws {
		if dir == w || resolve(dir) == w {
			return true
		}
	}
	return false
}

// splitTopLevel splits s at each sep outside quotes. A "|" that is part
// of "||" doesn't split.
func splitTopLevel(s, sep string) []string {
	var out []string
	for {
		before, after, ok := cutTopLevel(s, sep)
		if !ok {
			return append(out, s)
		}
		out = append(out, before)
		s = after
	}
}

// cutTopLevel cuts s around the first sep outside quotes.
func cutTopLevel(s, sep string) (before, after string, ok bool) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\':
			i++
		case strings.HasPrefix(s[i:], sep):
			if sep == "|" && (strings.HasPrefix(s[i:], "||") || (i > 0 && s[i-1] == '|')) {
				i++
				continue
			}
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
