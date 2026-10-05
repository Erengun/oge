package fixturescan

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type rule struct {
	name string
	re   *regexp.Regexp
	// allowed reports whether a match is a known placeholder. nil means
	// every match is a finding.
	allowed func(match []string) bool
}

var rules = []rule{
	{name: "anthropic-key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)},
	{name: "sk-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{name: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`)},
	{name: "slack-token", re: regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`)},
	{name: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{name: "private-key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{name: "bearer-token", re: regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{
		name:    "refresh-token",
		re:      regexp.MustCompile(`(?i)refresh_?token\\?"\s*:\s*\\?"([^"\\]*)`),
		allowed: func(m []string) bool { return isPlaceholder(m[1]) },
	},
	{
		name:    "email",
		re:      regexp.MustCompile(`[A-Za-z0-9._%+-]+@((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,})\b`),
		allowed: func(m []string) bool { return allowedEmail(m[0], m[1]) },
	},
	{
		name:    "home-path",
		re:      regexp.MustCompile(`/(?:Users|home)/([A-Za-z0-9._-]+)`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
	{
		name:    "encoded-home-path",
		re:      regexp.MustCompile(`-(?:Users|home)-([A-Za-z0-9._]+)-`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
}

// syntheticSecrets are fixtures that hold token-shaped strings on purpose,
// to prove Öge never echoes them. Credential rules skip these files; the
// email and home-path rules still apply.
var syntheticSecrets = map[string]string{
	"internal/cli/testdata/dryrun/invalid-config/oge.toml": "dry-run must not echo secrets from an invalid config",
}

var credentialRule = map[string]bool{
	"anthropic-key": true, "sk-key": true, "github-token": true, "slack-token": true,
	"aws-access-key": true, "jwt": true, "private-key": true, "bearer-token": true, "refresh-token": true,
}

// placeholderUser are the user names fixtures may use in home paths.
var placeholderUser = map[string]bool{"user": true, "USER": true}

func isPlaceholder(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "[") ||
		strings.EqualFold(v, "redacted") || strings.EqualFold(v, "placeholder")
}

func allowedEmail(addr, domain string) bool {
	addr, domain = strings.ToLower(addr), strings.ToLower(domain)
	switch {
	case addr == "noreply@anthropic.com":
		return true
	case domain == "users.noreply.github.com":
		return true
	}
	for _, d := range []string{"example.com", "example.org", "example.net"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

type finding struct {
	line int
	rule string
}

// scan returns one finding per rule per line. It never returns the match.
func scan(r io.Reader) ([]finding, error) {
	var out []finding
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		for _, ru := range rules {
			for _, m := range ru.re.FindAllStringSubmatch(line, -1) {
				if ru.allowed == nil || !ru.allowed(m) {
					out = append(out, finding{n, ru.name})
					break
				}
			}
		}
	}
	return out, sc.Err()
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// scannedFiles lists every file under a testdata directory and under
// docs/research, skipping hidden directories such as .git and .claude.
func scannedFiles(t *testing.T, root string) []string {
	t.Helper()
	research := filepath.Join(root, "docs", "research")
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		inTestdata := false
		for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
			if part == "testdata" {
				inTestdata = true
				break
			}
		}
		if inTestdata || strings.HasPrefix(p, research+string(filepath.Separator)) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommittedFixturesHaveNoSecrets(t *testing.T) {
	root := moduleRoot(t)
	files := scannedFiles(t, root)
	if len(files) == 0 {
		t.Fatal("no fixture or research files found; the walk is broken")
	}
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		found, err := scan(f)
		f.Close()
		rel, _ := filepath.Rel(root, p)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		for _, fd := range found {
			if _, ok := syntheticSecrets[filepath.ToSlash(rel)]; ok && credentialRule[fd.rule] {
				continue
			}
			t.Errorf("%s:%d: %s (redact it or replace it with a placeholder)", rel, fd.line, fd.rule)
		}
	}
}

func TestScanCatchesEachRule(t *testing.T) {
	// Built by concatenation so this file never holds a token-shaped literal.
	long := strings.Repeat("a1B2", 8)
	cases := map[string]string{
		"anthropic-key":     "key " + "sk-" + "ant-" + long,
		"sk-key":            "key " + "sk-" + long,
		"github-token":      "gh" + "p_" + long,
		"slack-token":       "xo" + "xb-" + long,
		"aws-access-key":    "AK" + "IA" + "ABCDEFGHIJKLMNOP",
		"jwt":               "ey" + "J" + long + "." + long + "." + long,
		"private-key":       "-----BEGIN " + "RSA PRIVATE KEY-----",
		"bearer-token":      "Authorization: Bear" + "er " + long,
		"refresh-token":     `{"refresh_` + `token":"` + long + `"}`,
		"email":             "mail " + "someone" + "@" + "company.io",
		"home-path":         "/Us" + "ers/alice/dev",
		"encoded-home-path": "projects/-Us" + "ers-alice-dev-x",
	}
	for want, line := range cases {
		found, err := scan(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		hit := false
		for _, f := range found {
			hit = hit || f.rule == want
		}
		if !hit {
			t.Errorf("rule %s did not fire", want)
		}
	}
}

func TestScanAllowsPlaceholders(t *testing.T) {
	lines := []string{
		`"cwd": "/home/user/project", "transcript_path": "/home/user/.claude/projects/-home-user-project/x.jsonl"`,
		`Co-Authored-By: Claude <noreply@anthropic.com>`,
		`mail a@example.com or b@mail.example.org`,
		`{"refresh_token":"<redacted>"} {"refreshToken": false}`,
		`"source": "example-plugin@builtin", "@anthropic-ai/sdk", "pkg@1.2.3"`,
		`task-list risk-free disk-image desk-lamp`,
	}
	for _, l := range lines {
		found, err := scan(strings.NewReader(l))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			t.Errorf("placeholder line tripped %s: %q", f.rule, l)
		}
	}
}
