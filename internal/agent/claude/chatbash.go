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
// it is expanded by the shell ($, a backtick), whether any of it was
// quoted, and whether any of it was not.
type sword struct {
	text    string
	dynamic bool
	quoted  bool
	bare    bool
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
// one (&&, ||, ;, a new line, |, &, ( and ), and $( <( >( opening a
// substitution), the here-documents it reads, and whether its standard
// output last went to a named file.
type scmd struct {
	words  []sword
	ops    []string
	docs   []*heredoc
	toFile bool
}

// splitShell splits a command into simple commands, and those into
// words. A # starting a word comments out the rest of its line. ok is
// false for an unbalanced quote, or a here-document whose delimiter is
// missing, never closes, or is one the shell would read differently
// ($'EOF', "E\"OF").
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
	// redirect reads a redirection operator starting at i and returns
	// where it ends. Its target is the next word, kept a word; >& and <&
	// name a descriptor, >| overrides noclobber, and <( and >( open a
	// process substitution. stdout is a redirection of standard output.
	redirect := func(i int, stdout bool) int {
		dup := false
		for strings.ContainsRune("<>&|", next(i)) {
			i++
			dup = dup || rs[i] == '&'
		}
		if next(i) == '(' {
			i++
			if stdout {
				cur.toFile = false
			}
			flushCmd(string(rs[i-1]) + "(")
			return i
		}
		if stdout {
			cur.toFile = !dup
		}
		return i
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
			in, w.bare = true, true
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
				closed := false
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
						closed = true
						break
					}
					body.WriteString(line + "\n")
				}
				if !closed {
					return nil, false
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
			// &> and &>>: a redirection of both outputs, not a
			// background job.
			flushWord()
			i = redirect(i+1, true)
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
				case c == '$':
					return nil, false // $'EOF' is EOF to the shell
				case c == '\'' || c == '"':
					d.quoted = true
					e := i + 1
					for e < len(rs) && rs[e] != c {
						e++
					}
					if e >= len(rs) || (c == '"' && strings.ContainsRune(string(rs[i+1:e]), '\\')) {
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
			// A descriptor written right before it, as in 2>, is not
			// a word.
			stdout := r == '>'
			if in && !w.quoted && !w.dynamic && strings.Trim(b.String(), "0123456789") == "" {
				stdout = stdout && b.String() == "1"
				w, in = sword{}, false
				b.Reset()
			}
			flushWord()
			i = redirect(i, stdout)
		case r == '$' && next(i) == '(':
			w.dynamic, in = true, true
			b.WriteRune(r)
			i++
			flushCmd("$(")
		case strings.ContainsRune(";&|()", r):
			flushCmd(string(r))
		default:
			if r == '$' || r == '`' {
				w.dynamic = true
			}
			b.WriteRune(r)
			in, w.bare = true, true
		}
	}
	if quote != 0 || len(pending) > 0 {
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
func (p *policy) escapes(cmd string) bool { return p.escapesIn(cmd, []string{p.ws[0]}, false) }

// escapesIn is escapes for a command that may run in any of cwds.
// program is a program for an interpreter that isn't a shell (python -c,
// a here-document fed to node), where quoted text joins up into paths,
// so no quoted part is taken for a pattern.
func (p *policy) escapesIn(cmd string, cwds []string, program bool) bool {
	cmds, ok := splitShell(cmd)
	if !ok {
		return true
	}
	every := cwds        // every directory a command may have run in
	var failed []string  // where a cd that may have failed left the shell
	var outer [][]string // cwds at each open (
	for k, c := range cmds {
		for _, op := range c.ops {
			switch op {
			case ";", "\n", "||":
				cwds, failed = union(cwds, failed), nil
			case "(", "$(", "<(", ">(":
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
		h := head(c.words)
		name := ""
		if h < len(c.words) {
			name = filepath.Base(c.words[h].text)
		}
		for _, d := range c.docs {
			if !hereData(c, d) && p.escapesIn(d.body, cwds, program || interpreter(name)) {
				return true
			}
		}
		// An inline program is read as its own command. A shell's is
		// then not a word; another interpreter's is a word too, read
		// as a program (a # there is not a comment).
		progs := map[int]bool{}
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
				shell := !interpreter(filepath.Base(w.text))
				progs[i+1] = shell
				if prog := c.words[i+1]; prog.dynamic || p.escapesIn(prog.text, cwds, program || !shell) {
					return true
				}
				if j != h {
					break
				}
			}
		}
		for _, cwd := range cwds {
			for i, w := range c.words {
				shell, prog := progs[i]
				if shell || i > h && separator(name, c.words[i-1].text, w.text) {
					continue
				}
				if p.wordEscapes(cwd, w, program || prog) {
					return true
				}
			}
		}
		switch name {
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
// writes it to a named file, so no pipe, substitution or shell reads it,
// and the shell runs nothing inside it.
func hereData(c *scmd, d *heredoc) bool {
	h := head(c.words)
	if h >= len(c.words) || !oneOf(filepath.Base(c.words[h].text), []string{"cat", "tee"}) || !c.toFile {
		return false
	}
	return d.quoted || (!strings.Contains(d.body, "$(") && !strings.Contains(d.body, "`"))
}

// interpreter is a program that reads code and isn't a shell.
func interpreter(name string) bool {
	return inline[name] != nil && !oneOf(name, []string{"sh", "bash", "zsh", "dash", "ksh", "fish"})
}

// delimiters are the flags whose value is a field delimiter.
var delimiters = map[string][]string{
	"cut": {"-d", "--delimiter"}, "paste": {"-d", "--delimiters"},
	"awk": {"-F"}, "gawk": {"-F", "--field-separator"}, "mawk": {"-F"}, "nawk": {"-F"},
	"sort": {"-t", "--field-separator"}, "join": {"-t"}, "column": {"-s", "-t"},
}

// separator reports whether a lone / is a delimiter, not the root: an
// argument of tr, or the value of a delimiter flag (cut -d, awk -F,
// sort -t).
func separator(name, prev, w string) bool {
	if w == "" || strings.Trim(w, "/") != "" {
		return false
	}
	return name == "tr" || oneOf(prev, delimiters[name])
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
// Workspace, read from cwd. A URL is not a path, except a file:// one or
// one with a .. in it (x://../.. is a path through a directory x:).
//
// In a word quoted whole, a /-prefixed part is a path only when its
// first component exists: '/api/v1' and '/^func/' are patterns,
// '/etc/x' is a path. Nothing is there to read, so long as the part
// can't be joined to more text: not in a program, not with a $ or a
// glob, and not where the word holds quotes of its own, as in awk's
// system("cat /e" "tc/passwd").
func (p *policy) wordEscapes(cwd string, sw sword, program bool) bool {
	w := sw.text
	pattern := sw.quoted && !sw.bare && !sw.dynamic && !program && !strings.ContainsAny(w, "'\"*?[")
	parts := strings.FieldsFunc(w, func(r rune) bool { return strings.ContainsRune(" '\"(),;=<>|&`[]", r) })
	for _, t := range parts {
		if m := urlRE.FindStringSubmatch(t); m != nil && !hasDotDot(t) {
			if !strings.EqualFold(m[1], "file") {
				continue
			}
			t = t[len(m[0])-1:] // file:///etc/x is /etc/x
		}
		for _, t := range strings.Split(t, ":") {
			if p.partEscapes(cwd, t, pattern) {
				return true
			}
		}
	}
	return false
}

func (p *policy) partEscapes(cwd, t string, pattern bool) bool {
	switch {
	case t == "":
		return false
	case strings.HasPrefix(t, "~"), strings.HasPrefix(t, "$HOME"), strings.HasPrefix(t, "${HOME}"):
		return true
	case strings.HasPrefix(t, "/"):
		if pattern {
			first := "/" + strings.SplitN(strings.TrimLeft(filepath.Clean(t), "/"), "/", 2)[0]
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
