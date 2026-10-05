package oracle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/pipeline"
)

func TestCheckEnvironmentIsPrivate(t *testing.T) {
	blobs, err := ledger.OpenBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/host/home", "GOWORK": "/host/go.work", "FOO": "bar"}
	r := &Runner{Blobs: blobs, PassEnv: []string{"HOME", "GOWORK", "FOO"}, Getenv: func(k string) string { return host[k] }}
	dir, env, err := r.Prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if env["GOWORK"] != "off" {
		t.Errorf("GOWORK = %q, want off", env["GOWORK"])
	}
	for k := range env {
		if k != "PATH" && !contains(pipeline.CheckPrivateEnv, k) {
			t.Errorf("%s is fixed by Prepare but pass_env may name it", k)
		}
	}
	e, _, err := r.Exec(context.Background(), `printf '%s|%s|%s' "$HOME" "$GOWORK" "$FOO" >&2`, dir, env, time.Minute, 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	b, err := blobs.Get(e.Stderr.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if want := env["HOME"] + "|off|bar"; string(b) != want {
		t.Errorf("command saw %q, want %q", b, want)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
