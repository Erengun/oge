package redact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	in := "token ghp_abcdefghijklmnopqrstuvwxyz0123 and MY_API_KEY=hunter2 ok"
	got := string(Redact([]byte(in)))
	if strings.Contains(got, "ghp_abc") || strings.Contains(got, "hunter2") || !strings.Contains(got, "MY_API_KEY=[REDACTED") || !strings.HasSuffix(got, " ok") {
		t.Fatalf("got %q", got)
	}
}
