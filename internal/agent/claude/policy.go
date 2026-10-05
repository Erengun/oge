package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/redact"
)

// TODO(#44-decision): the pre-authorised list (ADR-0019). Until
// interactive Host requests exist (#45), what isn't on it is denied with a
// reason, so the list decides what an unattended implementer can do. It
// is versioned by ProfileVersion and hashed into the Launch profile.
//
// Pre-authorised for the implementer, all inside the Workspace:
//   - Read, Glob and Grep;
//   - Edit and Write, except under .git;
//   - Bash running exactly one of the Run's Check commands;
//   - Bash running one simple command that starts with one of
//     preAuthorised, with every path argument inside the Workspace, no
//     flag that runs another program, and only allowedSuffixes after it.
var preAuthorised = [][]string{
	{"go", "build"}, {"go", "vet"}, {"go", "test"}, {"gofmt"},
	{"ls"}, {"cat"}, {"git", "diff"}, {"git", "status"},
}

// allowedSuffixes may end a pre-authorised command: they only reshape its
// output.
var allowedSuffixes = []string{"2>&1", "| head", "| tail"}

// Policy rules, as recorded on each HostDecision.
const (
	rulePreAuthorised = "pre_authorised"
	ruleOutside       = "outside_role" // clearly outside the role or the Workspace
	ruleNoInteractive = "no_interactive_approval"
	ruleQuestion      = "question_unanswerable"
)

const denyTail = " Interactive approval isn't available yet, so don't retry this another way. " +
	"Use Read, Edit, Write, Glob and Grep inside the Workspace, or run the project's Check commands."

// policy answers one Session's Host requests.
type policy struct {
	ws      []string // the Workspace, as given and with symlinks resolved
	private []string
	checks  []string
	by      string // "launch_profile:<hash>"
	hash    string
}

func newPolicy(spec agent.LaunchSpec, hash string) *policy {
	p := &policy{checks: spec.CheckCommands, by: "launch_profile:" + hash, hash: hash}
	p.ws = withReal(spec.Workspace)
	for _, d := range spec.DenyRead {
		p.private = append(p.private, withReal(d)...)
	}
	return p
}

func withReal(dir string) []string {
	dir = filepath.Clean(dir)
	out := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		out = append(out, r)
	}
	return out
}

// decide answers one tool call. input is the tool's input object.
func (p *policy) decide(tool string, input json.RawMessage) agent.HostDecision {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	d := agent.HostDecision{Family: agent.Approval, Tool: tool, Target: target(tool, in, p.ws), By: p.by}
	allow := func() agent.HostDecision {
		d.Decision, d.Rule, d.Reason = "allow", rulePreAuthorised, "pre-authorised by Launch profile "+p.hash
		return d
	}
	deny := func(rule, why string) agent.HostDecision {
		d.Decision, d.Rule, d.Reason = "deny", rule, "Öge denied this: "+why+"."
		if rule != ruleQuestion {
			d.Reason += denyTail
		}
		return d
	}
	switch tool {
	case "Read", "Glob", "Grep":
		path := str("file_path")
		if tool != "Read" {
			path = str("path")
		}
		if p.isPrivate(path) || (tool == "Glob" && filepath.IsAbs(str("pattern")) && p.isPrivate(str("pattern"))) {
			return deny(ruleOutside, "Öge's private state is never readable")
		}
		if (tool == "Read" && path == "") || !p.inside(path) || (tool == "Glob" && filepath.IsAbs(str("pattern")) && !p.inside(str("pattern"))) {
			return deny(ruleOutside, "it reads outside the Workspace")
		}
		return allow()
	case "Edit", "Write", "NotebookEdit":
		path := str("file_path")
		if tool == "NotebookEdit" {
			path = str("notebook_path")
		}
		if path == "" || !p.inside(path) || p.isPrivate(path) {
			return deny(ruleOutside, "it writes outside the Workspace")
		}
		if p.underGit(path) {
			return deny(ruleOutside, "the implementer doesn't write under .git")
		}
		return allow()
	case "Bash":
		cmd := strings.TrimSpace(str("command"))
		for _, priv := range p.private {
			if strings.Contains(cmd, priv) {
				return deny(ruleOutside, "Öge's private state is never readable")
			}
		}
		switch p.bash(cmd) {
		case bashOK:
			return allow()
		case bashOutside:
			return deny(ruleOutside, "the command reaches outside the Workspace")
		}
		return deny(ruleNoInteractive, "this command isn't pre-authorised")
	case "AskUserQuestion":
		// A question is never answered on the human's behalf (ADR-0019).
		d.Family = agent.Question
		d.Decision, d.Rule = "cancel", ruleQuestion
		d.Reason = "No one can answer questions during this Run. Make a reasonable, minimal and reversible assumption, say what you assumed, and continue."
		return d
	}
	return deny(ruleOutside, tool+" isn't one of the implementer's tools")
}

// inside reports whether path, absolute or relative to the Workspace,
// resolves inside it, following any symlink that already exists.
func (p *policy) inside(path string) bool {
	if path == "" {
		return true // the tool's default: the working directory
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.ws[0], path)
	}
	path = filepath.Clean(path)
	if !under(path, p.ws) {
		return false
	}
	return under(resolve(path), p.ws)
}

func (p *policy) isPrivate(path string) bool {
	if path == "" || len(p.private) == 0 {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.ws[0], path)
	}
	path = filepath.Clean(path)
	return under(path, p.private) || under(resolve(path), p.private)
}

func (p *policy) underGit(path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.ws[0], path)
	}
	for _, ws := range p.ws {
		if rel, err := filepath.Rel(ws, filepath.Clean(path)); err == nil {
			first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if first == ".git" {
				return true
			}
		}
	}
	return false
}

// resolve follows symlinks in path's longest existing prefix.
func resolve(path string) string {
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(p) == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

func under(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

type bashVerdict int

const (
	bashGrey bashVerdict = iota
	bashOK
	bashOutside
)

// bash classifies a shell command.
func (p *policy) bash(cmd string) bashVerdict {
	for _, c := range p.checks {
		if cmd == strings.TrimSpace(c) {
			return bashOK
		}
	}
	dir := p.ws[0]
	// One leading "cd <dir> &&", as models often write.
	if rest, ok := strings.CutPrefix(cmd, "cd "); ok {
		d, after, ok := strings.Cut(rest, "&&")
		d = unquote(strings.TrimSpace(d))
		if !ok || d == "" {
			return bashGrey
		}
		if !p.inside(d) {
			return bashOutside
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		dir, cmd = d, strings.TrimSpace(after)
		for _, c := range p.checks {
			if cmd == strings.TrimSpace(c) {
				return bashOK
			}
		}
	}
	cmd = trimSuffixes(cmd)
	if cmd == "" || strings.ContainsAny(cmd, ";&|<>`$\n\r(){}~!") {
		return bashGrey
	}
	words, ok := split(cmd)
	if !ok || len(words) == 0 {
		return bashGrey
	}
	matched := false
	for _, pre := range preAuthorised {
		if len(words) >= len(pre) && equal(words[:len(pre)], pre) {
			matched = true
			break
		}
	}
	verdict := bashGrey
	if matched {
		verdict = bashOK
	}
	for _, w := range words[1:] {
		lw := strings.ToLower(w)
		if strings.HasPrefix(w, "-") && (strings.Contains(lw, "exec") || strings.Contains(lw, "vettool") ||
			strings.Contains(lw, "overlay") || strings.Contains(lw, "output") || strings.Contains(lw, "ext-diff")) {
			return bashGrey
		}
		val := w
		if strings.HasPrefix(w, "-") {
			_, v, ok := strings.Cut(w, "=")
			if !ok {
				continue
			}
			val = v
		}
		if !strings.Contains(val, "/") && val != ".." {
			continue
		}
		path := val
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if !p.inside(path) || p.isPrivate(path) {
			return bashOutside
		}
	}
	return verdict
}

// trimSuffixes drops output-only suffixes: "2>&1" and one "| head" or
// "| tail" with at most a line count.
func trimSuffixes(cmd string) string {
	if before, after, ok := strings.Cut(cmd, "|"); ok {
		f := strings.Fields(after)
		if len(f) == 0 || (f[0] != "head" && f[0] != "tail") || len(f) > 3 {
			return cmd
		}
		for _, a := range f[1:] {
			if strings.Trim(a, "-n0123456789") != "" {
				return cmd
			}
		}
		cmd = strings.TrimSpace(before)
	}
	return strings.TrimSpace(strings.TrimSuffix(cmd, "2>&1"))
}

// split splits a command into words, honouring simple quotes. ok is false
// for an unbalanced quote.
func split(s string) (words []string, ok bool) {
	var cur strings.Builder
	inWord, escaped := false, false
	var quote rune
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

func unquote(s string) string {
	if w, ok := split(s); ok && len(w) == 1 {
		return w[0]
	}
	return s
}

func equal(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return len(a) == len(b)
}

// target is a tool call's short, redacted target: a Workspace-relative
// path, a pattern, or a command's first line. It never carries file
// contents. ws is the Workspace, as given and with symlinks resolved.
func target(tool string, in map[string]any, ws []string) string {
	str := func(k string) string { s, _ := in[k].(string); return s }
	var t string
	switch tool {
	case "Read", "Edit", "Write":
		t = relPath(str("file_path"), ws)
	case "NotebookEdit":
		t = relPath(str("notebook_path"), ws)
	case "Glob", "Grep":
		t = str("pattern")
		if p := str("path"); p != "" {
			t += " in " + relPath(p, ws)
		}
	case "Bash":
		t, _, _ = strings.Cut(strings.TrimSpace(str("command")), "\n")
		for _, w := range ws {
			for _, form := range []string{w, strings.ReplaceAll(w, " ", `\ `), `"` + w + `"`, "'" + w + "'"} {
				t = strings.ReplaceAll(t, form+string(os.PathSeparator), "")
				t = strings.ReplaceAll(t, "cd "+form+" && ", "")
			}
		}
	case "AskUserQuestion":
		t = "a question"
	}
	return shorten(string(redact.Redact([]byte(t))), 80)
}

func relPath(p string, ws []string) string {
	if p == "" {
		return ""
	}
	for _, w := range ws {
		if rel, err := filepath.Rel(w, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return filepath.ToSlash(rel)
		}
	}
	return p
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
