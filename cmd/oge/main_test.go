package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsStampedVersion(t *testing.T) {
	old := version
	version = "1.2.3"
	t.Cleanup(func() { version = old })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got, want := stdout.String(), "oge 1.2.3\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestUnknownFlagIsARefusal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no-such-flag") {
		t.Fatalf("stderr does not name the flag: %q", stderr.String())
	}
}
