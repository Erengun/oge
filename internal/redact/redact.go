// Package redact applies Öge's known-pattern redaction before anything is
// persisted (ADR-0011). It is best effort: patterns cannot catch arbitrary
// secrets.
package redact

import "regexp"

type rule struct {
	name string
	re   *regexp.Regexp
}

var rules = []rule{
	{"anthropic-or-openai-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`)},
	{"slack-token", regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}`)},
	{"aws-access-key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|\z)`)},
	{"secret-assignment", regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD|ACCESS_KEY)[A-Z0-9_]*=)\S+`)},
}

// Rules names every rule Redact applies, for the Evidence.
func Rules() []string {
	names := make([]string, len(rules))
	for i, r := range rules {
		names[i] = r.name
	}
	return names
}

// Redact replaces every match with [REDACTED:<rule>].
func Redact(b []byte) []byte {
	for _, r := range rules {
		repl := []byte("[REDACTED:" + r.name + "]")
		if r.name == "secret-assignment" {
			repl = []byte("${1}[REDACTED:" + r.name + "]")
		}
		b = r.re.ReplaceAll(b, repl)
	}
	return b
}
