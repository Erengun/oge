package claude

import (
	"regexp"
	"sort"
	"strings"
)

// shape is the positive, known-safe form of one pre-authorised command
// (#44): the flags it may take, and how its other words are read. Anything
// not listed is not pre-authorised.
type shape struct {
	bools map[string]bool // flags without a value
	vals  map[string]bool // flags with a value that isn't a path
	paths map[string]bool // flags whose value is a path
	// boolRe also accepts a flag, such as ls's combined short flags.
	boolRe *regexp.Regexp
	// writes: the command may write the paths it names (gofmt -w, go
	// build -o), so none of them may be under .git.
	writes func(flags map[string]bool) bool
	// patterns are value flags that may hold a quoted find pattern.
	patterns map[string]bool
	// valueOK checks a value flag's value.
	valueOK func(flag, val string) bool
}

func set(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x] = true
	}
	return m
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// bashShapes are the pre-authorised commands, by their first words.
var bashShapes = map[string]shape{
	"go test": {
		bools: set("-v", "-race", "-short", "-json", "-cover", "-failfast"),
		vals:  set("-run", "-skip", "-count", "-timeout", "-tags"),
	},
	"go build": {
		bools:  set("-v", "-race", "-trimpath"),
		vals:   set("-tags"),
		paths:  set("-o"),
		writes: func(map[string]bool) bool { return true },
	},
	"go vet": {
		bools: set("-v"),
		vals:  set("-tags"),
	},
	"gofmt": {
		bools:  set("-l", "-d", "-s", "-w"),
		writes: func(f map[string]bool) bool { return f["-w"] },
	},
	"ls": {
		boolRe: regexp.MustCompile(`^-[laAR1hdtSrF]+$`),
	},
	"cat": {
		bools: set("-n"),
	},
	"find": {
		bools:    set("-print"),
		vals:     set("-name", "-iname", "-path", "-type", "-maxdepth", "-mindepth"),
		paths:    set("-newer"),
		patterns: set("-name", "-iname", "-path"),
		valueOK: func(flag, val string) bool {
			switch flag {
			case "-type":
				return val == "f" || val == "d" || val == "l"
			case "-maxdepth", "-mindepth":
				return digits.MatchString(val)
			}
			return true
		},
	},
	"git diff": {
		bools: set("--stat", "--name-only", "--name-status", "--cached", "--staged", "--no-color", "--numstat",
			"--shortstat", "--no-ext-diff", "--patch", "-p", "--minimal", "--"),
		boolRe:  regexp.MustCompile(`^-U[0-9]+$`),
		vals:    set("--unified", "--color"),
		valueOK: func(flag, val string) bool { return flag != "--color" || val == "never" },
	},
	"git status": {
		bools:   set("-s", "--short", "--porcelain", "-b", "--branch", "-uno", "-unormal", "-uall", "--"),
		vals:    set("--untracked-files"),
		valueOK: func(_, val string) bool { return val == "no" || val == "normal" || val == "all" },
	},
}

// bash classifies a shell command: exactly a Check command, or one
// simple command in a pre-authorised shape on contained paths.
func (p *policy) bash(cmd string) verdict {
	for _, c := range p.checks {
		if cmd == strings.TrimSpace(c) {
			return vOK
		}
	}
	for _, priv := range p.private {
		if strings.Contains(cmd, priv) {
			return vOutside
		}
	}
	words, ok := lex(cmd)
	if !ok || len(words) == 0 {
		return vGrey
	}
	name := words[0].text
	if len(words) > 1 && (name == "go" || name == "git") {
		name += " " + words[1].text
	}
	sh, ok := bashShapes[name]
	if !ok || words[0].quoted || words[0].glob {
		return vGrey
	}
	args := words[strings.Count(name, " ")+1:]
	return p.shape(sh, args)
}

func (p *policy) shape(sh shape, args []word) verdict {
	flags := map[string]bool{}
	var paths []string
	v := vOK
	afterDash := false
	for i := 0; i < len(args); i++ {
		w := args[i]
		// Quotes don't stop a word being a flag: the shell strips them.
		if afterDash || !strings.HasPrefix(w.text, "-") {
			if w.glob {
				return vGrey
			}
			paths = append(paths, w.text)
			continue
		}
		if w.text == "--" && sh.bools["--"] {
			afterDash = true
			continue
		}
		if w.glob {
			return vGrey
		}
		flag, val, hasVal := strings.Cut(w.text, "=")
		switch {
		case !hasVal && (sh.bools[flag] || (sh.boolRe != nil && sh.boolRe.MatchString(flag))):
			flags[flag] = true
		case sh.vals[flag] || sh.paths[flag]:
			if !hasVal {
				if i+1 >= len(args) {
					return vGrey
				}
				i++
				if args[i].glob && !sh.patterns[flag] {
					return vGrey
				}
				val = args[i].text
			}
			if sh.valueOK != nil && !sh.valueOK(flag, val) {
				return vGrey
			}
			flags[flag] = true
			if sh.paths[flag] {
				paths = append(paths, val)
			}
		default:
			return vGrey // not in the command's safe set
		}
	}
	write := sh.writes != nil && sh.writes(flags)
	for _, path := range paths {
		v = worst(v, p.path(path, write))
	}
	return v
}

// word is one shell word, with how it was written.
type word struct {
	text   string
	quoted bool // some of it was in quotes
	glob   bool // it holds a quoted * or ? (literal, for find's patterns)
}

// safe are the only characters a command may hold outside quotes, besides
// the spaces between words.
func safe(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r)
}

// lex splits a command into words conservatively. ok is false for
// anything it isn't sure of: any character outside the safe set (so no
// chaining, substitution, redirection, globbing, ~, $, braces or !), an
// unbalanced quote, or a backslash other than one before a space. Inside
// quotes, only safe characters, spaces, and * and ? (marked) may appear.
func lex(s string) (words []word, ok bool) {
	var cur word
	var b strings.Builder
	in := false
	var quote rune
	escaped := false
	flush := func() {
		if in {
			cur.text = b.String()
			words = append(words, cur)
		}
		cur, in = word{}, false
		b.Reset()
	}
	for _, r := range s {
		switch {
		case escaped:
			if r != ' ' {
				return nil, false
			}
			b.WriteRune(r)
			escaped = false
		case quote != 0:
			switch {
			case r == quote:
				quote = 0
			case r == '*' || r == '?':
				cur.glob = true
				b.WriteRune(r)
			case safe(r) || r == ' ':
				b.WriteRune(r)
			default:
				return nil, false
			}
		case r == '\'' || r == '"':
			quote, in, cur.quoted = r, true, true
		case r == '\\':
			escaped, in = true, true
		case r == ' ':
			flush()
		case safe(r):
			b.WriteRune(r)
			in = true
		default:
			return nil, false
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	flush()
	return words, true
}

// describeShapes is the shapes' definition, for the Launch profile hash.
func describeShapes() map[string][]string {
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	d := map[string][]string{}
	for name, sh := range bashShapes {
		var l []string
		l = append(l, "bools:"+strings.Join(keys(sh.bools), ","), "vals:"+strings.Join(keys(sh.vals), ","),
			"paths:"+strings.Join(keys(sh.paths), ","), "patterns:"+strings.Join(keys(sh.patterns), ","))
		if sh.boolRe != nil {
			l = append(l, "re:"+sh.boolRe.String())
		}
		d[name] = l
	}
	return d
}
