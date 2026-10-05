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

// TODO(#44-decision): the pre-authorised operations (ADR-0019, as
// decided on #44): a known operation, in a known-safe shape, on paths
// contained in the Workspace. Until interactive Host requests exist (#45),
// anything else is denied with a reason. The set is versioned by
// ProfileVersion and hashed into the Launch profile.
//
// For the implementer, inside the Workspace:
//   - Read, Glob and Grep;
//   - Edit and Write, except under any .git (case-folded, at any depth);
//   - Bash running exactly one of the Run's Check commands;
//   - Bash running one simple command from bashShapes (bash.go), with
//     only that command's positive flag set and every other word a
//     contained path.
//
// Policy rules, as recorded on each HostDecision.
const (
	rulePreAuthorised = "pre_authorised"
	ruleOutside       = "outside_role" // clearly outside the role or the Workspace
	ruleNoInteractive = "no_interactive_approval"
	ruleQuestion      = "question_unanswerable"
	// ruleUnchecked denies every request until the envelope check has
	// passed, and after it failed (ADR-0005 as amended).
	ruleUnchecked = "envelope_not_passed"
)

const denyTail = " Interactive approval isn't available yet, so don't retry this another way. " +
	"Use Read, Edit, Write, Glob and Grep inside the Workspace, or run the project's Check commands from the current directory."

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

// verdict is how a request, or one of its paths, classifies.
type verdict int

const (
	vOK      verdict = iota
	vGrey            // not pre-authorised: would need approval
	vOutside         // clearly outside the role or the Workspace
)

func worst(a, b verdict) verdict { return max(a, b) }

// decide answers one tool call. input is the tool's input object.
func (p *policy) decide(tool string, input json.RawMessage) agent.HostDecision {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	d := agent.HostDecision{Family: agent.Approval, Tool: tool, Target: target(tool, in, p.ws), By: p.by}
	answer := func(v verdict, outside, grey string) agent.HostDecision {
		switch v {
		case vOK:
			d.Decision, d.Rule, d.Reason = "allow", rulePreAuthorised, "pre-authorised by Launch profile "+p.hash
			return d
		case vOutside:
			d.Decision, d.Rule, d.Reason = "deny", ruleOutside, "Öge denied this: "+outside+"."+denyTail
			return d
		}
		d.Decision, d.Rule, d.Reason = "deny", ruleNoInteractive, "Öge denied this: "+grey+"."+denyTail
		return d
	}
	switch tool {
	case "Read":
		if str("file_path") == "" {
			return answer(vOutside, "it names no file in the Workspace", "")
		}
		return answer(p.path(str("file_path"), false), "it reads outside the Workspace", "")
	case "Glob":
		return answer(worst(p.path(str("path"), false), p.pattern(str("pattern"), str("path"))), "it searches outside the Workspace", "")
	case "Grep":
		return answer(worst(p.path(str("path"), false), p.pattern(str("glob"), str("path"))), "it searches outside the Workspace", "")
	case "Edit", "Write", "NotebookEdit":
		path := str("file_path")
		if tool == "NotebookEdit" {
			path = str("notebook_path")
		}
		if path == "" {
			return answer(vOutside, "it names no file in the Workspace", "")
		}
		return answer(p.path(path, true), "it writes outside the Workspace or under .git", "")
	case "Bash":
		cmd := strings.TrimSpace(str("command"))
		v := p.bash(cmd)
		if v == vGrey {
			if h := p.hint(cmd); h != "" {
				// TODO(#90-decision): a hint replaces denyTail, whose "don't
				// retry this another way" it would contradict.
				d.Decision, d.Rule, d.Reason = "deny", ruleNoInteractive, "Öge denied this: this command isn't pre-authorised. "+h
				d.Hint = h
				return d
			}
		}
		return answer(v, "the command reaches outside the Workspace", "this command isn't pre-authorised")
	case "AskUserQuestion":
		// A question is never answered on the human's behalf (ADR-0019).
		d.Family = agent.Question
		d.Decision, d.Rule = "cancel", ruleQuestion
		d.Reason = "No one can answer questions during this Run. Make a reasonable, minimal and reversible assumption, say what you assumed, and continue."
		return d
	}
	return answer(vOutside, tool+" isn't one of the implementer's tools", "")
}

// refuse answers a request that arrives while no envelope has passed.
func (p *policy) refuse(tool string, input json.RawMessage, why string) agent.HostDecision {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	d := agent.HostDecision{Family: agent.Approval, Tool: tool, Target: target(tool, in, p.ws), By: p.by,
		Decision: "deny", Rule: ruleUnchecked, Reason: "Öge denied this: " + why + "."}
	if tool == "AskUserQuestion" {
		d.Family, d.Decision = agent.Question, "cancel"
	}
	return d
}

// path classifies one path a tool names, absolute or relative to the
// Workspace. Empty means the tool's default, the Workspace itself. write
// also refuses anything under a .git directory.
func (p *policy) path(path string, write bool) verdict {
	return p.pathFrom(p.ws[0], path, write)
}

func (p *policy) pathFrom(cwd, path string, write bool) verdict {
	if path == "" {
		return vOK
	}
	// No home or variable expansion, no Windows-style or volume paths,
	// and no relative "..": a contained path never needs them.
	if strings.HasPrefix(path, "~") || strings.HasPrefix(path, "$") || strings.Contains(path, "\\") ||
		filepath.VolumeName(path) != "" || driveLetter(path) || (!filepath.IsAbs(path) && hasDotDot(path)) {
		return vOutside
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if !under(path, p.ws) || p.isPrivate(path) {
		return vOutside
	}
	real := resolve(path) // follows any symlink, at any component
	if !under(real, p.ws) || p.isPrivate(real) {
		return vOutside
	}
	if write && (inGit(path) || inGit(real)) {
		return vOutside
	}
	return vOK
}

// pattern classifies a Glob pattern or a Grep glob filter, relative to
// base (the tool's path; empty is the Workspace). Its fixed prefix, and
// whatever its first wildcard part matches now, must resolve inside the
// Workspace, symlinks followed.
func (p *policy) pattern(pat, base string) verdict {
	if pat == "" {
		return vOK
	}
	if strings.HasPrefix(pat, "~") || strings.HasPrefix(pat, "$") || strings.Contains(pat, "..") ||
		strings.Contains(pat, "\\") || driveLetter(pat) || filepath.VolumeName(pat) != "" {
		return vOutside
	}
	dir := p.ws[0]
	if base != "" {
		if filepath.IsAbs(base) {
			dir = filepath.Clean(base)
		} else {
			dir = filepath.Join(dir, base)
		}
	}
	if filepath.IsAbs(pat) {
		dir, pat = string(filepath.Separator), strings.TrimPrefix(filepath.ToSlash(pat), "/")
	}
	parts := strings.Split(filepath.ToSlash(pat), "/")
	fixed := dir
	for i, c := range parts {
		if !strings.ContainsAny(c, "*?[{") {
			if i == len(parts)-1 {
				break // the last part names files, not a directory to enter
			}
			fixed = filepath.Join(fixed, c)
			continue
		}
		if v := p.path(fixed, false); v != vOK {
			return v
		}
		if c == "**" || i == len(parts)-1 {
			return vOK
		}
		matches, err := filepath.Glob(filepath.Join(fixed, c))
		if err != nil {
			return vGrey
		}
		for _, m := range matches {
			if v := p.path(m, false); v != vOK {
				return v
			}
		}
		return vOK
	}
	return p.path(fixed, false)
}

func driveLetter(s string) bool {
	return len(s) >= 2 && s[1] == ':' && (s[0]|0x20 >= 'a' && s[0]|0x20 <= 'z')
}

func hasDotDot(path string) bool {
	for _, c := range strings.Split(filepath.ToSlash(path), "/") {
		if c == ".." {
			return true
		}
	}
	return false
}

// inGit reports whether any component of path is a .git, case-folded.
func inGit(path string) bool {
	for _, c := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(c, ".git") {
			return true
		}
	}
	return false
}

func (p *policy) isPrivate(path string) bool {
	return len(p.private) > 0 && (under(path, p.private) || under(resolve(path), p.private))
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
		t = shortenWorkspace(t, ws)
	case "AskUserQuestion":
		t = "a question"
	}
	return shorten(noControl(string(redact.Redact([]byte(t)))), 80)
}

// noControl drops C0, DEL and C1 control characters, so no terminal
// escape reaches a target or a hint built from one.
func noControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

// shortenWorkspace writes a command's Workspace paths relative to it:
// "<ws>/a" as "a" and the Workspace itself as ".", in its plain,
// escaped and quoted forms. Everything else, a "cd" prefix included,
// stays, so "cd <ws> && go test" reads apart from "go test" (#90).
func shortenWorkspace(t string, ws []string) string {
	for _, w := range ws {
		for _, form := range []string{w, strings.ReplaceAll(w, " ", `\ `), `"` + w + `"`, "'" + w + "'"} {
			t = strings.ReplaceAll(t, form+string(os.PathSeparator), "")
			var b strings.Builder
			for {
				i := strings.Index(t, form)
				if i < 0 {
					break
				}
				end := i + len(form)
				b.WriteString(t[:i])
				if end == len(t) || strings.ContainsRune(" ;&|)", rune(t[end])) {
					b.WriteString(".")
				} else {
					b.WriteString(form) // a longer path that only starts like the Workspace
				}
				t = t[end:]
			}
			b.WriteString(t)
			t = b.String()
		}
	}
	return t
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
