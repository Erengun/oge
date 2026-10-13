package claude

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// A Chat's Bash commands are decided by claude's auto mode (D2), so a
// command Öge doesn't pre-authorise still runs if the mode approves it.
// escapes is the hard deny on top: a command that names anything outside
// the Workspace is denied before auto mode or the human sees it. It reads
// the command conservatively; what it can't see (an obfuscated path) is
// bounded by the sandbox's denyRead for reads and by the sandbox for
// writes (ADR-0013 as amended).

// systemReadOK are the absolute directories a Chat command may name
// besides the Workspace: devices and system binaries.
var systemReadOK = []string{"/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr",
	"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/usr/local/bin", "/opt/homebrew/bin"}

// inline are interpreters and the flags that take a program inline.
var inline = map[string][]string{
	"sh": {"-c"}, "bash": {"-c"}, "zsh": {"-c"}, "dash": {"-c"}, "ksh": {"-c"}, "fish": {"-c"},
	"python": {"-c"}, "python3": {"-c"}, "python2": {"-c"},
	"node": {"-e", "--eval", "-p", "--print"}, "perl": {"-e", "-E"}, "ruby": {"-e"}, "php": {"-r"},
	"osascript": {"-e"},
}

// sword is one shell word: its text with quotes removed, and whether any
// of it is expanded by the shell ($, a backtick).
type sword struct {
	text    string
	dynamic bool
}

// splitShell splits a command into simple commands at ; & | ( ) and new
// lines, and those into words. ok is false for an unbalanced quote.
func splitShell(cmd string) (cmds [][]sword, ok bool) {
	var cur []sword
	var w sword
	var b strings.Builder
	in := false
	var quote rune
	flushWord := func() {
		if in {
			w.text = b.String()
			cur = append(cur, w)
		}
		w, in = sword{}, false
		b.Reset()
	}
	flushCmd := func() {
		flushWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
		}
		cur = nil
	}
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs):
				i++
				b.WriteRune(rs[i])
			default:
				if r == '$' || r == '`' {
					w.dynamic = true
				}
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			b.WriteRune(rs[i])
			in = true
		case r == ' ' || r == '\t':
			flushWord()
		case strings.ContainsRune(";&|()\n", r):
			flushCmd()
		case r == '<' || r == '>':
			// A redirection's target is the next word; keep it a word.
			flushWord()
		default:
			if r == '$' || r == '`' {
				w.dynamic = true
			}
			b.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	flushCmd()
	return cmds, true
}

// escapes reports whether a Chat's Bash command reaches outside the
// Workspace: an absolute path outside it and the toolchain, ~ or $HOME,
// a .. that leaves it, a cd out of it, or an inline program (sh -c,
// python -c, eval) that is dynamic or names such a path.
func (p *policy) escapes(cmd string) bool {
	cmds, ok := splitShell(cmd)
	if !ok {
		return true
	}
	cwd := p.ws[0]
	for _, c := range cmds {
		name := filepath.Base(c[0].text)
		for _, w := range c {
			if p.wordEscapes(cwd, w.text) {
				return true
			}
		}
		switch {
		case name == "cd" || name == "pushd":
			if len(c) == 1 || c[1].dynamic {
				return true // home, or somewhere the shell decides
			}
			d := c[1].text
			if !filepath.IsAbs(d) {
				d = filepath.Join(cwd, d)
			}
			if cwd = filepath.Clean(d); !under(resolve(cwd), p.ws) && !under(cwd, p.ws) {
				return true
			}
		case name == "eval" || name == "source" || name == ".":
			return true // a program the policy can't read
		case inline[name] != nil:
			for i, w := range c[1:] {
				if !oneOf(w.text, inline[name]) {
					continue
				}
				if i+2 >= len(c) {
					return true
				}
				prog := c[i+2]
				if prog.dynamic || p.escapes(prog.text) {
					return true
				}
			}
		}
	}
	return false
}

// wordEscapes reports whether any path-like part of a word leaves the
// Workspace, read from cwd.
func (p *policy) wordEscapes(cwd, w string) bool {
	parts := strings.FieldsFunc(w, func(r rune) bool { return strings.ContainsRune(" '\"(),;:=<>|&`[]", r) })
	for _, t := range parts {
		switch {
		case strings.HasPrefix(t, "~"), strings.HasPrefix(t, "$HOME"), strings.HasPrefix(t, "${HOME}"):
			return true
		case strings.HasPrefix(t, "/"):
			if !p.contained(filepath.Clean(t)) {
				return true
			}
		case hasDotDot(t):
			if !p.contained(filepath.Join(cwd, t)) {
				return true
			}
		}
	}
	return false
}

// contained is an absolute path inside the Workspace or a directory a
// Chat command may read, and never in Öge's private state.
func (p *policy) contained(path string) bool {
	if p.isPrivate(path) {
		return false
	}
	for _, x := range []string{path, resolve(path)} {
		if under(x, p.ws) || under(x, p.readOK) || under(x, systemReadOK) {
			return true
		}
	}
	return false
}

// toolchain are the toolchain directories a Chat's commands read outside
// the Workspace: Go's root, path and module cache. get reads the parent
// environment.
func toolchain(get func(string) string) []string {
	var out []string
	add := func(d string) {
		if filepath.IsAbs(d) {
			out = append(out, withReal(d)...)
		}
	}
	for _, v := range []string{"GOROOT", "GOPATH", "GOMODCACHE"} {
		for _, d := range filepath.SplitList(get(v)) {
			add(d)
		}
	}
	if home := get("HOME"); filepath.IsAbs(home) && get("GOPATH") == "" {
		add(filepath.Join(home, "go"))
	}
	if g, err := exec.LookPath("go"); err == nil {
		if r, err := filepath.EvalSymlinks(g); err == nil {
			add(filepath.Dir(filepath.Dir(r))) // <root>/bin/go
		}
	}
	return out
}
