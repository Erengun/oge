package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// wrappers run the command that follows them; a cd or eval after one is
// still a cd or eval.
var wrappers = map[string]bool{"env": true, "command": true, "builtin": true, "exec": true, "nice": true,
	"nohup": true, "time": true, "timeout": true, "stdbuf": true, "sudo": true, "doas": true}

// sword is one shell word: its text with quotes removed, whether any of
// it is expanded by the shell ($, a backtick), and whether any of it was
// quoted.
type sword struct {
	text    string
	dynamic bool
	quoted  bool
}

// heredoc is a here-document's body; quoted is a quoted delimiter, so
// the shell expands nothing in it.
type heredoc struct {
	delim  string
	dash   bool
	quoted bool
	body   string
}

// scmd is one simple command, the operators between it and the previous
// one (&&, ||, ;, a new line, |, &, ( and )), and the here-documents it
// reads.
type scmd struct {
	words []sword
	ops   []string
	docs  []*heredoc
}

// splitShell splits a command into simple commands, and those into
// words. A # starting a word comments out the rest of its line. ok is
// false for an unbalanced quote or a here-document with no delimiter.
func splitShell(cmd string) (cmds []*scmd, ok bool) {
	cur := &scmd{}
	var ops []string
	var pending []*heredoc
	var w sword
	var b strings.Builder
	in := false
	var quote rune
	flushWord := func() {
		if in {
			w.text = b.String()
			cur.words = append(cur.words, w)
		}
		w, in = sword{}, false
		b.Reset()
	}
	flushCmd := func(op string) {
		flushWord()
		if len(cur.words) > 0 || len(cur.docs) > 0 {
			cur.ops = ops
			cmds = append(cmds, cur)
			cur, ops = &scmd{}, nil
		}
		ops = append(ops, op)
	}
	rs := []rune(cmd)
	next := func(i int) rune {
		if i+1 < len(rs) {
			return rs[i+1]
		}
		return 0
	}
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
			quote, in, w.quoted = r, true, true
		case r == '\\' && i+1 < len(rs):
			i++
			b.WriteRune(rs[i])
			in = true
		case r == '#' && !in:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case r == ' ' || r == '\t':
			flushWord()
		case r == '\n':
			flushCmd("\n")
			// Here-document bodies follow the line that opened them.
			j := i + 1
			for _, d := range pending {
				var body strings.Builder
				for j < len(rs) {
					k := j
					for k < len(rs) && rs[k] != '\n' {
						k++
					}
					line := string(rs[j:k])
					j = k + 1
					check := line
					if d.dash {
						check = strings.TrimLeft(line, "\t")
					}
					if check == d.delim {
						break
					}
					body.WriteString(line + "\n")
				}
				d.body = body.String()
			}
			if len(pending) > 0 {
				pending = nil
				i = j - 1
			}
		case r == '&' && next(i) == '&':
			i++
			flushCmd("&&")
		case r == '|' && next(i) == '|':
			i++
			flushCmd("||")
		case r == '|' && next(i) == '&':
			i++
			flushCmd("|")
		case r == ';' && next(i) == ';':
			i++
			flushCmd(";")
		case r == '&' && next(i) == '>':
			// &> and &>>: a redirection, not a background job.
			flushWord()
		case r == '<' && next(i) == '<' && (i+2 >= len(rs) || rs[i+2] != '<'):
			flushWord()
			i += 2
			d := &heredoc{}
			if i < len(rs) && rs[i] == '-' {
				d.dash = true
				i++
			}
			for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
				i++
			}
			var dl strings.Builder
			for ; i < len(rs) && !strings.ContainsRune(" \t\n;&|()<>", rs[i]); i++ {
				switch c := rs[i]; {
				case c == '\'' || c == '"':
					d.quoted = true
					e := i + 1
					for e < len(rs) && rs[e] != c {
						e++
					}
					if e >= len(rs) {
						return nil, false
					}
					dl.WriteString(string(rs[i+1 : e]))
					i = e
				case c == '\\' && i+1 < len(rs):
					d.quoted = true
					i++
					dl.WriteRune(rs[i])
				default:
					dl.WriteRune(c)
				}
			}
			i--
			if d.delim = dl.String(); d.delim == "" {
				return nil, false
			}
			cur.docs = append(cur.docs, d)
			pending = append(pending, d)
		case r == '<' || r == '>':
			// A redirection's target is the next word; keep it a word.
			// >& and <& name a descriptor, >| overrides noclobber.
			flushWord()
			for strings.ContainsRune("<>&|", next(i)) {
				i++
			}
		case strings.ContainsRune(";&|()", r):
			flushCmd(string(r))
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
	flushCmd("")
	return cmds, true
}

// escapes reports whether a Chat's Bash command reaches outside the
// Workspace: an absolute path outside it and the toolchain, ~ or $HOME,
// a .. that leaves it, a cd out of it, an inline program (sh -c,
// python -c, eval) that is dynamic or names such a path, or a
// here-document that is a program and names one.
//
// It follows where each command runs: a cd joined by && or into an
// existing directory moves the working directory, one that may fail
// leaves both possible, and one in a subshell or a pipeline moves
// nothing after it.
func (p *policy) escapes(cmd string) bool {
	cmds, ok := splitShell(cmd)
	if !ok {
		return true
	}
	cwds := []string{p.ws[0]}
	every := cwds        // every directory a command may have run in
	var failed []string  // where a cd that may have failed left the shell
	var outer [][]string // cwds at each open (
	for k, c := range cmds {
		for _, op := range c.ops {
			switch op {
			case ";", "\n", "||":
				cwds, failed = union(cwds, failed), nil
			case "(":
				outer = append(outer, cwds)
			case ")":
				// A subshell's cd doesn't leave it.
				if n := len(outer); n > 0 {
					cwds, outer = union(outer[n-1], failed), outer[:n-1]
				} else {
					cwds = every
				}
				failed = nil
			}
		}
		for _, d := range c.docs {
			if !p.hereData(c, cmds, k, d) && p.escapes(d.body) {
				return true
			}
		}
		h := head(c.words)
		for _, cwd := range cwds {
			for i, w := range c.words {
				if i > h && h < len(c.words) && separator(c.words[h].text, c.words[i-1].text, w.text) {
					continue
				}
				if p.wordEscapes(cwd, w) {
					return true
				}
			}
		}
		for j, w := range c.words {
			// An interpreter heads the command, or sits further in it
			// (xargs sh -c, find -exec sh -c): there, only the flag right
			// after it counts.
			flags := inline[filepath.Base(w.text)]
			if flags == nil {
				continue
			}
			for i := j + 1; i < len(c.words); i++ {
				if !oneOf(c.words[i].text, flags) {
					if j != h {
						break
					}
					continue
				}
				if i+1 >= len(c.words) {
					if j != h {
						break
					}
					return true // a flag with no program
				}
				if prog := c.words[i+1]; prog.dynamic || p.escapes(prog.text) {
					return true
				}
				if j != h {
					break
				}
			}
		}
		if h >= len(c.words) {
			continue
		}
		switch name := filepath.Base(c.words[h].text); name {
		case "cd", "pushd":
			args := c.words[h+1:]
			for len(args) > 0 && oneOf(args[0].text, []string{"-L", "-P", "-e", "-@", "--"}) {
				args = args[1:]
			}
			if len(args) == 0 || args[0].dynamic || args[0].text == "-" || strings.HasPrefix(args[0].text, "+") {
				return true // home, the last directory, or somewhere the shell decides
			}
			var to []string
			sure := true
			for _, cwd := range cwds {
				d := args[0].text
				if !filepath.IsAbs(d) {
					d = filepath.Join(cwd, d)
				}
				d = filepath.Clean(d)
				if !under(resolve(d), p.ws) && !under(d, p.ws) {
					return true
				}
				if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
					sure = false
				}
				to = union(to, []string{d})
			}
			every = union(every, to)
			piped := oneOf("|", c.ops) || oneOf("&", c.ops) ||
				(k+1 < len(cmds) && (oneOf("|", cmds[k+1].ops) || oneOf("&", cmds[k+1].ops)))
			switch {
			case piped:
				// A cd in a pipeline or in the background runs in a
				// subshell: the shell stays where it was.
			case sure:
				cwds = to
			default:
				failed = union(failed, cwds)
				cwds = to
			}
		case "popd":
			cwds = every
		case "eval", "source", ".":
			return true // a program the policy can't read
		}
	}
	return false
}

// head is the index of a command's name, after any assignments and any
// wrapper (env, nice, timeout ...) with its flags.
func head(ws []sword) int {
	i := 0
	for i < len(ws) {
		t := ws[i].text
		switch {
		case isAssign(t):
			i++
		case wrappers[filepath.Base(t)]:
			i++
			for i < len(ws) && (strings.HasPrefix(ws[i].text, "-") || isAssign(ws[i].text) || isDuration(ws[i].text)) {
				i++
			}
		default:
			return i
		}
	}
	return i
}

var (
	assignRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	durationRE = regexp.MustCompile(`^[0-9.]+[smhd]?$`)
	urlRE      = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://`)
)

func isAssign(t string) bool   { return assignRE.MatchString(t) }
func isDuration(t string) bool { return durationRE.MatchString(t) }

// hereData reports whether a here-document is only data: cat or tee
// writes it out, nothing reads it as a program, and the shell runs
// nothing inside it.
func (p *policy) hereData(c *scmd, cmds []*scmd, k int, d *heredoc) bool {
	h := head(c.words)
	if h >= len(c.words) || !oneOf(filepath.Base(c.words[h].text), []string{"cat", "tee"}) {
		return false
	}
	if k+1 < len(cmds) && oneOf("|", cmds[k+1].ops) {
		return false // piped on, perhaps to a shell
	}
	return d.quoted || (!strings.Contains(d.body, "$(") && !strings.Contains(d.body, "`"))
}

// separator reports whether a lone / is a delimiter, not the root: an
// argument of tr, or the value of cut -d, awk -F or sort -t.
func separator(name, prev, w string) bool {
	if w == "" || strings.Trim(w, "/") != "" {
		return false
	}
	return filepath.Base(name) == "tr" || oneOf(prev, []string{"-d", "-F", "-t", "--delimiter", "--field-separator"})
}

func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, x := range b {
		if !oneOf(x, out) {
			out = append(out, x)
		}
	}
	return out
}

// wordEscapes reports whether any path-like part of a word leaves the
// Workspace, read from cwd. A URL is not a path, except a file:// one.
// In a quoted word, a /-prefixed part is a path only when its first
// component exists: '/api/v1' and '/^func/' are patterns, '/etc/x' is a
// path.
func (p *policy) wordEscapes(cwd string, sw sword) bool {
	w := sw.text
	parts := strings.FieldsFunc(w, func(r rune) bool { return strings.ContainsRune(" '\"(),;=<>|&`[]\n\t", r) })
	for _, t := range parts {
		if m := urlRE.FindStringSubmatch(t); m != nil {
			if !strings.EqualFold(m[1], "file") {
				continue
			}
			t = t[len(m[0])-1:] // file:///etc/x is /etc/x
		}
		for _, t := range strings.Split(t, ":") {
			if p.partEscapes(cwd, t, sw.quoted) {
				return true
			}
		}
	}
	return false
}

func (p *policy) partEscapes(cwd, t string, quoted bool) bool {
	switch {
	case t == "":
		return false
	case strings.HasPrefix(t, "~"), strings.HasPrefix(t, "$HOME"), strings.HasPrefix(t, "${HOME}"):
		return true
	case strings.HasPrefix(t, "/"):
		if quoted {
			first := "/" + strings.SplitN(strings.TrimLeft(t, "/"), "/", 2)[0]
			if _, err := os.Lstat(first); err != nil {
				return false
			}
		}
		return !p.contained(filepath.Clean(t))
	case hasDotDot(t):
		return !p.contained(filepath.Join(cwd, t))
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
