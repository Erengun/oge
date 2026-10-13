package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
)

// A Chat implementer launches in claude's auto mode, under its own
// profile hash; the one-shot profile keeps "default" and its hash.
func TestAutoModeLaunch(t *testing.T) {
	spec := agent.LaunchSpec{Role: "implementer", Workspace: t.TempDir()}
	one, _ := profileFor("implementer")
	args, err := one.args(spec, "id", nil)
	if err != nil || !strings.Contains(strings.Join(args, " "), "--permission-mode default") {
		t.Fatalf("one-shot args %v, %v", args, err)
	}
	auto := one
	if auto.mode, err = permissionMode(agent.AutoPermissions); err != nil {
		t.Fatal(err)
	}
	args, _ = auto.args(spec, "id", nil)
	if !strings.Contains(strings.Join(args, " "), "--permission-mode auto") || !strings.Contains(strings.Join(args, " "), "--no-session-persistence") {
		t.Errorf("auto args %v", args)
	}
	if auto.hash() == one.hash() {
		t.Error("auto mode has the one-shot profile's hash")
	}
	if w, fatal := (envelope{mode: "auto"}).check(initFrame{PermissionMode: "default"}, false); !strings.Contains(fatal, "permission mode") {
		t.Errorf("an auto Session reporting default mode: %v %q", w, fatal)
	}
}

// Only the implementer may run in auto mode (D1).
func TestAutoModeImplementerOnly(t *testing.T) {
	_, err := (&Adapter{Path: "no-such-claude"}).Open(context.Background(),
		agent.LaunchSpec{Role: "verifier", Permissions: agent.AutoPermissions})
	if err == nil || !strings.Contains(err.Error(), "verifier can't run in auto mode") {
		t.Errorf("verifier in auto mode: %v", err)
	}
}
