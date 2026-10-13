package cli

import (
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func tkey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

const samplePatch = `diff --git a/add.go b/add.go
index 1111111..2222222 100644
--- a/add.go
+++ b/add.go
@@ -1,3 +1,3 @@
 package add
-func Add(a, b int) int { return 0 }
+func Add(a, b int) int { return a + b }
 // end
# Öge: CLAUDE.md is an agent-config change, held back: oge apply and oge branch leave it out unless --with-agent-config
diff --git a/CLAUDE.md b/CLAUDE.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/CLAUDE.md
@@ -0,0 +1,2 @@
+# notes
+more
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 4444444..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
`

func diffHome(t *testing.T) *home {
	t.Helper()
	h := testHome(80)
	h.diffOf = func(id string) (string, []diffFile, bool) {
		return "Run 20261013T101010-abcdef · Accepted · Candidate abc against the Snapshot def", splitPatch(samplePatch), true
	}
	submit(h, "/diff")
	if h.diff == nil {
		t.Fatal("/diff didn't open the viewer")
	}
	return h
}

func TestSplitPatch(t *testing.T) {
	fs := splitPatch(samplePatch)
	if len(fs) != 3 {
		t.Fatalf("%d files: %+v", len(fs), fs)
	}
	want := []struct {
		path, status string
		add, del     int
	}{{"add.go", "modified", 1, 1}, {"CLAUDE.md", "held back", 2, 0}, {"old.txt", "deleted", 0, 1}}
	for i, w := range want {
		g := fs[i]
		if g.path != w.path || g.status != w.status || g.add != w.add || g.del != w.del {
			t.Errorf("file %d = %s %s +%d -%d, want %+v", i, g.path, g.status, g.add, g.del, w)
		}
	}
	if !strings.HasPrefix(fs[1].text, "# Öge:") || strings.Contains(fs[0].text, "# Öge:") {
		t.Errorf("the mark didn't stay with its file:\n%s\n---\n%s", fs[0].text, fs[1].text)
	}
}

func TestDiffViewerGolden(t *testing.T) {
	h := diffHome(t)
	golden(t, "diff-list", frame(h))
	key(h, "j")
	h.Update(enter())
	golden(t, "diff-file", frame(h))
}

func TestDiffViewerKeys(t *testing.T) {
	h := diffHome(t)
	h.Update(tkey(tea.KeyDown))
	h.Update(tkey(tea.KeyDown))
	h.Update(tkey(tea.KeyDown)) // stops at the last file
	if h.diff.sel != 2 {
		t.Fatalf("sel = %d", h.diff.sel)
	}
	h.Update(tkey(tea.KeyUp))
	h.Update(enter())
	if !h.diff.viewing || !strings.Contains(frame(h), "+more") {
		t.Fatalf("enter didn't open CLAUDE.md:\n%s", frame(h))
	}
	h.Update(tkey(tea.KeyEscape))
	if h.diff == nil || h.diff.viewing {
		t.Fatal("esc didn't go back to the list")
	}
	h.Update(tkey(tea.KeyEscape))
	if h.diff != nil {
		t.Fatal("second esc didn't close")
	}
}

func TestDiffViewerScrollsAndWheel(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,60 @@\n")
	for i := 0; i < 60; i++ {
		b.WriteString("+line " + strings.Repeat("x", i%3) + "\n")
	}
	h := testHome(80)
	h.diffOf = func(string) (string, []diffFile, bool) { return "head", splitPatch(b.String()), true }
	submit(h, "/diff")
	h.Update(enter())
	if !h.wantsMouse() {
		t.Error("the viewer doesn't ask for the mouse")
	}
	before := h.diff.vp.YOffset()
	wheel(h, tea.MouseWheelDown)
	wheel(h, tea.MouseWheelDown)
	if h.diff.vp.YOffset() <= before {
		t.Error("the wheel didn't scroll the diff")
	}
	h.Update(tkey(tea.KeyPgDown))
	if h.diff.vp.AtTop() {
		t.Error("pgdown stayed at the top")
	}
}

// A Candidate's held-out file is never in the viewer: it shows what
// delivery.Diff returns, which leaves held-out tests out.
func TestDiffViewerNeverShowsHeldOut(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t)
	code, out, errOut := f.run(t, verifierThen(negTest+`echo "exit: extended"`, fixScript), standardTask, "--agent", "fake", "--unattended")
	if code != ExitOK {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	env := f.env(io.Discard, io.Discard)
	_, files, ok := diffLoader(env)("")
	if !ok || len(files) == 0 {
		t.Fatal("the viewer found no files")
	}
	for _, fl := range files {
		if strings.Contains(fl.text, heldOutMarker) || strings.Contains(fl.path, "neg_test") {
			t.Errorf("the viewer shows the held-out test: %s", fl.path)
		}
	}
	// And it is the CLI's patch, file for file.
	_, cli, _ := f.deliver(t, "diff", "--plain")
	for _, fl := range files {
		if !strings.Contains(cli, strings.TrimSuffix(fl.text, "\n")) {
			t.Errorf("%s differs from oge diff's", fl.path)
		}
	}
}
