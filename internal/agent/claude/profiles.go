package claude

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is one Claude config directory the user can run a Stage with
// (#128). Name is empty for the default profile, bound as plain "claude";
// any other is bound as "claude@<Name>".
type Profile struct {
	Name string
	Dir  string
}

// Label is how a binding names the profile: "claude" or "claude@work".
// It is what the dry run and the Ledger show, never Dir.
func (p Profile) Label() string {
	if p.Name == "" {
		return Name
	}
	return Name + "@" + p.Name
}

// profileDirPrefix starts the name of every discovered profile directory
// under $HOME: ~/.claude-work is the profile "work".
const profileDirPrefix = ".claude-"

// Profiles lists the Claude profiles in one order: the default first, then
// the rest by name. It is the one place profiles are discovered, so the
// adapter, the resolver and the TUI picker agree.
//
// The default is the dir in getenv("CLAUDE_CONFIG_DIR") when that is
// absolute, else ~/.claude. Every directory ~/.claude-<name> that holds a
// Claude config becomes the profile <name>. A directory holds one when it
// has a .claude.json, a settings.json or a projects directory: a Stat for
// the entry's presence, never a read (ADR-0006). extra adds, or replaces,
// profiles kept elsewhere by name (the user-level Öge config, #129).
func Profiles(home string, getenv func(string) string, extra map[string]string) []Profile {
	def := Profile{}
	if d := getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		def.Dir = filepath.Clean(d)
	} else if filepath.IsAbs(home) {
		def.Dir = filepath.Join(home, ".claude")
	}
	byName := map[string]string{}
	for _, e := range claudeDirs(home) {
		if name, _ := strings.CutPrefix(filepath.Base(e), profileDirPrefix); ValidProfileName(name) && holdsConfig(e) {
			byName[name] = e
		}
	}
	for name, dir := range extra {
		if ValidProfileName(name) && filepath.IsAbs(dir) {
			byName[name] = filepath.Clean(dir)
		}
	}
	// A parent CLAUDE_CONFIG_DIR that is a discovered profile makes the
	// default that profile, named, instead of a second entry for one dir.
	for name, dir := range byName {
		if def.Dir == dir {
			def.Name = name
			delete(byName, name)
		}
	}
	out := []Profile{def}
	for name, dir := range byName {
		out = append(out, Profile{Name: name, Dir: dir})
	}
	rest := out[1:]
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return out
}

// claudeDirs are every directory ~/.claude-* under home, whatever it
// holds or is called: a Stat for presence, never a read (ADR-0006).
func claudeDirs(home string) []string {
	if !filepath.IsAbs(home) {
		return nil
	}
	var out []string
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), profileDirPrefix) {
			continue
		}
		dir := filepath.Join(home, e.Name())
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

// DenyDirs are the config dirs a sandbox must not let an agent read: every
// ~/.claude-* directory (bindable or not, with a config or not), the
// default and every profile.
func DenyDirs(home string, getenv func(string) string, extra map[string]string) []string {
	var out []string
	add := func(d string) {
		if d != "" && !oneOf(d, out) {
			out = append(out, d)
		}
	}
	for _, p := range Profiles(home, getenv, extra) {
		add(p.Dir)
	}
	for _, d := range claudeDirs(home) {
		add(d)
	}
	return out
}

// ValidProfileName reports whether s can be written in a binding such as
// claude@<s>:<model>: letters, digits, '.', '_' and '-'.
func ValidProfileName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func holdsConfig(dir string) bool {
	for _, entry := range []string{".claude.json", "settings.json", "projects"} {
		if _, err := os.Stat(filepath.Join(dir, entry)); err == nil {
			return true
		}
	}
	return false
}
