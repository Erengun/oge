package cli

import (
	"errors"
	"fmt"
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

// defaultActions is the table as shipped; reloads start from it.
var defaultActions = func() []actionBinding {
	d := slices.Clone(actionTable)
	for i := range d {
		d[i].keys = slices.Clone(d[i].keys)
	}
	return d
}()

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

// bareText is a key that types a character: a Gate or the composer takes it
// as text, so it can't be an action's key.
func bareText(s string) bool {
	r := []rune(s)
	return len(r) == 1 && r[0] > ' '
}

// lockReason is why name can't be bound to the action, or "".
func lockReason(ctx, action, name string) string {
	if slices.Contains(defaultKeys(ctx, action), name) {
		return "" // an action keeps its own default keys
	}
	if why, ok := lockedKeys[name]; ok {
		return name + " " + why + " (locked, ADR-0015)"
	}
	if bareText(name) {
		return name + " is typed text, not a shortcut"
	}
	if slices.Contains(ownedKeys, name) {
		return name + " is used for editing and navigation, which are not remappable"
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
						msg = fmt.Sprintf("%q is not a key name (like ctrl+g, alt+enter, shift+tab, f2)", k)
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

// resetActionKeys puts the shipped keys back.
func resetActionKeys() {
	for _, a := range defaultActions {
		setActionKeys(a.ctx, a.name, slices.Clone(a.keys))
	}
}

// reloadKeybindings re-reads the file and applies it, and returns a one-line
// notice when the file is bad (the defaults are then used). A missing file
// means the defaults. Called when the composer opens, never during a Run.
func reloadKeybindings(getenv func(string) string) string {
	resetActionKeys()
	path := keybindingsPath(getenv)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("oge: reading %s: %v; using the default keys", path, err)
	}
	over, probs := parseKeybindings(data)
	if len(probs) > 0 {
		return fmt.Sprintf("oge: %s: %s; using the default keys", path, probs[0])
	}
	for id, keys := range over {
		setActionKeys(id.ctx, id.name, keys)
	}
	return ""
}

// expandLabel is the key that expands the run view, for the hints.
func expandLabel() string {
	if ks := actionKeys(ctxRun, actExpand); len(ks) > 0 {
		return ks[0]
	}
	return "ctrl+o"
}
