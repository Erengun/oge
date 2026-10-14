package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/task"
)

func TestDryRunListsAgentLimitations(t *testing.T) {
	cfg, probs := pipeline.Load([]byte("schema = 1\n[project]\ntest_globs = [\"*_test.go\"]\n[[check.commands]]\nrun = \"go test -json ./...\"\nreport = \"go-test-json\"\n"))
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	f, probs := pipeline.Resolve(cfg, pipeline.Overrides{}, []string{"codex"})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	var b bytes.Buffer
	renderDryRun(&b, task.Task{}, f, true, "", false)
	if !strings.Contains(b.String(), "! Agent limitations\n!   implement (codex): shell commands can see") {
		t.Errorf("dry run:\n%s", b.String())
	}
}
