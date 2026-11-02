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
	// slashOK is set on a word Öge has read as a script or message, where
	// a part made only of / is an operator or a delimiter, not the root.
	slashOK bool
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
// output last went to a named file (not a descriptor or a device).
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
	target := false // the next word is where standard output goes
	var quote rune
	flushWord := func() {
		if in {
			w.text = b.String()
			cur.words = append(cur.words, w)
			if target {
				t := filepath.Clean(w.text)
				cur.toFile = !w.dynamic && (t == "/dev/null" || (t != "/dev" && !strings.HasPrefix(t, "/dev/")))
				target = false
			}
		}
		w, in = sword{}, false
		b.Reset()
	}
	flushCmd := func(op string) {
		flushWord()
		target = false
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
	// process substitution. stdout is a redirection of standard output;
	// it goes to a named file only when its target isn't a device
	// (/dev/stdout, /dev/fd/1) or dynamic.
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
			cur.toFile, target = false, !dup
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
func (p *policy) escapes(cmd string) bool { return p.escapesIn(cmd, []string{p.ws[0]}, notProg) }

// progKind says what a text is read as. In a quotedProg (python) a
// string literal needs quotes, so a bare / is an operator. In a codeProg
// (node, perl, ruby, php, osascript) q(/), %q(/) and a JS regexp literal
// String(/ /) are literals without quotes, so a lone / may be the root.
type progKind int

const (
	notProg progKind = iota
	codeProg
	quotedProg
)

// kindOf is how the program an interpreter runs is read.
func kindOf(name string) progKind {
	switch {
	case !interpreter(name):
		return notProg
	case strings.HasPrefix(name, "python"):
		return quotedProg
	}
	return codeProg
}

// or is k, or o when k is not a program.
func (k progKind) or(o progKind) progKind {
	if k == notProg {
		return o
	}
	return k
}

// escapesIn is escapes for a command that may run in any of cwds.
// program is a program for an interpreter that isn't a shell (python -c,
// a here-document fed to node), where quoted text joins up into paths,
// so no quoted part is taken for a pattern.
func (p *policy) escapesIn(cmd string, cwds []string, program progKind) bool {
	cmds, ok := splitShell(cmd)
	if !ok {
		return true
	}
	every := cwds        // every directory a command may have run in
	var failed []string  // where a cd that may have failed left the shell
	var outer [][]string // cwds at each open (
	var opened []string  // the op that opened each
	for k, c := range cmds {
		for _, op := range c.ops {
			switch op {
			case ";", "\n", "||":
				cwds, failed = union(cwds, failed), nil
			case "(", "$(", "<(", ">(":
				outer, opened = append(outer, cwds), append(opened, op)
			case ")":
				// A subshell's cd doesn't leave it.
				if n := len(outer); n > 0 {
					cwds, outer, opened = union(outer[n-1], failed), outer[:n-1], opened[:n-1]
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
		// A here-document piped on, or inside a substitution, may reach
		// a shell whatever cat does with it.
		bare := !oneOf("$(", opened) && !oneOf("<(", opened) && !oneOf(">(", opened) &&
			(k+1 >= len(cmds) || !oneOf("|", cmds[k+1].ops))
		for _, d := range c.docs {
			if !(bare && hereData(c, d)) && p.escapesIn(d.body, cwds, kindOf(name).or(program)) {
				return true
			}
		}
		// An inline program is read as its own command. A shell's is
		// then not a word; another interpreter's is a word too, read
		// as a program (a # there is not a comment).
		progs := map[int]progKind{}
		isProg := map[int]bool{}
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
				kind := kindOf(filepath.Base(w.text))
				progs[i+1], isProg[i+1] = kind, true
				if prog := c.words[i+1]; prog.dynamic || p.escapesIn(prog.text, cwds, kind.or(program)) {
					return true
				}
				if j != h {
					break
				}
			}
		}
		scripts := map[int]bool{}
		if !oneOf("$(", opened) && !oneOf("<(", opened) && !oneOf(">(", opened) && !resplits(cmds, k, len(outer) > 0) {
			scripts = scriptWords(name, c.words, h)
		}
		for _, cwd := range cwds {
			for i, w := range c.words {
				kind, prog := progs[i], isProg[i]
				if prog && kind == notProg || i > h && separator(name, c.words[i-1].text, w.text) {
					continue
				}
				if scripts[i] {
					w = scriptText(name, w)
				} else if prog && kind == quotedProg && !w.dynamic {
					// A python or node program is read again as code,
					// where a quoted / is told from an operator.
					w.text = maskOutsideStrings(w.text, `"'`)
				}
				if p.wordEscapes(cwd, w, kind.or(program)) {
					return true
				}
			}
		}
		// builtin and command run the builtin that follows.
		args := c.words[min(h+1, len(c.words)):]
		if oneOf(name, []string{"builtin", "command"}) && len(args) > 0 {
			name, args = filepath.Base(args[0].text), args[1:]
		}
		if name == "jq" {
			// -L/dir and --library-path=/dir hold their path in the flag.
			for _, a := range args {
				if (strings.HasPrefix(a.text, "-L") && len(a.text) > 2) || strings.HasPrefix(a.text, "--library-path=") {
					return true
				}
			}
		}
		switch name {
		case "cd", "pushd":
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
		case "eval":
			return true // a program the policy can't read
		case "source", ".":
			if !p.sourceOK(cwds, args) {
				return true // a program the policy can't read
			}
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
	jqImportRE = regexp.MustCompile(`\b(import|include|search)\b`)
	urlRE      = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://`)
)

func isAssign(t string) bool   { return assignRE.MatchString(t) }
func isDuration(t string) bool { return durationRE.MatchString(t) }

// hereData reports whether a here-document is only data: cat or tee
// writes it to a named file, and the shell runs nothing inside it. The
// caller checks that no pipe or substitution reads the command.
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
	"sort": {"-t", "--field-separator"}, "join": {"-t"}, "column": {"-s"},
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
// can't be joined to more text: not in a program, not with a $, a glob
// or an =, and not where the word holds quotes of its own, as in awk's
// system("cat /e" "tc/passwd").
func (p *policy) wordEscapes(cwd string, sw sword, kind progKind) bool {
	program := kind != notProg
	w := sw.text
	pattern := sw.quoted && !sw.bare && !sw.dynamic && !program && !strings.ContainsAny(w, "'\"*?[=")
	parts := strings.FieldsFunc(w, func(r rune) bool { return strings.ContainsRune(" '\"(),;=<>|&`[]", r) })
	for _, t := range parts {
		if sw.slashOK && t != "" && strings.Trim(t, "/") == "" {
			continue
		}
		if m := urlRE.FindStringSubmatch(t); m != nil && !hasDotDot(t) {
			if !strings.EqualFold(m[1], "file") {
				continue
			}
			t = t[len(m[0])-1:] // file:///etc/x is /etc/x
		}
		for _, t := range strings.Split(t, ":") {
			if kind == quotedProg && !sw.quoted && !sw.dynamic && t != "" && strings.Trim(t, "/") == "" {
				continue // an operator in code: 10 / 2, 7 // 2, x /= 2
			}
			if sw.slashOK && t != "" && strings.Trim(t, "/") == "" {
				continue
			}
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

var awks = []string{"awk", "gawk", "mawk", "nawk"}

// textSinks are the commands that only read text and print or count it.
// What a script prints may reach one of them; anything else (xargs, a
// shell, tar -T -, a read loop, tee, a substitution) may use it as a path
// or a command.
var textSinks = []string{"sort", "uniq", "wc", "head", "tail", "grep", "egrep", "fgrep", "rg",
	"cut", "tr", "cat", "less", "more"}

// resplits reports whether what command k prints may reach something
// other than a pure-text sink: a later command of its pipeline, or of
// the whole command when it sits in a group, that is not in textSinks (or
// is one given a flag that reads names from stdin or runs a program), a
// read loop, a group, or a substitution.
func resplits(cmds []*scmd, k int, grouped bool) bool {
	for m := k + 1; m < len(cmds); m++ {
		piped := oneOf("|", cmds[m].ops) || oneOf(">(", cmds[m].ops) || oneOf("<(", cmds[m].ops)
		// Output given to >( or <( reaches that command whatever
		// separates them.
		if !piped {
			if grouped {
				continue
			}
			return false
		}
		ws := cmds[m].words
		h := head(ws)
		if h >= len(ws) {
			continue
		}
		if !oneOf(filepath.Base(ws[h].text), textSinks) {
			return true
		}
		for _, w := range ws[h+1:] {
			if strings.HasPrefix(w.text, "--") && (strings.Contains(w.text, "files0") || strings.HasPrefix(w.text, "--comp")) {
				return true
			}
		}
	}
	return false
}

// scriptWords are the indexes of the words of a command that hold a
// script or a message: the script of sed, jq and awk, and the message
// of git commit. A / in them that stands alone is an operator or a
// delimiter. Every other word, the files they read included, is read as
// a path.
func scriptWords(name string, ws []sword, h int) map[int]bool {
	out := map[int]bool{}
	if h >= len(ws) {
		return out
	}
	args := ws[h+1:]
	at := func(i int) int { return h + 1 + i }
	switch {
	case name == "sed":
		scripted := false // -e or -f gave the script
		unsure := false   // a script file or a form not read: excuse nothing
		pos := -1
		var texts []string
	scan:
		for i := 0; i < len(args); i++ {
			t := args[i].text
			switch {
			case t == "--":
				if pos < 0 && !scripted && i+1 < len(args) {
					pos = i + 1
				}
				break scan
			case t == "--expression" || (len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.HasSuffix(t, "e")):
				scripted = true
				if i+1 < len(args) {
					out[at(i+1)] = true
					texts = append(texts, args[i+1].text)
					i++
				}
			case strings.HasPrefix(t, "--expression="):
				scripted = true
				texts = append(texts, strings.TrimPrefix(t, "--expression="))
			case t == "--file" || strings.HasPrefix(t, "--file=") ||
				(len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.Contains(t, "f")):
				unsure = true
				scripted = true
				i++
			case strings.HasPrefix(t, "--") && !knownSedLong(t):
				// getopt_long takes any unambiguous prefix (--e, --expr=,
				// --fi=), so a long option not known to take no script
				// leaves it unread.
				unsure = true
			case t == "-i" && i+1 < len(args) && args[i+1].text == "" && args[i+1].quoted:
				i++ // macOS: the suffix is a word of its own
			case len(t) > 2 && t[0] == '-' && t[1] != '-' && strings.Contains(t, "e"):
				unsure = true // -eSCRIPT: the script is in the word
				scripted = true
			case strings.HasPrefix(t, "-") && t != "-":
			default:
				if pos < 0 && !scripted {
					pos = i
				}
				break scan
			}
		}
		if pos >= 0 {
			out[at(pos)] = true
			texts = append(texts, ws[at(pos)].text)
		}
		// GNU sed joins every script into one program, so an e in any
		// of them runs what another one printed.
		if unsure || sedRuns(strings.Join(texts, "\n")) {
			return map[int]bool{}
		}
		for i := range out {
			if sedRuns(ws[i].text) {
				delete(out, i)
			}
		}
	case name == "jq":
		// -f, --from-file or a cluster holding f (-nf) makes a word, even
		// one after the filter, a program file: nothing is a script.
		for i := 0; i < len(args); i++ {
			t := args[i].text
			switch {
			case t == "--":
				i = len(args)
			case oneOf(t, []string{"--arg", "--argjson", "--slurpfile", "--rawfile"}):
				i += 2
			case oneOf(t, []string{"-L", "--library-path", "--indent"}):
				i++
			case t == "--from-file" || strings.HasPrefix(t, "--from-file=") || t == "--run-tests":
				return out
			case len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.Contains(t, "f"):
				return out
			}
		}
		for i := 0; i < len(args); i++ {
			t := args[i].text
			switch {
			case oneOf(t, []string{"--arg", "--argjson", "--slurpfile", "--rawfile"}):
				i += 2
			case oneOf(t, []string{"-L", "--library-path", "--indent"}):
				i++
			case oneOf(t, []string{"-f", "--from-file"}):
				return out
			case strings.HasPrefix(t, "-") && t != "-":
			default:
				out[at(i)] = true
				return out
			}
		}
	case oneOf(name, awks):
		// Only a flag known not to give the program lets the next word
		// be the script. Any other flag (-fprog, --file=p, -Ep, ...)
		// may make that word an input file.
		for i := 0; i < len(args); i++ {
			t := args[i].text
			switch {
			case t == "--":
				if i+1 < len(args) {
					out[at(i+1)] = true
				}
				return out
			case oneOf(t, []string{"-F", "-v"}):
				i++
			case len(t) > 2 && (strings.HasPrefix(t, "-F") || strings.HasPrefix(t, "-v")) && t[1] != '-':
			case oneOf(t, []string{"-e", "--source"}):
				if i+1 < len(args) {
					out[at(i+1)] = true
				}
				return out
			case strings.HasPrefix(t, "-") && t != "-":
				return out
			default:
				out[at(i)] = true
				return out
			}
		}
	case name == "git":
		// Only the global flags known here may precede commit; any
		// other (--namespace, --attr-source, ...) may take the next
		// word, so nothing is excused.
		commit := false
		for i := 0; i < len(args); i++ {
			t := args[i].text
			switch {
			case !commit && t == "commit":
				commit = true
			case !commit && oneOf(t, []string{"-C", "-c", "--git-dir", "--work-tree"}):
				i++
			case !commit && (oneOf(t, []string{"-p", "--paginate", "--no-pager"}) ||
				strings.HasPrefix(t, "--git-dir=") || strings.HasPrefix(t, "--work-tree=")):
			case !commit:
				return map[int]bool{}
			case t == "--message" || (len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.HasSuffix(t, "m")):
				// -Cm is -C with the value m: every letter before the m
				// must be a flag that takes no value.
				if len(t) > 2 && t[1] != '-' && strings.Trim(t[1:len(t)-1], "asvqneio") != "" {
					return map[int]bool{}
				}
				if i+1 < len(args) {
					out[at(i+1)] = true
					i++
				}
			case strings.HasPrefix(t, "--message="):
				out[at(i)] = true
			}
		}
	}
	return out
}

// scriptText is a script word with a part that is only slashes excused.
// Every other part is read as a path, as it is outside a script: the
// text of a script is not parsed, so nothing is trusted to be a string
// literal, a regexp or a comment. An awk script holding a " can build a
// command or a path from literals (system("cd /; cat etc/x")), so it
// excuses nothing.
func scriptText(name string, w sword) sword {
	if w.dynamic || (oneOf(name, awks) && strings.Contains(w.text, `"`)) {
		return w
	}
	// A jq string literal that is only slashes ("/") is a path for import
	// or a root once consumed, so a slash run touching a " excuses nothing,
	// nor does a program that imports (known false stop: jq -n '"/"').
	if name == "jq" && (strings.Contains(w.text, `/"`) || strings.Contains(w.text, `"/`) || jqImportRE.MatchString(w.text)) {
		return w
	}
	w.slashOK = true
	return w
}

// sourceOK reports whether source or . reads a relative path inside the
// Workspace: a / in it, no flag, no .., no absolute path, no ~, nothing
// expanded.
func (p *policy) sourceOK(cwds []string, args []sword) bool {
	if len(args) == 0 {
		return false
	}
	t := args[0]
	// bash and zsh search PATH for a name with no /, and a leading - is a
	// flag (source -- "$X", source -p $X x.sh).
	if t.dynamic || t.text == "" || strings.HasPrefix(t.text, "-") || !strings.Contains(t.text, "/") || filepath.IsAbs(t.text) || strings.HasPrefix(t.text, "~") ||
		strings.ContainsAny(t.text, "*?[{") || hasDotDot(t.text) {
		return false
	}
	for _, cwd := range cwds {
		full := filepath.Join(cwd, t.text)
		if !under(resolve(full), p.ws) {
			return false
		}
	}
	return true
}

// maskOutsideStrings turns every / outside a string literal into _. A
// literal opens at any of quotes and closes at the same one; a \ skips
// the next character. An unclosed literal runs to the end, so text it
// can't read stays read as a path.
func maskOutsideStrings(text, quotes string) string {
	var b strings.Builder
	var in rune
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case in != 0 && r == '\\' && i+1 < len(rs):
			b.WriteRune(r)
			i++
			b.WriteRune(rs[i])
			continue
		case in == 0 && strings.ContainsRune(quotes, r):
			in = r
		case in == r:
			in = 0
		case r == '/' && in == 0:
			r = '_'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// knownSedLong reports whether a sed long option is spelled out in full
// and does not give a script or a script file.
func knownSedLong(t string) bool {
	name, _, _ := strings.Cut(t, "=")
	if strings.Contains(t, "=") && !oneOf(name, []string{"--in-place", "--line-length"}) {
		return false
	}
	return oneOf(name, []string{"--quiet", "--silent", "--posix", "--regexp-extended", "--separate",
		"--sandbox", "--debug", "--null-data", "--zero-terminated", "--unbuffered",
		"--follow-symlinks", "--binary", "--in-place", "--line-length"})
}

// sedRuns reports whether a sed script may run a command: an e command,
// or an s command with an e flag. It reads the script as sed does, and
// says yes for one it can't read.
func sedRuns(script string) bool {
	rs := []rune(script)
	n := len(rs)
	i := 0
	skipTo := func(delim rune) bool { // past the next unescaped delim
		for i < n {
			r := rs[i]
			i++
			switch {
			case r == '\\' && i < n:
				i++
			case r == delim:
				return true
			}
		}
		return false
	}
	eol := func() {
		for i < n && rs[i] != '\n' {
			i++
		}
	}
	for i < n {
		// An address: numbers, $, ~, +, ,, !, spaces, /re/ and \cREc.
		for i < n {
			r := rs[i]
			switch {
			case r == '/' || r == '\\' && i+1 < n:
				d := r
				i++
				if r == '\\' {
					d = rs[i]
					i++
				}
				if !skipTo(d) {
					return true
				}
				for i < n && strings.ContainsRune("IM", rs[i]) {
					i++
				}
				continue
			case strings.ContainsRune("0123456789$~+,! \t;{}\n", r):
				i++
				continue
			}
			break
		}
		if i >= n {
			return false
		}
		c := rs[i]
		i++
		switch c {
		case 'e':
			return true
		case '#', 'a', 'i', 'c', 'r', 'R', 'w', 'W':
			eol()
		case 'b', 't', 'T', ':':
			// A label ends at ; a newline or a blank (GNU sed 4.3+), and
			// a branch's also at }.
			for i < n && strings.ContainsRune(" \t", rs[i]) {
				i++
			}
			end := ";\n \t"
			if c != ':' {
				end += "}"
			}
			for i < n && !strings.ContainsRune(end, rs[i]) {
				i++
			}
		case 's':
			if i >= n {
				return true
			}
			d := rs[i]
			i++
			closed := skipTo(d)
			if closed {
				closed = skipTo(d)
			}
			if !closed {
				return true
			}
			for i < n && !strings.ContainsRune(";}\n \t", rs[i]) {
				if rs[i] == 'e' {
					return true
				}
				if rs[i] == 'w' {
					eol()
					break
				}
				i++
			}
		case 'y':
			if i >= n {
				return true
			}
			d := rs[i]
			i++
			closed := skipTo(d)
			if closed {
				closed = skipTo(d)
			}
			if !closed {
				return true
			}
		}
	}
	return false
}
