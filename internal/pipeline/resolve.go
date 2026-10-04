package pipeline

import (
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
	Retries:                2,
	SendBacks:              3,
	UserRequests:           3,
	Attempts:               12,
	OracleGrowthAttempts:   3,
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
	FromBuiltin   Source = "built-in"
	FromProject   Source = "project"
	FromCLI       Source = "cli"
	FromInstalled Source = "installed"
	FromInherited Source = "inherits implement"
)

// Overrides are the per-Run CLI flags.
type Overrides struct {
	Checks    []string
	Tests     []string
	Outputs   []string
	Agents    []string // --agent <stage>=<agent>[:<model>] or <agent>[:<model>]
	Implement string   // alias: --implement <agent>[:<model>]
	Verify    string   // alias: --verify <agent>[:<model>]
	Fast      bool
	Blind     bool
	Confirm   bool
	Require   []string
}

// Frozen is the resolved Pipeline a Run would freeze.
type Frozen struct {
	Mode           Mode
	Verify         string // "after", "before", or "" in Fast mode
	Project        ProjectConfig
	Setup          SetupConfig
	Checks         []CheckCommand
	Stages         []Stage
	ResultGate     bool
	Limits         Limits
	Graph          Graph
	Hash           string
	TrustWeakening []Weakening
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
	Model   string
	Network string
	Source  Source
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
// PATH, used only when nothing is bound.
func Resolve(cfg *Config, o Overrides, installed []string) (*Frozen, []Problem) {
	src := FromProject
	if cfg == nil {
		cfg = &Config{Schema: SchemaVersion}
		src = FromBuiltin
	}
	var probs []Problem
	add := func(key, format string, a ...any) { probs = append(probs, Problem{key, fmt.Sprintf(format, a...)}) }
	p := cfg.Pipelines.Default
	f := &Frozen{Project: cfg.Project, Setup: cfg.Setup}

	// Mode.
	switch {
	case o.Fast && o.Blind:
		add("--fast, --blind", "choose one mode")
	case o.Fast:
		f.Mode = Fast
	case o.Blind:
		f.Mode, f.Verify = Blind, "before"
	case p.Verify == "before":
		f.Mode, f.Verify = Blind, "before"
	default:
		f.Mode, f.Verify = Standard, "after"
	}
	for _, g := range o.Require {
		if !oneOf(g, KnownGuarantees...) {
			add("--require "+g, "unknown guarantee; known: %s", strings.Join(KnownGuarantees, ", "))
		} else if f.Mode != "" && !oneOf(g, modeGuarantees[f.Mode]...) {
			add("--require "+g, "%s mode doesn't give this guarantee; use Standard (the default) or --blind", f.Mode)
		}
	}

	// Oracle: CLI flags add to the project's, never remove from it.
	// TODO(#39-decision): --check/--tests/--output append to the project's
	// values (trust-conservative: a flag can't drop an Oracle command) rather
	// than replace them.
	f.Project.TestGlobs = appendCopy(cfg.Project.TestGlobs, o.Tests)
	f.Project.OutputGlobs = appendCopy(cfg.Project.OutputGlobs, o.Outputs)
	for _, c := range cfg.Check.Commands {
		f.Checks = append(f.Checks, resolveCheck(c, src))
	}
	for _, run := range o.Checks {
		f.Checks = append(f.Checks, resolveCheck(CheckCommandConfig{Run: run, Report: inferReport(run)}, FromCLI))
	}
	if len(f.Checks) == 0 {
		add("check.commands", "no Check command: a Run can't accept anything without one. Add [[check.commands]] to %s or pass --check", ConfigPath)
	}
	if len(f.Project.TestGlobs) == 0 {
		add("project.test_globs", "no test globs: the Oracle's tests must be named. Set project.test_globs in %s or pass --tests", ConfigPath)
	}
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
	impl.Agent, impl.Model = p.Stages.Implement.Agent, p.Stages.Implement.Model
	if p.Stages.Implement.Network != "" {
		impl.Network = p.Stages.Implement.Network
	}
	if impl.Agent == "" {
		impl.Source = ""
	}
	ver := Stage{Name: "verify", Role: "verifier", Network: "off", Source: src}
	ver.Agent, ver.Model = p.Stages.Verify.Agent, p.Stages.Verify.Model
	if ver.Agent == "" {
		ver.Source = ""
	}
	switch f.Mode {
	case Fast:
		f.Stages = []Stage{impl}
	case Blind:
		f.Stages = []Stage{ver, impl}
	default:
		f.Stages = []Stage{impl, ver}
	}
	probs = append(probs, bind(f.Stages, o)...)
	if f.Mode != "" {
		probs = append(probs, defaultBindings(f.Stages, installed)...)
	}

	// Gates and limits.
	f.ResultGate = o.Confirm || (p.Gates.Result != nil && *p.Gates.Result)
	f.Limits = resolveLimits(p.Limits)

	// Trust-weakening options: from the project file only (there is no user
	// config and no CLI network flag in the MVP).
	if cfg.Setup.Network == "on" {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"setup network = on", FromProject, fmt.Sprintf("setup = %q", cfg.Setup.Run)})
	}
	if impl.Network == "on" {
		f.TrustWeakening = append(f.TrustWeakening, Weakening{"implement network = on", FromProject, ""})
	}

	if len(probs) > 0 {
		return nil, probs
	}
	f.Graph = Compile(f.Mode, f.ResultGate)
	f.Hash = f.Graph.Hash(f.Limits)
	return f, nil
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
func bind(stages []Stage, o Overrides) []Problem {
	var probs []Problem
	boundBy := map[string]string{}
	set := func(flag, stage, spec string) {
		agent, model, _ := strings.Cut(spec, ":")
		if !knownAgent(agent) {
			probs = append(probs, Problem{flag, fmt.Sprintf("unknown agent %q; use one of %s", agent, strings.Join(KnownAgents, ", "))})
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
			stages[i].Agent, stages[i].Model, stages[i].Source = agent, model, FromCLI
			return
		}
		probs = append(probs, Problem{flag, fmt.Sprintf("no Stage %q in this mode (Stages: %s)", stage, stageNames(stages))})
	}
	for _, a := range o.Agents {
		flag := "--agent " + a
		if stage, spec, ok := strings.Cut(a, "="); ok {
			set(flag, stage, spec)
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
			set(flag+" "+spec, match[0], spec)
		case 0:
			probs = append(probs, Problem{flag, fmt.Sprintf("no %s Stage in this mode; use --agent <stage>=<agent>", role)})
		default:
			probs = append(probs, Problem{flag, fmt.Sprintf("ambiguous: several %s Stages (%s); use --agent <stage>=<agent>", role, strings.Join(match, ", "))})
		}
	}
	alias("--implement", "implementer", o.Implement)
	alias("--verify", "verifier", o.Verify)
	return probs
}

func stageNames(stages []Stage) string {
	var n []string
	for _, s := range stages {
		n = append(n, s.Name)
	}
	return strings.Join(n, ", ")
}

// defaultBindings binds the implementer to the one installed agent when
// nothing bound it, and the verifier to the implementer's agent and model
// (always in a fresh Session).
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
			stages[i].Agent, stages[i].Model, stages[i].Source = impl.Agent, impl.Model, FromInherited
		}
	}
	return nil
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
		return 0, fmt.Errorf("%q is not a positive duration such as \"10m\" or \"90s\"", s)
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
	return 0, fmt.Errorf("%q is not a size such as \"1MiB\", \"512KiB\" or \"4096B\"", s)
}
