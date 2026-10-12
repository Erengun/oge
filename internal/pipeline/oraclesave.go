package pipeline

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

var (
	testGlobsLine = regexp.MustCompile(`^\s*test_globs\s*=`)
	checkHeader   = regexp.MustCompile(`^\s*\[\[\s*check\.commands\s*\]\]`)
	tableHeader   = regexp.MustCompile(`^\s*\[`)
	projectHeader = regexp.MustCompile(`^\s*\[\s*project\s*\]`)
)

// RenderOracle is the project config text with its Check commands and test
// globs replaced by the given ones (#132). It changes only those two keys:
// everything else, comments included, stays as written. A key whose value
// is already the given one is not touched. existing must be a valid config.
//
// The result is read back with Load and must hold exactly the given values;
// a layout this edit can't handle (dotted keys, inline tables) is an error,
// so a file that would not mean what the human reviewed is never offered.
func RenderOracle(existing string, checks []CheckCommandConfig, globs []string) (string, error) {
	crlf := strings.Contains(existing, "\r\n")
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	cfg, probs := Load([]byte(existing))
	if len(probs) > 0 || cfg == nil {
		cfg = &Config{}
	}
	lines := strings.Split(strings.TrimRight(existing, "\n"), "\n")
	if strings.TrimSpace(existing) == "" {
		lines = nil
	}
	if !equalStrings(cfg.Project.TestGlobs, globs) {
		lines = setTestGlobs(lines, globs)
	}
	if !sameChecks(cfg.Check.Commands, checks) {
		lines = setChecks(lines, checks)
	}
	out := strings.Join(lines, "\n") + "\n"
	got, probs := Load([]byte(out))
	if len(probs) > 0 || got == nil || !equalStrings(got.Project.TestGlobs, globs) || !sameChecks(got.Check.Commands, checks) {
		return "", fmt.Errorf("this file's layout needs a hand edit: %s", ConfigPath)
	}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
}

func equalStrings(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func sameChecks(a, b []CheckCommandConfig) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func setTestGlobs(lines, globs []string) []string {
	quoted := make([]string, len(globs))
	for i, g := range globs {
		quoted[i] = tomlString(g)
	}
	line := "test_globs = [" + strings.Join(quoted, ", ") + "]"
	section := ""
	for i, l := range lines {
		if tableHeader.MatchString(l) {
			section = "other"
			if projectHeader.MatchString(l) {
				section = "project"
			}
		}
		if section == "project" && testGlobsLine.MatchString(l) {
			end := i
			for depth := bracketDepth(l); depth > 0 && end+1 < len(lines); {
				end++
				depth += bracketDepth(lines[end])
			}
			out := append([]string(nil), lines[:i]...)
			out = append(out, line)
			return append(out, lines[end+1:]...)
		}
	}
	for i, l := range lines {
		if projectHeader.MatchString(l) {
			out := append([]string(nil), lines[:i+1]...)
			out = append(out, line)
			return append(out, lines[i+1:]...)
		}
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return append(lines, "[project]", line)
}

// bracketDepth is how many arrays a line leaves open, ignoring strings and
// comments.
func bracketDepth(l string) int {
	depth, inStr := 0, false
	for i := 0; i < len(l); i++ {
		switch c := l[i]; {
		case inStr && c == '\\':
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '#':
			return depth
		case c == '[':
			depth++
		case c == ']':
			depth--
		}
	}
	return depth
}

func setChecks(lines []string, checks []CheckCommandConfig) []string {
	var out []string
	at := -1 // where the first removed block was
	for i := 0; i < len(lines); i++ {
		if !checkHeader.MatchString(lines[i]) {
			out = append(out, lines[i])
			continue
		}
		if at < 0 {
			at = len(out)
		}
		start := i
		for i+1 < len(lines) && !tableHeader.MatchString(lines[i+1]) {
			i++
		}
		// Comments that end the block belong to what follows it.
		end := i
		for end > start && (strings.TrimSpace(lines[end]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end]), "#")) {
			end--
		}
		for _, l := range lines[end+1 : i+1] {
			if strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		}
	}
	var blocks []string
	for _, c := range checks {
		blocks = append(blocks, "", "[[check.commands]]", "run = "+tomlString(c.Run))
		if c.Report != "" {
			blocks = append(blocks, "report = "+tomlString(c.Report))
		}
		if c.ExpectedTests != nil {
			blocks = append(blocks, fmt.Sprintf("expected_tests = %d", *c.ExpectedTests))
		}
		if c.Timeout != "" {
			blocks = append(blocks, "timeout = "+tomlString(c.Timeout))
		}
		if c.OutputCap != "" {
			blocks = append(blocks, "output_cap = "+tomlString(c.OutputCap))
		}
	}
	if at < 0 {
		at = len(out)
	}
	if at == 0 && len(blocks) > 0 {
		blocks = blocks[1:] // no blank line at the top of the file
	}
	res := append([]string(nil), out[:at]...)
	res = append(res, blocks...)
	if at < len(out) && len(blocks) > 0 {
		res = append(res, "")
	}
	res = append(res, out[at:]...)
	var tidy []string // no double blank lines
	for _, l := range res {
		if n := len(tidy); n > 0 && l == "" && tidy[n-1] == "" {
			continue
		}
		tidy = append(tidy, l)
	}
	return tidy
}

// tomlString is s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
