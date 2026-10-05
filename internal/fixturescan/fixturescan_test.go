package fixturescan

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
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

// credentialKeys are field names whose value is a secret, in JSON
// ("key": "v"), query-string (key=v), TOML (key = "v") or YAML (key: v) form.
const credentialKeys = `access[_-]?token|id[_-]?token|refresh[_-]?token|api[_-]?key|password|passwd|session[_-]?key|client[_-]?secret`

var rules = []rule{
	{name: "anthropic-key", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)},
	{name: "sk-key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{name: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`)},
	{name: "slack-token", re: regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`)},
	{name: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
	{name: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{name: "private-key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{name: "bearer-token", re: regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]{20,}`)},
	{name: "basic-auth", re: regexp.MustCompile(`(?i)Authorization:\s*Basic\s+[A-Za-z0-9+/=]{8,}`)},
	{name: "cookie", re: regexp.MustCompile(`(?i)\b(?:Set-)?Cookie:\s*[^\s=;]+=[^\s;]{8,}`)},
	{
		name:    "credential-field",
		re:      regexp.MustCompile(`(?i)\b(?:` + credentialKeys + `)\b\\?["']?\s*[:=]\s*\\?["']?([^"'\s&,;\\}\])\x60]*)`),
		allowed: func(m []string) bool { return isPlaceholder(m[1]) },
	},
	{
		name:    "email",
		re:      regexp.MustCompile(`[A-Za-z0-9._%+-]+@((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,})\b`),
		allowed: func(m []string) bool { return allowedEmail(m[0], m[1]) },
	},
	{
		name:    "user-path",
		re:      regexp.MustCompile(`(?:/|\\/)(?:Users|home)(?:/|\\/)([A-Za-z0-9._-]+)`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
	{
		name:    "windows-user-path",
		re:      regexp.MustCompile(`(?i)\b[A-Z]:(?:\\{1,2}|/)Users(?:\\{1,2}|/)([A-Za-z0-9._-]+)`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
	{
		name:    "tilde-user-path",
		re:      regexp.MustCompile(`(?:^|[\s"'(=:,\x60])~([A-Za-z][A-Za-z0-9._-]*)/`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
	{name: "root-path", re: regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])/root/`)},
	{name: "macos-temp-path", re: regexp.MustCompile(`/var/folders/[A-Za-z0-9_]`)},
	{
		name:    "encoded-user-path",
		re:      regexp.MustCompile(`-(?:Users|home)-([A-Za-z0-9._]+)(?:[-/"'\x60]|$)`),
		allowed: func(m []string) bool { return placeholderUser[m[1]] },
	},
}

// placeholderUser are the user names fixtures and docs may use in home paths.
var placeholderUser = map[string]bool{"user": true, "USER": true}

func isPlaceholder(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 6 {
		return true // too short to be a real secret; also catches "", "x", "false"
	}
	for _, p := range []string{"<", "[", "$", "{", "%", "*", "...", "…"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	switch strings.ToLower(v) {
	case "redacted", "placeholder", "null", "false", "true", "undefined":
		return true
	}
	return false
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

// allow is one known-safe match. It names the file, the rule and the exact
// matched text, either literally or, for token-shaped values, by SHA-256 so
// this file never holds the value. Every entry carries its reason.
type allow struct {
	path, rule, match, sha256, reason string
}

var allowlist = []allow{
	{path: "internal/cli/testdata/dryrun/invalid-config/oge.toml", rule: "anthropic-key", sha256: "b31ccb10481bd65a4e40fb68ee1982849ef93890c9b5baac3d30d8fd47b3ae70", reason: "synthetic key: dry-run must not echo secrets from an invalid config"},
	{path: "internal/cli/testdata/dryrun/invalid-config/oge.toml", rule: "sk-key", sha256: "b31ccb10481bd65a4e40fb68ee1982849ef93890c9b5baac3d30d8fd47b3ae70", reason: "same synthetic key, seen by the generic sk- rule"},
	{path: "internal/cli/testdata/dryrun/invalid-config/oge.toml", rule: "github-token", sha256: "f419886e26c74511ac3a28b1f9f8a7dff3ad6196518b8445d5b441c565b917e2", reason: "synthetic token: dry-run must not echo secrets from an invalid config"},
	{path: "internal/cli/run_test.go", rule: "anthropic-key", sha256: "06a6e0d94d41ad892cefb2169795aa942a40d22fc7385e558498436e70d167fa", reason: "synthetic key: a run must never write the secret into its state"},
	{path: "internal/cli/run_test.go", rule: "sk-key", sha256: "06a6e0d94d41ad892cefb2169795aa942a40d22fc7385e558498436e70d167fa", reason: "synthetic key: a run must never write the secret into its state"},
	{path: "internal/pipeline/pipeline_test.go", rule: "anthropic-key", sha256: "f530817be7961ca3adbac0c73e19acd706a1845c4f4985035293e12f675906ee", reason: "synthetic credential: config values like this are rejected but never echoed"},
	{path: "internal/pipeline/pipeline_test.go", rule: "sk-key", sha256: "f530817be7961ca3adbac0c73e19acd706a1845c4f4985035293e12f675906ee", reason: "synthetic credential: config values like this are rejected but never echoed"},
	{path: "internal/pipeline/pipeline_test.go", rule: "github-token", sha256: "f419886e26c74511ac3a28b1f9f8a7dff3ad6196518b8445d5b441c565b917e2", reason: "synthetic credential: config values like this are rejected but never echoed"},
	{path: "internal/pipeline/pipeline_test.go", rule: "aws-access-key", sha256: "457643f44d19aed85fd756aa50cc0cd6b57376d4e8f5a72f9f85972a522002a3", reason: "synthetic credential: config values like this are rejected but never echoed"},
	{path: "internal/redact/redact_test.go", rule: "github-token", sha256: "f419886e26c74511ac3a28b1f9f8a7dff3ad6196518b8445d5b441c565b917e2", reason: "synthetic token: input for the redaction test"},
	{path: "internal/fixturescan/fixturescan_test.go", rule: "root-path", match: ")/root/", reason: "the root-path rule's own pattern"},
}

func allowed(path, ruleName, match string) bool {
	sum := sha256.Sum256([]byte(match))
	h := hex.EncodeToString(sum[:])
	for _, a := range allowlist {
		if a.path == path && a.rule == ruleName && (a.match == match || a.sha256 == h) {
			return true
		}
	}
	return false
}

type finding struct {
	line  int
	rule  string
	match string // never printed; used only for the allowlist
}

// scan returns every non-placeholder match, one per rule per distinct match
// per line.
func scan(r io.Reader) ([]finding, error) {
	var out []finding
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		for _, ru := range rules {
			seen := map[string]bool{}
			for _, m := range ru.re.FindAllStringSubmatch(line, -1) {
				if (ru.allowed == nil || !ru.allowed(m)) && !seen[m[0]] {
					seen[m[0]] = true
					out = append(out, finding{n, ru.name, m[0]})
				}
			}
		}
	}
	return out, sc.Err()
}

// skipped are tracked paths not scanned, each with its reason.
var skipped = []struct{ prefix, reason string }{
	{".agents/skills/", "vendored third-party skills (MIT), not Öge's text"},
	{".claude/skills/", "vendored third-party skills (MIT), not Öge's text"},
	{"go.sum", "module checksums only"},
}

func isSkipped(p string) bool {
	for _, s := range skipped {
		if p == s.prefix || strings.HasPrefix(p, s.prefix) {
			return true
		}
	}
	return false
}

// trackedFiles lists every file git tracks, relative to the module root.
func trackedFiles(t *testing.T) (string, []string) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v (this test needs a git checkout)", err)
	}
	root := strings.TrimSpace(string(out))
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && !isSkipped(p) {
			files = append(files, p)
		}
	}
	return root, files
}

func TestTrackedFilesHaveNoSecrets(t *testing.T) {
	root, files := trackedFiles(t)
	if len(files) < 50 {
		t.Fatalf("only %d tracked files found; the listing is broken", len(files))
	}
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted in the working tree but not yet committed
			}
			t.Fatal(err)
		}
		head := b
		if len(head) > 8000 {
			head = head[:8000]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			continue // binary
		}
		found, err := scan(bytes.NewReader(b))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		for _, fd := range found {
			if !allowed(rel, fd.rule, fd.match) {
				t.Errorf("%s:%d: %s (redact it, use a placeholder, or add a narrow allowlist entry with a reason)", rel, fd.line, fd.rule)
			}
		}
	}
}

func TestAllowlistEntriesAreUsed(t *testing.T) {
	root, _ := trackedFiles(t)
	for _, a := range allowlist {
		if a.reason == "" {
			t.Errorf("allowlist entry for %s/%s has no reason", a.path, a.rule)
		}
		b, err := os.ReadFile(filepath.Join(root, a.path))
		if err != nil {
			t.Errorf("allowlist entry for missing file %s", a.path)
			continue
		}
		found, _ := scan(bytes.NewReader(b))
		used := false
		for _, fd := range found {
			sum := sha256.Sum256([]byte(fd.match))
			used = used || (fd.rule == a.rule && (fd.match == a.match || hex.EncodeToString(sum[:]) == a.sha256))
		}
		if !used {
			t.Errorf("stale allowlist entry: %s %s (%s)", a.path, a.rule, a.reason)
		}
	}
}

func TestScanCatchesEachRule(t *testing.T) {
	// Built by concatenation so this file never holds a token-shaped literal.
	long := strings.Repeat("a1B2", 8)
	cases := map[string][]string{
		"anthropic-key":  {"key " + "sk-" + "ant-" + long},
		"sk-key":         {"key " + "sk-" + long},
		"github-token":   {"gh" + "p_" + long},
		"slack-token":    {"xo" + "xb-" + long},
		"aws-access-key": {"AK" + "IA" + "ABCDEFGHIJKLMNOP"},
		"google-api-key": {"AI" + "za" + long + "abc"},
		"jwt":            {"ey" + "J" + long + "." + long + "." + long},
		"private-key":    {"-----BEGIN " + "RSA PRIVATE KEY-----"},
		"bearer-token":   {"Authorization: Bear" + "er " + long},
		"basic-auth":     {"Authorization: Ba" + "sic " + long},
		"cookie":         {"Coo" + "kie: sid=" + long, "Set-Coo" + "kie: sid=" + long + "; Path=/"},
		"credential-field": {
			`{"refresh_` + `token":"` + long + `"}`,
			`https://x.test/cb?code=1&refresh_` + `token=` + long + `&y=2`,
			`refresh_` + `token = "` + long + `"`,
			`{\"refresh` + `Token\":\"` + long + `\"}`,
			`{"access_` + `token": "` + long + `"}`,
			`id_` + `token=` + long,
			`api_` + `key = "` + long + `"`,
			`"api` + `Key": "` + long + `"`,
			`pass` + `word: ` + long,
			`"session` + `Key":"` + long + `"`,
		},
		"email":             {"mail " + "someone" + "@" + "company.io"},
		"user-path":         {"/Us" + "ers/alice/dev", "/ho" + "me/alice/dev", `"\/Us` + `ers\/alice\/dev"`},
		"windows-user-path": {`C:\Us` + `ers\alice\dev`, `"C:\\Us` + `ers\\alice"`, `C:/Us` + `ers/alice`},
		"tilde-user-path":   {"cd ~" + "alice/dev"},
		"root-path":         {"cat /ro" + "ot/.ssh/id"},
		"macos-temp-path":   {"/private/var/fol" + "ders/xy/T/tmp"},
		"encoded-user-path": {"projects/-Us" + "ers-alice-dev-x", `"dir": "-Us` + `ers-alice"`, "-Us" + "ers-alice/x.jsonl", "-Us" + "ers-alice"},
	}
	for want, lines := range cases {
		for _, line := range lines {
			found, err := scan(strings.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			hit := false
			for _, f := range found {
				hit = hit || f.rule == want
			}
			if !hit {
				t.Errorf("rule %s did not fire on case %d of its kind", want, indexOf(lines, line))
			}
		}
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestScanAllowsPlaceholders(t *testing.T) {
	lines := []string{
		`"cwd": "/home/user/project", "transcript_path": "/home/user/.claude/projects/-home-user-project/x.jsonl"`,
		`"path": "-home-user-project"`,
		`Co-Authored-By: Claude <noreply@anthropic.com>`,
		`mail a@example.com or b@mail.example.org`,
		`{"refresh_token":"<redacted>"} {"refreshToken": false} api_key = "x"`,
		`password: $DB_PASSWORD, apiKey: "[REDACTED]"`,
		`"source": "example-plugin@builtin", "@anthropic-ai/sdk", "pkg@1.2.3"`,
		`task-list risk-free disk-image desk-lamp`,
		`~/dev/project and ~user/x`,
		`/rooted/path and chroot/x`,
	}
	for i, l := range lines {
		found, err := scan(strings.NewReader(l))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range found {
			t.Errorf("placeholder line %d tripped %s", i, f.rule)
		}
	}
}
