package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/fake"
	"github.com/erengun/oge/internal/ledger"
	"github.com/erengun/oge/internal/run"
)

// specRecorder notes the LaunchSpec each Session is opened with.
type specRecorder struct {
	agent.Adapter
	specs *[]agent.LaunchSpec
}

func (r specRecorder) Open(ctx context.Context, s agent.LaunchSpec) (agent.Session, error) {
	*r.specs = append(*r.specs, s)
	return r.Adapter.Open(ctx, s)
}

// A Run bound to claude@work launches with the profile's name, the output
// and the Ledger name it, and no profile path reaches the Ledger or the
// output (ADR-0006).
func TestRunNamesTheProfileAndNeverItsPath(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	home := f.vars["HOME"]
	writeFile(t, filepath.Join(home, ".claude-work", "settings.json"), []byte("{}"))

	script := filepath.Join(filepath.Dir(f.repo), "agent.sh")
	writeFile(t, script, []byte(fixScript))
	var stdout, stderr bytes.Buffer
	env := f.env(&stdout, &stderr)
	var specs []agent.LaunchSpec
	env.Agents = map[string]agent.Adapter{"claude": specRecorder{&fake.Adapter{Script: script, Env: []string{
		"HOME=" + home, "TMPDIR=" + f.vars["TMPDIR"], "OGE_TEST_STATE=" + f.state,
		"OGE_TEST_OUT=" + filepath.Join(filepath.Dir(f.repo), "out"),
	}}, &specs}}
	code := Main(env, []string{"fix Add", "--fast", "--agent", "claude@work", "--unattended"})
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if len(specs) != 1 || specs[0].Profile != "work" {
		t.Errorf("launched with %+v, want one Session on profile work", specs)
	}
	if !strings.Contains(stdout.String(), "claude@work") {
		t.Errorf("the output doesn't name the profile:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), ".claude-work") {
		t.Error("a profile path reached the output")
	}

	recs, err := ledger.Replay(f.onlyRun(t))
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, r := range recs {
		if bytes.Contains(r.Data, []byte(".claude-work")) {
			t.Errorf("%s record holds a profile path: %s", r.Type, r.Data)
		}
		if r.Type == run.RecAttemptStarting && bytes.Contains(r.Data, []byte(`"profile":"work"`)) {
			named = true
		}
	}
	if !named {
		t.Error("AttemptStarting doesn't name the profile")
	}
}
