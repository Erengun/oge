package pipeline

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Mode is what the user chose on the command line: Fast, Standard or Blind
// (ADR-0019). It is shown on every result and is not a quality ladder.
type Mode string

const (
	Fast     Mode = "Fast"
	Standard Mode = "Standard"
	Blind    Mode = "Blind"
)

// Guarantees each mode gives, by the name --require uses.
var modeGuarantees = map[Mode][]string{
	Fast:     nil,
	Standard: {"held-out"},
	Blind:    {"held-out"},
}

// KnownGuarantees are the names --require accepts.
// TODO(#39-decision): only "held-out" is named by ADR-0019; other guarantee
// names (e.g. one only Blind satisfies) are left for the human to choose.
var KnownGuarantees = []string{"held-out"}

// Limits are the resolved, frozen bounds of a Run. The defaults are
// provisional and tunable from evaluation without an ADR.
type Limits struct {
	Retries                int           `json:"retries"`
	SendBacks              int           `json:"send_backs"`
	UserRequests           int           `json:"user_requests"`
	Attempts               int           `json:"attempts"`
	OracleGrowthAttempts   int           `json:"oracle_growth_attempts"`
	OracleGrowthPerAttempt int           `json:"oracle_growth_per_attempt"`
	OracleGrowthPerRun     int           `json:"oracle_growth_per_run"`
	StageTimeout           time.Duration `json:"stage_timeout"`
	StageIdleTimeout       time.Duration `json:"stage_idle_timeout"`
	HostRequestTimeout     time.Duration `json:"host_request_timeout"` // 0 = off
}

// DefaultLimits are the spec's provisional defaults (#35).
var DefaultLimits = Limits{
	Retries:      2,
	SendBacks:    3,
	UserRequests: 3,
	Attempts:     12,
	// One verifier Attempt follows every implementer Attempt the send-back
	// budget allows, so the growth limit on verifier Attempts never trips
	// on the normal repair loop (see resolveLimits).
	// TODO(#51): the Oracle-growth Gate; revisit these provisional values.
	OracleGrowthAttempts:   4,
	OracleGrowthPerAttempt: 20,
	OracleGrowthPerRun:     50,
	StageTimeout:           60 * time.Minute,
	StageIdleTimeout:       10 * time.Minute,
}

// Provisional Check defaults.
const (
	DefaultCheckTimeout  = 10 * time.Minute
	DefaultOutputCap     = 1 << 20 // bytes
	DefaultExpectedTests = 1
)

// Source says where a resolved value came from.
type Source string

const (
	FromBuiltin Source = "built-in"
	FromProject Source = "project"
	FromUser    Source = "user"
	FromCLI     Source = "cli"
	// FromTUI is a choice made in the TUI's setup panel (#131). It acts as a
	// flag for this session's Runs, so it beats the user config; unlike a
	// typed flag, a weakening one is shown like the user config's, since it
	// was a menu pick rather than an explicit command.
	FromTUI       Source = "tui"
	FromInstalled Source = "installed"
	FromInherited Source = "inherits implement"
)

// Overrides are the per-Run CLI flags.
type Overrides struct {
	Checks    []string
	Tests     []string
	Outputs   []string
	Agents    []string // --agent <stage>=<agent>[@<profile>][:<model>] or <agent>[@<profile>][:<model>]
	Implement string   // alias: --implement <agent>[@<profile>][:<model>]
	Verify    string   // alias: --verify <agent>[@<profile>][:<model>]
	// ClaudeProfiles are the names of the Claude profiles found on this
	// machine (the default, bound as plain "claude", is not among them):
	// what claude@<name> may name.
	ClaudeProfiles []string
	Fast           bool
	Blind          bool
	Confirm        bool
	Require        []string
	// Unattended removes the optional Gates (ADR-0008).
	Unattended bool
	// User holds this repo's personal defaults from the user-level config
	// (nil when there are none). Flags beat it; it beats .oge/oge.toml.
	User *RepoDefaults
	// UserConfigPath names that file in messages about what it set.
	UserConfigPath string
	// Standard is --standard: the mode flag that lets a Run override a
	// weaker mode set in the user config.
	Standard bool
	// ModeFrom is who set the mode flag above: FromCLI (the zero value) for
	// a flag typed on the command line, FromTUI for the setup panel's pick.
	ModeFrom Source
	// TUIAgents are the Stage bindings picked in the setup panel, as
	// "<stage>=<agent>[@<profile>][:<model>]". They are applied after the
	// flags above and replace them for the same Stage, with source FromTUI;
	// a Stage not named here keeps its flag's source.
	TUIAgents []string
	// TUIEfforts are the effort levels picked in the setup panel, by Stage
	// name, for the Stages in TUIAgents (#179).
	TUIEfforts map[string]string
	// UnattendedFrom is who set Unattended: FromCLI (--unattended, the zero
	// value), FromUser (the user config) or FromTUI. Only the last two are
	// shown as trust-weakening: they remove Gates without a typed flag.
	UnattendedFrom Source
	// TUIOracle says the setup panel edited the Checks and test globs
	// (#132): TUIChecks and TUITests then replace the project's (flags
	// still add to them). A Check the project file doesn't have, or one
	// that was edited, is sourced FromTUI; one the file has and the panel
	// left out is shown as trust-weakening.
	TUIOracle bool
	TUIChecks []CheckCommandConfig
	TUITests  []string
	// TUITestConfig and TUIOutputs are the first-run proposal's other
	// project facts; they apply only where the file has none.
	TUITestConfig []string
	TUIOutputs    []string
}

// Frozen is the resolved Pipeline a Run would freeze.
type Frozen struct {
	Mode       Mode
	ModeSource Source `json:"-"` // FromUser, FromCLI, FromTUI or "" (project or default)
	// TestGlobsSource is FromTUI when the setup panel changed the test globs.
	TestGlobsSource Source `json:"-"`
	Verify          string // "after", "before", or "" in Fast mode
	Project         ProjectConfig
	Setup           SetupConfig
	Checks          []CheckCommand
	Stages          []Stage
	ResultGate      bool
	Limits          Limits
	Graph           Graph
	Hash            string
	TrustWeakening  []Weakening
	// Limitations are what a bound agent can't guarantee, by Stage, such
	// as Codex's implementer shell seeing credentials (ADR-0018). They
	// are reported, never Degraded.
	Limitations []string `json:",omitempty"`
	// Notices are what the resolution changed from what was asked.
	Notices []string
}

// CheckCommand is one resolved Oracle command.
type CheckCommand struct {
	Run           string
	Report        string
	ExpectedTests int
	Timeout       time.Duration
	OutputCap     int64
	Source        Source
}

// Stage is one resolved Stage with its binding.
type Stage struct {
	Name    string
	Role    string
	Agent   string
	Profile string // the agent's config profile ("work" for claude@work); "" is the default
	Model   string
	// Effort is the agent's thinking/effort level for this Stage, from the
	// user config or setup; "" is the agent's default (#179). Only Claude
	// takes one.
	Effort  string `json:"Effort,omitempty"`
	Network string
	Source  Source
	// NotLoggedIn is set by the CLI, after resolving, when the agent
	// reported that this Stage's profile has no login (#186). It is not
	// part of the compiled graph.
	NotLoggedIn bool `json:"-"`
}

// Binding is the Stage's agent as the user writes and reads it:
// "claude", or "claude@work" with a profile. Never a path.
func (s Stage) Binding() string {
	if s.Profile != "" {
		return s.Agent + "@" + s.Profile
	}
	return s.Agent
}

// Weakening is a trust-weakening option in effect and where it came from.
type Weakening struct {
	Option string
	Source Source
	Detail string
}

// Resolve applies built-in defaults and CLI overrides to a project config
// (nil when the Snapshot has none), validates the result as `oge run` would,
// and compiles the frozen graph. installed lists the KnownAgents found on
// PATH, used only when nothing is bound. registered names agents a test
// build adds beyond KnownAgents.
func Resolve(cfg *Config, o Overrides, installed []string, registered ...string) (*Frozen, []Problem) {
	src := FromProject
	if cfg == nil {
		cfg = &Config{Schema: SchemaVersion}
		src = FromBuiltin
	}
	var probs []Problem
	add := func(key, format string, a ...any) { probs = append(probs, Problem{key, fmt.Sprintf(format, a...)}) }
	modeFrom := FromCLI
	if o.ModeFrom != "" {
		modeFrom = o.ModeFrom
	}
	p := cfg.Pipelines.Default
	f := &Frozen{Project: cfg.Project, Setup: cfg.Setup}

	// Mode.
	var flagged []string
	for _, m := range []struct {
		on   bool
		name string
	}{{o.Fast, "--fast"}, {o.Blind, "--blind"}, {o.Standard, "--standard"}} {
		if m.on {
			flagged = append(flagged, m.name)
		}
	}
	userMode := ""
	if o.User != nil {
		userMode = o.User.Mode
	}
	switch {
	case len(flagged) > 1:
		add(strings.Join(flagged, ", "), "choose one mode")
	case o.Fast:
		f.Mode, f.ModeSource = Fast, modeFrom
	case o.Blind:
		f.Mode, f.Verify, f.ModeSource = Blind, "before", modeFrom
	case o.Standard:
		f.Mode, f.Verify, f.ModeSource = Standard, "after", modeFrom
	case userMode == "fast":
		f.Mode, f.ModeSource = Fast, FromUser
	case userMode == "blind":
		f.Mode, f.Verify, f.ModeSource = Blind, "before", FromUser
	case userMode == "standard":
		f.Mode, f.Verify, f.ModeSource = Standard, "after", FromUser
	case p.Verify == "before":
		f.Mode, f.Verify = Blind, "before"
	default:
		f.Mode, f.Verify = Standard, "after"
	}
	for _, g := range o.Require {
		if !oneOf(g, KnownGuarantees...) {
			add("--require "+g, "unknown guarantee; known: %s", strings.Join(KnownGuarantees, ", "))
		} else if f.Mode != "" && !oneOf(g, modeGuarantees[f.Mode]...) {
			if f.ModeSource == FromUser {
				add("--require "+g, "%s mode (mode = %q in your user config%s) doesn't give this guarantee; pass --standard or --blind", f.Mode, userMode, inFile(o.UserConfigPath))
			} else if f.ModeSource == FromTUI {
				add("--require "+g, "%s mode (set in setup) doesn't give this guarantee; change the mode with Tab", f.Mode)
			} else {
				add("--require "+g, "%s mode doesn't give this guarantee; use Standard (the default) or --blind", f.Mode)
			}
		}
	}

	// Oracle: CLI flags add to the project's, never remove from it.
	// TODO(#39-decision): --check/--tests/--output append to the project's
	// values (trust-conservative: a flag can't drop an Oracle command) rather
	// than replace them.
	// Blank values are hard errors, so they can't count towards the rule
	// that a Run needs at least one Check command and test glob.
	nonBlank := func(flag string, vals []string) []string {
		var out []string
		for _, v := range vals {
			if v = strings.TrimSpace(v); v == "" {
				add(flag, "must not be empty or whitespace")
				continue
			}
			out = append(out, v)
		}
		return out
	}
	baseGlobs, baseChecks := cfg.Project.TestGlobs, cfg.Check.Commands
	var removed, removedGlobs []string // what the file has and the setup panel left out
	if o.TUIOracle {
		baseGlobs, baseChecks = o.TUITests, o.TUIChecks
		keptGlobs := map[string]bool{}
		for _, g := range o.TUITests {
			keptGlobs[strings.TrimSpace(g)] = true
		}
		for _, g := range cfg.Project.TestGlobs {
			if g = strings.TrimSpace(g); !keptGlobs[g] {
				removedGlobs = append(removedGlobs, g)
			}
		}
		if len(removedGlobs) > 0 || len(o.TUITests) != len(cfg.Project.TestGlobs) {
			f.TestGlobsSource = FromTUI
		}
		if len(f.Project.TestConfig) == 0 {
			f.Project.TestConfig = appendCopy(nil, o.TUITestConfig)
		}
		if len(f.Project.OutputGlobs) == 0 {
			f.Project.OutputGlobs = appendCopy(nil, o.TUIOutputs)
		}
		kept := map[string]bool{}
		for _, c := range o.TUIChecks {
			kept[strings.TrimSpace(c.Run)] = true
		}
		for _, c := range cfg.Check.Commands {
			if run := strings.TrimSpace(c.Run); !kept[run] {
				removed = append(removed, run)
			}
		}
	}
	f.Project.TestGlobs = appendCopy(trimAll(baseGlobs), nonBlank("--tests", o.Tests))
	f.Project.OutputGlobs = appendCopy(trimAll(f.Project.OutputGlobs), nonBlank("--output", o.Outputs))
	inFile := map[string]bool{}
	for _, c := range cfg.Check.Commands {
		inFile[strings.TrimSpace(c.Run)] = true
	}
	for _, c := range baseChecks {
		c.Run = strings.TrimSpace(c.Run)
		cs := src
		if o.TUIOracle && !inFile[c.Run] {
			cs = FromTUI
		}
		if c.Report == "" && cs == FromTUI {
			c.Report = inferReport(c.Run)
		}
		f.Checks = append(f.Checks, resolveCheck(c, cs))
	}
	for _, run := range nonBlank("--check", o.Checks) {
		f.Checks = append(f.Checks, resolveCheck(CheckCommandConfig{Run: run, Report: inferReport(run)}, FromCLI))
	}
	if len(f.Checks) == 0 {
		add("check.commands", "no Check command: a Run can't accept anything without one. Add [[check.commands]] to %s or pass --check", ConfigPath)
	}
	if len(f.Project.TestGlobs) == 0 {
		add("project.test_globs", "no test globs: the Oracle's tests must be named. Set project.test_globs in %s or pass --tests", ConfigPath)
	}
	// TODO(#46-decision): no --check-report flag. A CLI-only --check gets
	// a report only when inferReport recognises it (go test -json); any
	// other held-out runner declares its report in the config.
	if f.Mode != Fast {
		for i, c := range f.Checks {
			if c.Report == "" {
				key := fmt.Sprintf("check.commands[%d].report", i)
				if c.Source == FromCLI {
					key = fmt.Sprintf("--check %q", c.Run)
				}
				add(key, "%s mode collects held-out tests, so each Check needs a structured-report format (%s). Declare it in %s", f.Mode, strings.Join(KnownReports, " or "), ConfigPath)
			}
		}
	}

	// Stages and bindings.
	impl := Stage{Name: "implement", Role: "implementer", Network: "off", Source: src}
	impl.Agent, impl.Profile = SplitAgent(p.Stages.Implement.Agent)
	impl.Model = p.Stages.Implement.Model
	if p.Stages.Implement.Network != "" {
		impl.Network = p.Stages.Implement.Network
	}
	if impl.Agent == "" {
		impl.Source = ""
	}
	// Profile names never enter git (#127); only the user config may bind one.
	if impl.Profile != "" {
		probs = append(probs, Problem{"pipelines.default.stages.implement.agent", profilesArePersonal})
	}
	if o.User != nil {
		probs = append(probs, userBinding(&impl, o.User.Stages.Implement, "implement", registered, o.ClaudeProfiles)...)
	}
	ver := Stage{Name: "verify", Role: "verifier", Network: "off", Source: src}
	ver.Agent, ver.Profile = SplitAgent(p.Stages.Verify.Agent)
	ver.Model = p.Stages.Verify.Model
	if ver.Agent == "" {
		ver.Source = ""
	}
	if ver.Profile != "" && f.Mode != Fast {
		probs = append(probs, Problem{"pipelines.default.stages.verify.agent", profilesArePersonal})
	}
	if o.User != nil {
		probs = append(probs, userBinding(&ver, o.User.Stages.Verify, "verify", registered, o.ClaudeProfiles)...)
	}
	switch f.Mode {
	case Fast:
		f.Stages = []Stage{impl}
	case Blind:
		f.Stages = []Stage{ver, impl}
	default:
		f.Stages = []Stage{impl, ver}
	}
	known := func(a string) bool { return knownAgent(a) || oneOf(a, registered...) }
	probs = append(probs, bind(f.Stages, o, known)...)
	if f.Mode != "" {
		probs = append(probs, defaultBindings(f.Stages, installed)...)
	}

	// Gates and limits.
	f.ResultGate = !o.Unattended && (o.Confirm || (p.Gates.Result != nil && *p.Gates.Result))
	switch {
	case o.Unattended && o.Confirm:
		f.Notices = append(f.Notices, "--confirm ignored: unattended Runs have no Result gate (ADR-0008)")
	case o.Unattended && p.Gates.Result != nil && *p.Gates.Result:
		f.Notices = append(f.Notices, "gates.result ignored: unattended Runs have no Result gate (ADR-0008)")
	}
	f.Limits = resolveLimits(p.Limits)

	// Trust-weakening options: from the project file, or a mode the user
	// config chose (there is no CLI network flag in the MVP).
	if (f.ModeSource == FromUser || f.ModeSource == FromTUI) && f.Mode == Fast {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"mode = fast", f.ModeSource, "no independent verifier, no held-out tests"})
	}
	if o.Unattended && (o.UnattendedFrom == FromUser || o.UnattendedFrom == FromTUI) {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"unattended", o.UnattendedFrom, "removes the optional Gates, ADR-0008"})
	}
	for _, run := range removed {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"Check removed in setup: " + run, FromTUI, "the Oracle has one fewer Check than " + ConfigPath})
	}
	for _, g := range removedGlobs {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"test glob removed in setup: " + g, FromTUI, "the Oracle protects fewer files than " + ConfigPath})
	}
	if cfg.Setup.Network == "on" {
		// The setup command isn't echoed: it may hold a token (#39).
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"setup network = on", FromProject, ""})
	}
	if impl.Network == "on" {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"implement network = on", FromProject, ""})
	}

	f.Limitations = limitations(f.Stages)

	if len(probs) > 0 {
		return nil, probs
	}
	f.Graph = Compile(f.Mode, f.ResultGate)
	f.Hash = f.Graph.Hash(f.Limits)
	return f, nil
}

// limitations are the bound agents' known limitations (ADR-0018, ADR-0023).
func limitations(stages []Stage) []string {
	var out []string
	for _, s := range stages {
		if s.Agent == "codex" && s.Role == "implementer" {
			out = append(out, s.Name+" (codex): shell commands can see your environment's credentials; Codex's shell snapshot ignores its environment policy")
		}
		if s.Agent == "agy" && s.Role == "implementer" {
			out = append(out, s.Name+" (agy): headless runs operate under --dangerously-skip-permissions; in-band interactive approvals are not supported by the agy CLI")
		}
	}
	return out
}

func inFile(path string) string {
	if path == "" {
		return ""
	}
	return ", " + path
}

// userBinding lets the user config's choice for a Stage replace the
// project's. As with --agent, a user agent brings its own model (none means
// the agent's default); a user model alone keeps the project's agent.
func userBinding(s *Stage, u UserStage, name string, registered, profiles []string) []Problem {
	key := "user config: stages." + name + ".agent"
	switch {
	case u.Agent != "":
		agent, profile, _ := ParseBinding(u.Agent)
		switch {
		case bareAt(u.Agent):
			return []Problem{{key, emptyProfile}}
		case !knownAgent(agent) && !oneOf(agent, registered...):
			return []Problem{{key, "unknown agent; use one of " + strings.Join(KnownAgents, ", ")}}
		}
		if msg := checkProfile(agent, profile, profiles); msg != "" {
			return []Problem{{key, msg}}
		}
		s.Agent, s.Profile, s.Model, s.Source = agent, profile, u.Model, FromUser
	case u.Model != "":
		s.Model = u.Model
		if s.Agent != "" {
			s.Source = FromUser
		}
	}
	if u.Effort != "" {
		if !oneOf(u.Effort, KnownEfforts...) {
			return []Problem{{"user config: stages." + name + ".effort", "unknown effort; use one of " + strings.Join(KnownEfforts, ", ")}}
		}
		s.Effort = u.Effort
	}
	return nil
}

func trimAll(vals []string) []string {
	var out []string
	for _, v := range vals {
		out = append(out, strings.TrimSpace(v))
	}
	return out
}

func appendCopy(a, b []string) []string {
	return append(append([]string(nil), a...), b...)
}

func resolveCheck(c CheckCommandConfig, src Source) CheckCommand {
	r := CheckCommand{Run: c.Run, Report: c.Report, ExpectedTests: DefaultExpectedTests,
		Timeout: DefaultCheckTimeout, OutputCap: DefaultOutputCap, Source: src}
	if c.ExpectedTests != nil {
		r.ExpectedTests = *c.ExpectedTests
	}
	if d, err := parseDuration(c.Timeout); err == nil && c.Timeout != "" {
		r.Timeout = d
	}
	if n, err := parseSize(c.OutputCap); err == nil && c.OutputCap != "" {
		r.OutputCap = n
	}
	return r
}

// InferReport is inferReport for the setup panel: the report a command
// declares by itself, or "".
func InferReport(run string) string { return inferReport(run) }

// inferReport recognises the structured-report format of a --check command
// where it is unambiguous.
func inferReport(run string) string {
	f := strings.Fields(run)
	if len(f) >= 2 && f[0] == "go" && f[1] == "test" {
		for _, a := range f[2:] {
			if a == "-json" || a == "--json" {
				return "go-test-json"
			}
		}
	}
	return ""
}

// bind applies --agent and the Role-kind aliases.
func bind(stages []Stage, o Overrides, knownAgent func(string) bool) []Problem {
	flagsFrom := FromCLI
	profiles := o.ClaudeProfiles
	var probs []Problem
	boundBy := map[string]string{}
	set := func(flag, stage, spec string) {
		agent, profile, model := ParseBinding(spec)
		if bareAt(spec) {
			probs = append(probs, Problem{flag, emptyProfile})
			return
		}
		if !knownAgent(agent) {
			probs = append(probs, Problem{flag, "unknown agent; use one of " + strings.Join(KnownAgents, ", ")})
			return
		}
		if msg := checkProfile(agent, profile, profiles); msg != "" {
			probs = append(probs, Problem{flag, msg})
			return
		}
		for i := range stages {
			if stages[i].Name != stage {
				continue
			}
			if prev, ok := boundBy[stage]; ok {
				probs = append(probs, Problem{flag, fmt.Sprintf("Stage %s is already bound by %s", stage, prev)})
				return
			}
			boundBy[stage] = flag
			stages[i].Agent, stages[i].Profile, stages[i].Model, stages[i].Source = agent, profile, model, flagsFrom
			stages[i].Effort = "" // a flag binding starts from the agent's defaults
			return
		}
		probs = append(probs, Problem{flag, fmt.Sprintf("no Stage %q in this mode (Stages: %s)", stage, stageNames(stages))})
	}
	// A Problem's key names the flag, never its value, which may hold
	// anything the user typed (#39).
	for _, a := range o.Agents {
		flag := "--agent"
		if stage, spec, ok := strings.Cut(a, "="); ok {
			set(flag, stage, spec)
			continue
		}
		if agent, profile, _ := ParseBinding(a); bareAt(a) || !knownAgent(agent) || checkProfile(agent, profile, profiles) != "" {
			set(flag, stages[0].Name, a) // reports the unknown agent or profile once
			continue
		}
		for _, s := range stages {
			set(flag, s.Name, a)
		}
	}
	alias := func(flag, role, spec string) {
		if spec == "" {
			return
		}
		var match []string
		for _, s := range stages {
			if s.Role == role {
				match = append(match, s.Name)
			}
		}
		switch len(match) {
		case 1:
			set(flag, match[0], spec)
		case 0:
			probs = append(probs, Problem{flag, fmt.Sprintf("no %s Stage in this mode; use --agent <stage>=<agent>", role)})
		default:
			probs = append(probs, Problem{flag, fmt.Sprintf("ambiguous: several %s Stages (%s); use --agent <stage>=<agent>", role, strings.Join(match, ", "))})
		}
	}
	alias("--implement", "implementer", o.Implement)
	alias("--verify", "verifier", o.Verify)
	flagsFrom = FromTUI
	for _, a := range o.TUIAgents {
		if stage, spec, ok := strings.Cut(a, "="); ok {
			delete(boundBy, stage)
			set("setup", stage, spec)
		}
	}
	for stage, e := range o.TUIEfforts {
		for i := range stages {
			if stages[i].Name == stage && stages[i].Agent != "" {
				if !oneOf(e, KnownEfforts...) {
					probs = append(probs, Problem{"setup", "unknown effort; use one of " + strings.Join(KnownEfforts, ", ")})
				}
				stages[i].Effort = e
			}
		}
	}
	return probs
}

const (
	profilesArePersonal = "Claude profiles are personal; bind them with --agent or the user config (~/.config/oge/config.toml)"
	emptyProfile        = "empty profile name after @"
)

// bareAt reports a binding such as "claude@" or "claude@:sonnet".
func bareAt(spec string) bool {
	head, _, _ := strings.Cut(spec, ":")
	return strings.HasSuffix(head, "@")
}

// checkProfile is the Problem's message when agent@profile can't be
// bound, "" when it can. The typed name isn't echoed (#39).
func checkProfile(agent, profile string, known []string) string {
	switch {
	case profile == "":
		return ""
	case agent != "claude":
		return "profiles are supported for claude only"
	case oneOf(profile, known...):
		return ""
	case len(known) == 0:
		return "unknown Claude profile; none found (a profile is a ~/.claude-<name> directory holding a Claude config)"
	}
	return "unknown Claude profile; known: " + strings.Join(known, ", ")
}

func stageNames(stages []Stage) string {
	var n []string
	for _, s := range stages {
		n = append(n, s.Name)
	}
	return strings.Join(n, ", ")
}

// defaultBindings binds the implementer to the one installed agent when
// nothing bound it, and an unbound verifier to the implementer's agent and,
// unless one is configured, its model (always in a fresh Session).
func defaultBindings(stages []Stage, installed []string) []Problem {
	var impl *Stage
	for i := range stages {
		if stages[i].Role == "implementer" {
			impl = &stages[i]
		}
	}
	if impl.Agent == "" {
		switch len(installed) {
		case 1:
			impl.Agent, impl.Source = installed[0], FromInstalled
		case 0:
			return []Problem{{"--agent", fmt.Sprintf("no agent bound and none installed; install one of %s, or bind with --agent", strings.Join(KnownAgents, ", "))}}
		default:
			return []Problem{{"--agent", fmt.Sprintf("no agent bound and several installed (%s); choose with --agent <agent> or set pipelines.default.stages.implement.agent", strings.Join(installed, ", "))}}
		}
	}
	for i := range stages {
		if stages[i].Role == "verifier" && stages[i].Agent == "" {
			// Inherit only what isn't set: a configured verify model wins.
			stages[i].Agent, stages[i].Profile, stages[i].Source = impl.Agent, impl.Profile, FromInherited
			if stages[i].Model == "" {
				stages[i].Model = impl.Model
			}
			if stages[i].Effort == "" {
				stages[i].Effort = impl.Effort
			}
		}
	}
	// Effort is supported for claude and agy: refuse it rather than show
	// or record a level no launch applies (#179, ADR-0023).
	var probs []Problem
	for _, s := range stages {
		if s.Effort != "" && s.Agent != "claude" && s.Agent != "agy" {
			probs = append(probs, Problem{"stages." + s.Name + ".effort", "effort is supported for claude and agy only"})
		}
	}
	return probs
}

func resolveLimits(c LimitsConfig) Limits {
	l := DefaultLimits
	for _, i := range []struct {
		dst *int
		src *int
	}{
		{&l.Retries, c.Retries}, {&l.SendBacks, c.SendBacks}, {&l.UserRequests, c.UserRequests},
		{&l.Attempts, c.Attempts}, {&l.OracleGrowthAttempts, c.OracleGrowthAttempts},
		{&l.OracleGrowthPerAttempt, c.OracleGrowthPerAttempt}, {&l.OracleGrowthPerRun, c.OracleGrowthPerRun},
	} {
		if i.src != nil {
			*i.dst = *i.src
		}
	}
	if c.OracleGrowthAttempts == nil && l.OracleGrowthAttempts < l.SendBacks+1 {
		// A raised send-back budget raises the default with it.
		l.OracleGrowthAttempts = l.SendBacks + 1
	}
	if d, err := parseDuration(c.StageTimeout); err == nil {
		l.StageTimeout = d
	}
	if d, err := parseDuration(c.StageIdleTimeout); err == nil {
		l.StageIdleTimeout = d
	}
	if d, err := parseDuration(c.HostRequestTimeout); err == nil {
		l.HostRequestTimeout = d
	}
	return l
}

func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errors.New(`not a positive duration such as "10m" or "90s"`)
	}
	return d, nil
}

func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{{"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err == nil && n > 0 {
				return n * u.mult, nil
			}
		}
	}
	return 0, errors.New(`not a size such as "1MiB", "512KiB" or "4096B"`)
}
