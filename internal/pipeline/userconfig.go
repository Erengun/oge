package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// UserConfigRel is the user-level Öge config, relative to the config home
// ($XDG_CONFIG_HOME, else ~/.config on every OS, to match dotfile habits).
// It holds personal, per-repo choices that never belong in git (#129).
const UserConfigRel = "oge/config.toml"

// UserSchemaVersion is the newest user-config schema this binary understands.
const UserSchemaVersion = 1

// UserConfig is the user-level file as written. Precedence for a Run:
// flags > this file (for this repo) > .oge/oge.toml > installed default.
type UserConfig struct {
	Schema int `toml:"schema"`
	// Profiles are extra Claude profiles kept outside ~/.claude-<name>:
	// name -> absolute config directory.
	// It is the `extra` map of claude.Profiles.
	Profiles map[string]string       `toml:"profiles,omitempty"`
	Repos    map[string]RepoDefaults `toml:"repos,omitempty"`
	// Notify is the live view's bell and desktop notification (#170). It is
	// global, not per repo: how a person is told belongs to them, not the repo.
	Notify *NotifyConfig `toml:"notify,omitempty"`
	// Theme is the live view's palette preset (#174), one of KnownThemes;
	// "" is auto. Global: how the terminal looks is the person's, not the repo's.
	Theme string `toml:"theme,omitempty"`
	// Vim turns on vim's INSERT and NORMAL modes in the composer (#156).
	// Global: how a person edits is theirs. Off when absent.
	Vim bool `toml:"vim,omitempty"`
	// VimEscape is a two-character sequence typed in INSERT that acts as Esc,
	// such as "jj" (#158). Global like Vim; empty is off.
	VimEscape string `toml:"vim_escape,omitempty"`
	// Advisor is who gives a Chat's second opinion (docs/ux/advisor.md,
	// D-A1). Global: what a person pays for a second opinion is theirs.
	Advisor *AdvisorConfig `toml:"advisor,omitempty"`
	// Chat is how a Chat treats a failing Check (docs/ux/desk.md, U2).
	// Global: whether Öge may prompt an agent for them is the person's.
	Chat *ChatConfig `toml:"chat,omitempty"`
}

// ChatConfig is the [chat] table.
type ChatConfig struct {
	// Feedback is what a failing Check does: "on" sends the agent its
	// feedback turn at once, "ask" shows `s send back` and waits for the
	// human, "off" offers nothing. Empty is "ask".
	Feedback string `toml:"feedback,omitempty"`
	// FeedbackBudget is how many feedback turns a chain of cuts gets; 0 is
	// DefaultFeedbackBudget.
	FeedbackBudget int `toml:"feedback_budget,omitempty"`
}

// [chat] feedback values.
const (
	ChatFeedbackOn  = "on"
	ChatFeedbackAsk = "ask"
	ChatFeedbackOff = "off"
)

// DefaultFeedbackBudget is the feedback turns a chain gets when none is
// configured (D5).
const DefaultFeedbackBudget = 3

// ChatConfig returns the [chat] settings, the zero value when there are none.
func (c *UserConfig) ChatConfig() ChatConfig {
	if c == nil || c.Chat == nil {
		return ChatConfig{}
	}
	return *c.Chat
}

// AdvisorConfig is the [advisor] table. Each key falls back when empty:
// the agent to the Chat's implementer's, the model to the agent's
// strongest, the effort to DefaultAdvisorEffort (D-A1).
type AdvisorConfig struct {
	Agent  string `toml:"agent,omitempty"`
	Model  string `toml:"model,omitempty"`
	Effort string `toml:"effort,omitempty"`
	// Auto is what happens when a chain's feedback budget is spent (D-A3):
	// "on" consults and sends the advice, "ask" shows it first, "off" never
	// consults. Empty is "ask".
	Auto string `toml:"auto,omitempty"`
}

// [advisor] auto values.
const (
	AdvisorAutoOn  = "on"
	AdvisorAutoAsk = "ask"
	AdvisorAutoOff = "off"
)

// DefaultAdvisorEffort is the Advisor's effort when none is configured.
const DefaultAdvisorEffort = "high"

// AdvisorConfig returns the [advisor] settings, the zero value when there
// are none.
func (c *UserConfig) AdvisorConfig() AdvisorConfig {
	if c == nil || c.Advisor == nil {
		return AdvisorConfig{}
	}
	return *c.Advisor
}

// VimMode reports whether the composer's vim mode is on.
func (c *UserConfig) VimMode() bool { return c != nil && c.Vim }

// VimEscapeKeys is the configured INSERT escape sequence, "" when there is none.
func (c *UserConfig) VimEscapeKeys() string {
	if c == nil {
		return ""
	}
	return c.VimEscape
}

// KnownThemes are the palette presets of the live view. Status is never
// shown by colour alone, so every one keeps the same glyphs and words.
var KnownThemes = []string{"auto", "dark", "light", "daltonized", "ansi"}

// ThemeName returns the configured palette preset, "" when there is none.
func (c *UserConfig) ThemeName() string {
	if c == nil {
		return ""
	}
	return c.Theme
}

// NotifyConfig holds the [notify] settings. Each is on when absent.
type NotifyConfig struct {
	Attention *bool `toml:"attention,omitempty"` // the Run needs you (a Gate)
	RunEnd    *bool `toml:"run_end,omitempty"`   // the Run ended, any outcome
	Bell      *bool `toml:"bell,omitempty"`      // ring the bell with either
}

// NotifyConfig returns the [notify] settings, or nil when there are none.
func (c *UserConfig) NotifyConfig() *NotifyConfig {
	if c == nil {
		return nil
	}
	return c.Notify
}

// RepoDefaults are one repository's personal defaults, keyed in the file by
// the repo root's absolute path. Absent keys fall through to the next source.
type RepoDefaults struct {
	Mode       string `toml:"mode,omitempty"` // fast | standard | blind
	Unattended *bool  `toml:"unattended,omitempty"`
	Apply      *bool  `toml:"apply,omitempty"`
	Stages     struct {
		Implement UserStage `toml:"implement,omitempty"`
		Verify    UserStage `toml:"verify,omitempty"`
	} `toml:"stages,omitempty"`
}

// UserStage is a Stage binding. Agent is carried as written, agent[@profile];
// the Claude adapter owns the @profile grammar (#139).
type UserStage struct {
	Agent string `toml:"agent,omitempty"`
	Model string `toml:"model,omitempty"`
	// Effort is Claude's --effort level (#179); "" is its default.
	Effort string `toml:"effort,omitempty"`
}

// KnownEfforts are the levels `claude --effort` accepts (claude 2.1.289).
var KnownEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// UserConfigPath returns where the user config lives, or "" when neither
// XDG_CONFIG_HOME nor HOME is set.
func UserConfigPath(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, filepath.FromSlash(UserConfigRel))
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".config", filepath.FromSlash(UserConfigRel))
	}
	return ""
}

// LoadUserConfig parses a user config strictly. Like Load it returns every
// problem it finds. It does not scan for credentials: a profile legitimately
// points at a Claude config directory, which Öge selects but never reads
// (ADR-0006).
func LoadUserConfig(data []byte) (*UserConfig, []Problem) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, []Problem{{Msg: "not valid TOML" + position(err)}}
	}
	var probs []Problem
	switch v, ok := raw["schema"]; {
	case !ok:
		probs = append(probs, Problem{"schema", fmt.Sprintf("required; add schema = %d", UserSchemaVersion)})
	case !isInt(v):
		probs = append(probs, Problem{"schema", "must be an integer"})
	case v.(int64) > UserSchemaVersion:
		return nil, []Problem{{"schema", fmt.Sprintf("%d is newer than this oge understands (schema %d); upgrade oge", v.(int64), UserSchemaVersion)}}
	case v.(int64) < 1:
		probs = append(probs, Problem{"schema", fmt.Sprintf("%d is not a valid schema; use schema = %d", v.(int64), UserSchemaVersion)})
	}
	var cfg UserConfig
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, append(probs, typeError(err))
	}
	for _, p := range undecoded(md.Undecoded()) {
		// The project file's trust-mistake rules don't apply here.
		probs = append(probs, Problem{p.Key, "unknown key"})
	}
	probs = append(probs, cfg.validate()...)
	if len(probs) > 0 {
		return nil, probs
	}
	return &cfg, nil
}

func (c *UserConfig) validate() []Problem {
	var probs []Problem
	add := func(key, msg string) { probs = append(probs, Problem{key, msg}) }
	if c.Theme != "" && !oneOf(c.Theme, KnownThemes...) {
		add("theme", "must be one of "+strings.Join(KnownThemes, ", "))
	}
	if r := []rune(c.VimEscape); c.VimEscape != "" && (len(r) != 2 || strings.ContainsFunc(c.VimEscape, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) })) {
		add("vim_escape", "must be two printable characters, such as \"jj\"")
	}
	if a := c.Advisor; a != nil {
		if a.Agent != "" && strings.TrimSpace(a.Agent) == "" {
			add("advisor.agent", "must not be blank")
		}
		if a.Effort != "" && !oneOf(a.Effort, KnownEfforts...) {
			add("advisor.effort", "must be one of "+strings.Join(KnownEfforts, ", "))
		}
		if a.Auto != "" && !oneOf(a.Auto, AdvisorAutoOn, AdvisorAutoAsk, AdvisorAutoOff) {
			add("advisor.auto", "must be one of on, ask, off")
		}
		for field, v := range map[string]string{"agent": a.Agent, "model": a.Model} {
			if credentialValue.MatchString(v) || strings.HasPrefix(strings.TrimSpace(v), "sk-") {
				add("advisor."+field, "looks like a credential; Öge never takes credentials in config (ADR-0006)")
			}
		}
	}
	if ch := c.Chat; ch != nil {
		if ch.Feedback != "" && !oneOf(ch.Feedback, ChatFeedbackOn, ChatFeedbackAsk, ChatFeedbackOff) {
			add("chat.feedback", "must be one of ask, on, off")
		}
		if ch.FeedbackBudget < 0 {
			add("chat.feedback_budget", "must be 0 or more")
		}
	}
	for _, name := range sortedKeys(c.Profiles) {
		key := "profiles." + name
		switch {
		case strings.TrimSpace(name) == "" || strings.ContainsAny(name, "@: \t"):
			add("profiles", fmt.Sprintf("profile name %q must be non-empty without spaces, '@' or ':'", name))
		case !filepath.IsAbs(c.Profiles[name]):
			add(key, "must be an absolute path to the profile's config directory")
		}
	}
	for _, root := range sortedKeys(c.Repos) {
		key := `repos."` + root + `"`
		d := c.Repos[root]
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			add(key, "a repo is keyed by the clean, absolute path of its root")
		}
		if d.Mode != "" && !oneOf(d.Mode, "fast", "standard", "blind") {
			add(key+".mode", `must be "fast", "standard" or "blind"`)
		}
		for _, s := range []struct {
			name string
			st   UserStage
		}{{"implement", d.Stages.Implement}, {"verify", d.Stages.Verify}} {
			if s.st.Agent != "" && strings.TrimSpace(s.st.Agent) == "" {
				add(key+".stages."+s.name+".agent", "must not be blank")
			}
			if s.st.Effort != "" && !oneOf(s.st.Effort, KnownEfforts...) {
				add(key+".stages."+s.name+".effort", "must be one of "+strings.Join(KnownEfforts, ", "))
			}
			for field, v := range map[string]string{"agent": s.st.Agent, "model": s.st.Model} {
				if credentialValue.MatchString(v) || strings.HasPrefix(strings.TrimSpace(v), "sk-") {
					add(key+".stages."+s.name+"."+field, "looks like a credential; Öge never takes credentials in config (ADR-0006)")
				}
			}
		}
	}
	return probs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ForRepo returns the defaults saved for repoRoot. Keys are canonical:
// absolute, clean and with symlinks resolved, which SaveRepoDefaults
// guarantees. A git worktree is a separate repo with its own key.
func (c *UserConfig) ForRepo(repoRoot string) *RepoDefaults {
	if c == nil {
		return nil
	}
	root := filepath.Clean(repoRoot)
	if d, ok := c.Repos[root]; ok {
		return &d
	}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		if d, ok := c.Repos[real]; ok {
			return &d
		}
	}
	return nil
}

// SaveRepoDefaults records defaults for repoRoot in the user config at path.
// It is the only writer, and only an explicit user action (the TUI's "save as
// default") calls it. The write is atomic (temp file, rename), the file is
// mode 0600 in a 0700 directory, and other repos' sections and [profiles]
// are kept. An existing file that doesn't parse is never overwritten.
// Comments in the file are not preserved.
func SaveRepoDefaults(path, repoRoot string, defaults RepoDefaults) error {
	return SaveUserDefaults(path, repoRoot, defaults, "")
}

// SaveUserDefaults is SaveRepoDefaults that also sets the global theme when
// theme is not "".
func SaveUserDefaults(path, repoRoot string, defaults RepoDefaults, theme string) error {
	if !filepath.IsAbs(repoRoot) {
		return fmt.Errorf("repo root %q must be absolute", repoRoot)
	}
	repoRoot = filepath.Clean(repoRoot)
	if real, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = real
	}
	// A dotfile-managed config is usually a symlink: write through it.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	cfg := &UserConfig{Schema: UserSchemaVersion}
	switch data, err := os.ReadFile(path); {
	case err == nil:
		var probs []Problem
		if cfg, probs = LoadUserConfig(data); len(probs) > 0 {
			return fmt.Errorf("%s isn't valid, so it was not changed: %s", path, probs[0])
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if cfg.Repos == nil {
		cfg.Repos = map[string]RepoDefaults{}
	}
	cfg.Repos[repoRoot] = defaults
	if theme != "" {
		cfg.Theme = theme
	}
	if probs := cfg.validate(); len(probs) > 0 {
		return fmt.Errorf("not saved: %s", probs[0])
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(cfg); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
