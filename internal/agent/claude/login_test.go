package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseAuthStatus(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      Login
		method    string
	}{
		{"logged in", `{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty"}`, LoggedIn, "claude.ai"},
		{"logged out", `{"loggedIn": false, "authMethod": "none", "apiProvider": "firstParty"}` + "\n", LoggedOut, ""},
		{"no field", `{"authMethod": "claude.ai"}`, LoginUnknown, ""},
		{"garbage", `error: unknown command 'auth'`, LoginUnknown, ""},
		{"empty", ``, LoginUnknown, ""},
	} {
		got, method := parseAuthStatus([]byte(tc.out))
		if got != tc.want || method != tc.method {
			t.Errorf("%s: got %v %q, want %v %q", tc.name, got, method, tc.want, tc.method)
		}
	}
}

// fakeAdapter runs the fake claude, built once, with the given environment.
func fakeAdapter(t *testing.T, env ...string) *Adapter {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "claude")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", exe, "./fakeclaude").CombinedOutput(); err != nil {
		t.Fatalf("building the fake claude: %v\n%s", err, out)
	}
	return &Adapter{Path: exe, Environ: func() []string { return append(os.Environ(), env...) }}
}

func TestLoginStatusPerProfile(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, ".claude-work")
	a := fakeAdapter(t, "OGE_FAKE_CLAUDE_LOGINS="+work, "CLAUDE_CONFIG_DIR=")
	ctx := context.Background()
	got := a.LoginStatuses(ctx, []Profile{{Dir: filepath.Join(home, ".claude")}, {Name: "work", Dir: work}})
	if got[0] != LoggedOut || got[1] != LoggedIn {
		t.Errorf("got %v, want [LoggedOut LoggedIn]", got)
	}
}

func TestLoginStatusUnsupportedMakesNoClaim(t *testing.T) {
	a := fakeAdapter(t, "OGE_FAKE_CLAUDE_LOGINS=unsupported")
	if got := a.LoginStatus(context.Background(), Profile{}); got != LoginUnknown {
		t.Errorf("got %v, want LoginUnknown", got)
	}
}

func TestLoginStatusMissingClaudeMakesNoClaim(t *testing.T) {
	a := &Adapter{Path: filepath.Join(t.TempDir(), "nope")}
	if got := a.LoginStatus(context.Background(), Profile{}); got != LoginUnknown {
		t.Errorf("got %v, want LoginUnknown", got)
	}
}

func TestLoginStatusTimeoutMakesNoClaim(t *testing.T) {
	a := fakeAdapter(t, "OGE_FAKE_CLAUDE_LOGINS=hang")
	old := loginTimeout
	loginTimeout = 200 * time.Millisecond
	defer func() { loginTimeout = old }()
	if got := a.LoginStatus(context.Background(), Profile{}); got != LoginUnknown {
		t.Errorf("got %v, want LoginUnknown", got)
	}
}
