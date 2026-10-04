// Package pipeline turns a project's .oge/oge.toml plus CLI overrides into a
// frozen Pipeline: validated config and the compiled graph (ADR-0013).
package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// SchemaVersion is the newest .oge/oge.toml schema this binary understands.
const SchemaVersion = 1

// ConfigPath is where a project's config lives, relative to the repository
// root. It is read from the Snapshot.
const ConfigPath = ".oge/oge.toml"

// Problem is one validation error. Key names the config key or CLI flag;
// Msg names the rule broken. Neither ever contains a credential value.
type Problem struct {
	Key string
	Msg string
}

func (p Problem) String() string {
	if p.Key == "" {
		return p.Msg
	}
	return p.Key + ": " + p.Msg
}

// Config is the project file as written. Absent keys are zero values; the
// built-in defaults are applied by Resolve.
type Config struct {
	Schema    int           `toml:"schema"`
	Project   ProjectConfig `toml:"project"`
	Setup     SetupConfig   `toml:"setup"`
	Check     CheckConfig   `toml:"check"`
	Pipelines struct {
		Default PipelineConfig `toml:"default"`
	} `toml:"pipelines"`
}

type ProjectConfig struct {
	TestGlobs   []string `toml:"test_globs"`
	TestConfig  []string `toml:"test_config"`
	OutputGlobs []string `toml:"output_globs"`
	Probe       []string `toml:"probe"`
	PassEnv     []string `toml:"pass_env"`
}

type SetupConfig struct {
	Run     string `toml:"run"`
	Network string `toml:"network"`
}

type CheckConfig struct {
	Commands []CheckCommandConfig `toml:"commands"`
}

type CheckCommandConfig struct {
	Run           string `toml:"run"`
	Report        string `toml:"report"`
	ExpectedTests *int   `toml:"expected_tests"`
	Timeout       string `toml:"timeout"`
	OutputCap     string `toml:"output_cap"`
}

type PipelineConfig struct {
	Verify string `toml:"verify"`
	Gates  struct {
		Result *bool `toml:"result"`
	} `toml:"gates"`
	Limits LimitsConfig `toml:"limits"`
	Stages struct {
		Implement StageConfig `toml:"implement"`
		Verify    StageConfig `toml:"verify"`
	} `toml:"stages"`
}

type StageConfig struct {
	Role    string `toml:"role"`
	Agent   string `toml:"agent"`
	Model   string `toml:"model"`
	Network string `toml:"network"`
}

// LimitsConfig overrides the provisional built-in limits (see DefaultLimits).
type LimitsConfig struct {
	Retries                *int   `toml:"retries"`
	SendBacks              *int   `toml:"send_backs"`
	UserRequests           *int   `toml:"user_requests"`
	Attempts               *int   `toml:"attempts"`
	OracleGrowthAttempts   *int   `toml:"oracle_growth_attempts"`
	OracleGrowthPerAttempt *int   `toml:"oracle_growth_per_attempt"`
	OracleGrowthPerRun     *int   `toml:"oracle_growth_per_run"`
	StageTimeout           string `toml:"stage_timeout"`
	StageIdleTimeout       string `toml:"stage_idle_timeout"`
	HostRequestTimeout     string `toml:"host_request_timeout"`
}

// mandatoryGates are the Gates no config may configure or remove (ADR-0008,
// ADR-0013), keyed by their config spelling.
var mandatoryGates = map[string]string{
	"tamper":           "tamper",
	"infeasible":       "infeasible",
	"bound_exhaustion": "bound-exhaustion",
	"oracle_growth":    "Oracle-growth",
	"own_test_failure": "Own-test-failure",
	"ambiguous_file":   "Ambiguous-file",
}

var (
	credentialKey = regexp.MustCompile(`(?i)(api_?key|token|secret|passw(or)?d|credential|auth|config_dir|codex_home)`)
	// credentialValue matches strings that carry a credential or point Öge at
	// a credential store. Matches are reported by key only, never echoed.
	credentialValue = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}|\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_|\bglpat-|\bxox[abpr]-|\bAKIA[0-9A-Z]{16}\b|-----BEGIN [A-Z ]*PRIVATE KEY|\b[A-Z][A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD|ACCESS_KEY)[A-Z0-9_]*=\S|\b(?:CLAUDE_CONFIG_DIR|CODEX_HOME)=|\.credentials\.json|\.codex/auth\.json`)
	envName         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const credentialRule = "looks like a credential or credential-store setting; Öge never takes credentials in config (ADR-0006). To expose a variable to Checks, put its name (never its value) in project.pass_env"

// Load parses a project config strictly. It returns every problem it finds,
// not just the first.
func Load(data []byte) (*Config, []Problem) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return nil, []Problem{{Msg: fmt.Sprintf("not valid TOML: line %d: %s", perr.Position.Line, perr.Message)}}
		}
		return nil, []Problem{{Msg: "not valid TOML: " + err.Error()}}
	}

	var probs []Problem
	switch v, ok := raw["schema"]; {
	case !ok:
		probs = append(probs, Problem{"schema", fmt.Sprintf("required; add schema = %d", SchemaVersion)})
	case !isInt(v):
		probs = append(probs, Problem{"schema", "must be an integer"})
	case v.(int64) > SchemaVersion:
		// A newer schema may use keys this binary doesn't know; listing them
		// would only be noise.
		return nil, []Problem{{"schema", fmt.Sprintf("%d is newer than this oge understands (schema %d); upgrade oge", v.(int64), SchemaVersion)}}
	case v.(int64) < 1:
		probs = append(probs, Problem{"schema", fmt.Sprintf("%d is not a valid schema; use schema = %d", v.(int64), SchemaVersion)})
	}

	probs = append(probs, scanCredentials("", raw)...)

	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		// Types that don't match the schema, e.g. a string where a list goes.
		return nil, append(probs, Problem{Msg: typeError(err)})
	}
	probs = append(probs, undecoded(md.Undecoded())...)
	probs = append(probs, cfg.validate()...)
	if len(probs) > 0 {
		return nil, probs
	}
	return &cfg, nil
}

func isInt(v any) bool { _, ok := v.(int64); return ok }

func typeError(err error) string {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		return fmt.Sprintf("wrong type: line %d: %s", perr.Position.Line, perr.Message)
	}
	return "wrong type: " + err.Error()
}

func scanCredentials(prefix string, v any) []Problem {
	var probs []Problem
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			probs = append(probs, scanCredentials(join(prefix, k), v[k])...)
		}
	case []map[string]any:
		for i, e := range v {
			probs = append(probs, scanCredentials(fmt.Sprintf("%s[%d]", prefix, i), e)...)
		}
	case []any:
		for i, e := range v {
			probs = append(probs, scanCredentials(fmt.Sprintf("%s[%d]", prefix, i), e)...)
		}
	case string:
		if credentialValue.MatchString(v) {
			probs = append(probs, Problem{prefix, credentialRule})
		}
	}
	return probs
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

// undecoded turns unknown keys into problems, with a specific rule where the
// key is a known trust mistake rather than a typo.
func undecoded(keys []toml.Key) []Problem {
	seen := map[string]bool{}
	var probs []Problem
	for _, k := range keys {
		s := k.String()
		parentReported := false
		for i := 1; i < len(k); i++ {
			if seen[k[:i].String()] {
				parentReported = true
			}
		}
		seen[s] = true
		if parentReported {
			continue
		}
		probs = append(probs, Problem{s, unknownKeyRule(k)})
	}
	return probs
}

func unknownKeyRule(k toml.Key) string {
	last := k[len(k)-1]
	switch {
	case len(k) == 2 && k[0] == "pipelines":
		return "only the built-in default Pipeline exists"
	case len(k) == 4 && k[0] == "pipelines" && k[2] == "gates":
		if name, ok := mandatoryGates[last]; ok {
			return fmt.Sprintf("the %s Gate is mandatory and can't be configured or removed (ADR-0008)", name)
		}
		return "unknown Gate; only the Result gate can be configured (gates.result)"
	case len(k) == 4 && k[0] == "pipelines" && k[2] == "stages":
		return "the Stage list is fixed: implement and verify; custom Stages aren't supported"
	case len(k) == 5 && k[0] == "pipelines" && k[2] == "stages" && isReuseKey(last):
		if k[3] == "verify" {
			return "a verifier always gets a fresh Session without user-global instructions; it must not inherit the implementer's context (ADR-0013)"
		}
		return "Session reuse and user-global instructions aren't supported"
	case credentialKey.MatchString(last):
		return credentialRule
	}
	return "unknown key"
}

func isReuseKey(k string) bool {
	switch k {
	case "session", "session_reuse", "reuse_session", "user_global_instructions":
		return true
	}
	return false
}

// roleKinds are the Role kinds Öge defines; a Stage name never confers one.
var roleKinds = map[string]bool{
	"planner": true, "implementer": true, "verifier": true, "reviewer": true,
	"challenger": true, "decider": true, "advisor": true,
}

// KnownAgents are the agent names a Stage may bind.
var KnownAgents = []string{"claude", "codex"}

func knownAgent(a string) bool {
	for _, k := range KnownAgents {
		if a == k {
			return true
		}
	}
	return false
}

// KnownReports are the structured-report formats Öge parses itself.
var KnownReports = []string{"go-test-json", "junit"}

func (c *Config) validate() []Problem {
	var probs []Problem
	add := func(key, format string, a ...any) { probs = append(probs, Problem{key, fmt.Sprintf(format, a...)}) }

	for i, name := range c.Project.PassEnv {
		if !envName.MatchString(name) {
			add(fmt.Sprintf("project.pass_env[%d]", i), "must be a variable name, never a value (ADR-0006)")
		}
	}
	if !oneOf(c.Setup.Network, "", "off", "on") {
		add("setup.network", `must be "off" or "on"`)
	}
	for i, cmd := range c.Check.Commands {
		key := fmt.Sprintf("check.commands[%d]", i)
		if strings.TrimSpace(cmd.Run) == "" {
			add(key+".run", "required: the command a Check runs")
		}
		if cmd.Report != "" && !oneOf(cmd.Report, KnownReports...) {
			add(key+".report", "unknown report format %q; use one of %s", cmd.Report, strings.Join(KnownReports, ", "))
		}
		if cmd.ExpectedTests != nil && *cmd.ExpectedTests < 1 {
			add(key+".expected_tests", "must be at least 1")
		}
		if cmd.Timeout != "" {
			if _, err := parseDuration(cmd.Timeout); err != nil {
				add(key+".timeout", "%v", err)
			}
		}
		if cmd.OutputCap != "" {
			if _, err := parseSize(cmd.OutputCap); err != nil {
				add(key+".output_cap", "%v", err)
			}
		}
	}

	p := c.Pipelines.Default
	if !oneOf(p.Verify, "", "after", "before") {
		add("pipelines.default.verify", `must be "after" or "before"`)
	}
	probs = append(probs, p.Limits.validate()...)
	probs = append(probs, p.Stages.Implement.validate("implement", "implementer")...)
	probs = append(probs, p.Stages.Verify.validate("verify", "verifier")...)
	if n := p.Stages.Verify.Network; n != "" && n != "off" {
		add("pipelines.default.stages.verify.network", "a verifier never gets network; judged roles are always off (ADR-0013)")
	}
	return probs
}

func (s StageConfig) validate(name, role string) []Problem {
	var probs []Problem
	key := "pipelines.default.stages." + name
	switch {
	case s.Role == "" || s.Role == role:
	case !roleKinds[s.Role]:
		probs = append(probs, Problem{key + ".role", fmt.Sprintf("unknown Role kind %q", s.Role)})
	default:
		probs = append(probs, Problem{key + ".role", fmt.Sprintf("Stage %s has the fixed Role kind %s", name, role)})
	}
	if s.Agent != "" && !knownAgent(s.Agent) {
		probs = append(probs, Problem{key + ".agent", fmt.Sprintf("unknown agent %q; use one of %s", s.Agent, strings.Join(KnownAgents, ", "))})
	}
	if name == "implement" && !oneOf(s.Network, "", "off", "on") {
		probs = append(probs, Problem{key + ".network", `must be "off" or "on"`})
	}
	return probs
}

func (l LimitsConfig) validate() []Problem {
	var probs []Problem
	ints := []struct {
		key string
		v   *int
		min int
	}{
		{"retries", l.Retries, 0},
		{"send_backs", l.SendBacks, 0},
		{"user_requests", l.UserRequests, 0},
		{"attempts", l.Attempts, 1},
		{"oracle_growth_attempts", l.OracleGrowthAttempts, 1},
		{"oracle_growth_per_attempt", l.OracleGrowthPerAttempt, 1},
		{"oracle_growth_per_run", l.OracleGrowthPerRun, 1},
	}
	for _, i := range ints {
		if i.v != nil && *i.v < i.min {
			probs = append(probs, Problem{"pipelines.default.limits." + i.key, fmt.Sprintf("must be at least %d", i.min)})
		}
	}
	for key, v := range map[string]string{"stage_timeout": l.StageTimeout, "stage_idle_timeout": l.StageIdleTimeout} {
		if v != "" {
			if _, err := parseDuration(v); err != nil {
				probs = append(probs, Problem{"pipelines.default.limits." + key, err.Error()})
			}
		}
	}
	if v := l.HostRequestTimeout; v != "" && v != "off" {
		if _, err := parseDuration(v); err != nil {
			probs = append(probs, Problem{"pipelines.default.limits.host_request_timeout", err.Error() + `, or "off"`})
		}
	}
	sort.Slice(probs, func(i, j int) bool { return probs[i].Key < probs[j].Key })
	return probs
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
