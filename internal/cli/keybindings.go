package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/erengun/oge/internal/pipeline"
)

// The keybindings file (#163): overrides for the action table (#162), in
// keybindings.toml next to the user config, loaded like theme.toml (#175):
// strict, one notice when it is bad (the defaults then stand), re-read when
// the composer opens, never fatal.
//
//	[composer]
//	editor  = "ctrl+g"
//	newline = ["ctrl+j", "alt+enter"]
//	[run]
//	expand = "ctrl+t"
//
// An override replaces that action's keys; the other actions keep theirs.

// keybindingsFile is the file's name in the config directory.
const keybindingsFile = "keybindings.toml"

// defaultActions is the table as shipped.
var defaultActions = actionTable

// maxConfigFile caps a small config file (keybindings.toml, theme.toml).
const maxConfigFile = 64 << 10

// readSmallFile reads a config file that must be a regular file under
// maxConfigFile: a FIFO would block and /dev/zero never ends. A missing file
// is os.ErrNotExist.
func readSmallFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigFile {
		return nil, fmt.Errorf("larger than %d KiB", maxConfigFile>>10)
	}
	return data, nil
}

// keybindingsPath is where the file is, or "" without a config home.
func keybindingsPath(getenv func(string) string) string {
	p := pipeline.UserConfigPath(getenv)
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), keybindingsFile)
}

// Keys the file may never bind (ADR-0015, ADR-0022). A Gate takes every key
// but ctrl+c, esc, enter, backspace and typed text itself, so those stay the
// Gate's; the rest below belong to handlers that are not in the table.
var (
	lockedKeys = map[string]string{
		"ctrl+c":    "cancels; it must always work",
		"esc":       "closes and backs out; it must always work",
		"enter":     "answers a Gate and runs the task",
		"backspace": "edits what a Gate asks you to type",
		"space":     "is typed text, and Gate answers are typed",
		"ctrl+m":    "is enter",
		"ctrl+i":    "is tab",
		"ctrl+[":    "is esc",
	}
	ownedKeys = []string{
		"tab", "up", "down", "left", "right", "home", "end", "pgup", "pgdown", "delete",
		"ctrl+a", "ctrl+e", "ctrl+b", "ctrl+f", "ctrl+k", "ctrl+u", "ctrl+w", "ctrl+y", "ctrl+d", "ctrl+h",
		"ctrl+p", "ctrl+n", "ctrl+z", "ctrl+_", "ctrl+/",
		"alt+b", "alt+f", "alt+d", "alt+backspace",
		"ctrl+left", "ctrl+right", "alt+left", "alt+right",
	}
)

var keyMods = []string{"ctrl+", "alt+", "shift+", "meta+", "hyper+", "super+"}

var namedKeys = []string{
	"enter", "tab", "backspace", "esc", "space", "up", "down", "left", "right", "begin", "find",
	"insert", "delete", "select", "pgup", "pgdown", "home", "end",
}

// validKeyName reports whether s is a name tea.KeyPressMsg.String() can
// produce: modifiers in ctrl, alt, shift, meta, hyper, super order, then a
// named key, f1 to f63, or one character.
func validKeyName(s string) bool {
	rest := s
	for _, m := range keyMods { // in order, each at most once
		rest = strings.TrimPrefix(rest, m)
	}
	// Bubble Tea spells a modified letter in lower case ("ctrl+g").
	if rest != s && len(rest) == 1 && rest[0] >= 'A' && rest[0] <= 'Z' {
		return false
	}
	switch {
	case rest == "":
		return false
	case slices.Contains(namedKeys, rest):
		return true
	case rest[0] == 'f' && len(rest) > 1:
		n, err := strconv.Atoi(rest[1:])
		return err == nil && n >= 1 && n <= 63 && rest[1] != '0' && rest[1] != '+'
	}
	r := []rune(rest)
	return len(r) == 1 && r[0] > ' ' && r[0] != 0x7f
}

// typesText is a key that can carry text: a plain character, space, or those
// with only shift (Shift+Space and Shift+letter arrive with their text on
// terminals that report it). Only ctrl, alt, meta, super, hyper, named and
// function keys are safe to bind. s is a valid name.
func typesText(s string) bool {
	rest := s
	for _, m := range keyMods {
		rest = strings.TrimPrefix(rest, m)
	}
	if rest != "space" && len([]rune(rest)) != 1 {
		return false
	}
	mods := strings.TrimSuffix(s, rest)
	return mods == "" || mods == "shift+"
}

// lockReason is why name can't be bound to the action, or "".
func lockReason(ctx, action, name string) string {
	if slices.Contains(defaultKeys(ctx, action), name) {
		return "" // an action keeps its own default keys
	}
	if why, ok := lockedKeys[name]; ok {
		return name + " " + why + " (locked, ADR-0015)"
	}
	if typesText(name) {
		return name + " types text, so it can't be a shortcut (use ctrl+, alt+ or a function key)"
	}
	if slices.Contains(ownedKeys, name) {
		return name + " is used for editing and navigation, which are not remappable"
	}
	return ""
}

var keyAliases = map[string]string{"escape": "esc", "return": "enter", "pageup": "pgup", "pagedown": "pgdown", "del": "delete", "ins": "insert"}

// canonicalHint suggests the spelling Bubble Tea uses, when there is one.
func canonicalHint(k string) string {
	lower := strings.ToLower(k)
	for from, to := range keyAliases {
		lower = strings.ReplaceAll(lower, from, to)
	}
	if lower != k && validKeyName(lower) {
		return "; write it as " + lower
	}
	return ""
}

func defaultKeys(ctx, action string) []string {
	for _, a := range defaultActions {
		if a.ctx == ctx && a.name == action {
			return a.keys
		}
	}
	return nil
}

type keyOverride struct{ ctx, name string }

// parseKeybindings reads the file strictly. Any Problem means none of it
// applies.
func parseKeybindings(data []byte) (map[keyOverride][]string, []pipeline.Problem) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return nil, []pipeline.Problem{{Key: perr.LastKey, Msg: "not valid TOML"}}
		}
		return nil, []pipeline.Problem{{Msg: "not valid TOML"}}
	}
	var probs []pipeline.Problem
	out := map[keyOverride][]string{}
	for _, ctx := range sortedKeys(raw) {
		sect, ok := raw[ctx].(map[string]any)
		if ctx != ctxComposer && ctx != ctxRun || !ok {
			probs = append(probs, pipeline.Problem{Key: ctx, Msg: "unknown context (composer, run)"})
			continue
		}
		for _, name := range sortedKeys(sect) {
			key := ctx + "." + name
			if defaultKeys(ctx, name) == nil {
				probs = append(probs, pipeline.Problem{Key: key, Msg: "unknown action (" + strings.Join(actionNames(ctx), ", ") + ")"})
				continue
			}
			keys, msg := keyList(sect[name])
			if msg == "" {
				for _, k := range keys {
					if !validKeyName(k) {
						msg = fmt.Sprintf("%q is not a key name (like ctrl+g, alt+enter, shift+tab, f2)%s", k, canonicalHint(k))
					} else if why := lockReason(ctx, name, k); why != "" {
						msg = why
					}
					if msg != "" {
						break
					}
				}
			}
			if msg != "" {
				probs = append(probs, pipeline.Problem{Key: key, Msg: msg})
				continue
			}
			out[keyOverride{ctx, name}] = keys
		}
	}
	if len(probs) == 0 {
		probs = keyConflicts(out)
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return out, nil
}

// keyList reads a key or a list of keys; msg is the Problem, if any.
func keyList(v any) (keys []string, msg string) {
	switch x := v.(type) {
	case string:
		keys = []string{x}
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, "must be a key name or a list of them"
			}
			keys = append(keys, s)
		}
	default:
		return nil, "must be a key name or a list of them"
	}
	if len(keys) == 0 || slices.Contains(keys, "") {
		return nil, "can't be empty: every action needs a key"
	}
	return keys, ""
}

// keyConflicts finds a key two actions would share once the overrides apply.
// The composer and the run view count as one: the queued Task's box resolves
// both.
func keyConflicts(over map[keyOverride][]string) []pipeline.Problem {
	owner := map[string]keyOverride{}
	var probs []pipeline.Problem
	for _, a := range defaultActions {
		id := keyOverride{a.ctx, a.name}
		keys, ok := over[id]
		if !ok {
			keys = a.keys
		}
		for _, k := range keys {
			if prev, dup := owner[k]; dup {
				at := id
				if _, o := over[prev]; o && !ok {
					at = prev
				}
				other := prev
				if at == prev {
					other = id
				}
				probs = append(probs, pipeline.Problem{Key: at.ctx + "." + at.name,
					Msg: fmt.Sprintf("%s is also bound to %s.%s", k, other.ctx, other.name)})
				continue
			}
			owner[k] = id
		}
	}
	return probs
}

func actionNames(ctx string) []string {
	var n []string
	for _, a := range defaultActions {
		if a.ctx == ctx {
			n = append(n, a.name)
		}
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// loadKeybindings reads the file into a new keyMap, and returns a one-line
// notice when the file is bad (the defaults are then used). A missing file
// means the defaults. Called when the composer opens, never during a Run.
func loadKeybindings(getenv func(string) string) (*keyMap, string) {
	path := keybindingsPath(getenv)
	if path == "" {
		return defaultKeyMap, ""
	}
	data, err := readSmallFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultKeyMap, ""
	}
	if err != nil {
		return ignoredKeys(path, getenv, err.Error()), fmt.Sprintf("oge: reading %s: %v; using the default keys", path, err)
	}
	over, probs := parseKeybindings(data)
	if len(probs) > 0 {
		return ignoredKeys(path, getenv, probs[0].String()), fmt.Sprintf("oge: %s: %s; using the default keys", path, probs[0])
	}
	kb := defaultKeyMap.with(over)
	kb.src = shortPath(path, getenv)
	return kb, ""
}

// ignoredKeys is the defaults, remembering why the file was not used, for the
// panel's note.
func ignoredKeys(path string, getenv func(string) string, why string) *keyMap {
	return &keyMap{binds: actionTable, src: shortPath(path, getenv), problem: why}
}

// shortPath writes a path under the home directory as ~/....
func shortPath(path string, getenv func(string) string) string {
	home := getenv("HOME")
	if home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && filepath.IsLocal(rel) {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

// expandLabel is the key that expands the run view, for the hints.
func (k *keyMap) expandLabel() string {
	if ks := k.actionKeys(ctxRun, actExpand); len(ks) > 0 {
		return ks[0]
	}
	return "ctrl+o"
}
