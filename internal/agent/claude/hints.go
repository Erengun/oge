package claude

import "strings"

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

// hint is the recovery hint for a denied Bash command, or "".
//   - find … | … grep …: the Grep tool;
//   - find … | head (tail, wc, xargs, sort): the Glob tool;
//   - cd <the Workspace, or a directory in it> && <rest>: run <rest>
//     directly, as long as <rest> alone doesn't reach outside.
//
// Every segment must lex as simple words; anything else gets no hint.
func (p *policy) hint(cmd string) string {
	if strings.ContainsAny(cmd, "\n\r") {
		return ""
	}
	if dir, rest, ok := cutTopLevel(cmd, "&&"); ok {
		words, ok := lex(dir)
		if ok && len(words) == 2 && words[0].text == "cd" && !words[0].quoted && !words[1].glob &&
			p.path(words[1].text, false) == vOK {
			rest = strings.TrimSpace(rest)
			if rest == "" || p.bash(rest) == vOutside {
				return ""
			}
			return "Run " + target("Bash", map[string]any{"command": rest}, p.ws) + " directly; the working directory is already the Workspace."
		}
		return ""
	}
	segs := splitTopLevel(cmd, "|")
	if len(segs) < 2 {
		return ""
	}
	first, ok := lex(segs[0])
	if !ok || len(first) == 0 || first[0].text != "find" || first[0].quoted || p.bash(segs[0]) == vOutside {
		return ""
	}
	grep, glob := false, false
	for _, s := range segs[1:] {
		words, ok := lex(s)
		if !ok || len(words) == 0 {
			return ""
		}
		for _, w := range words {
			grep = grep || w.text == "grep"
		}
		glob = glob || globPipes[words[0].text]
		if !globPipes[words[0].text] && words[0].text != "grep" {
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
