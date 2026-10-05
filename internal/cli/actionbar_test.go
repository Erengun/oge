package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/erengun/oge/internal/run"
)

// barHarness is an action bar whose actions only say what they did.
// Each chunk of keys arrives as one read, as a terminal delivers one
// keypress (or one paste).
func barHarness(keys string, color bool) (*actionBar, *bytes.Buffer, *[]string) {
	var chunks []string
	for _, k := range keys {
		chunks = append(chunks, string(k))
	}
	return barChunks(chunks, color)
}

type chunkReader struct{ chunks []string }

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if c.chunks[0] == "" {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

func barChunks(chunks []string, color bool) (*actionBar, *bytes.Buffer, *[]string) {
	var out bytes.Buffer
	var did []string
	b := &actionBar{in: &chunkReader{chunks: chunks}, out: &out, st: newStyles(color)}
	b.apply = func() string {
		did = append(did, "apply")
		b.applied = true
		return "Applied Candidate 6d1231d to your working tree (1 file changed). Nothing was committed or staged."
	}
	b.diff = func() { did = append(did, "diff"); out.WriteString("(the paged diff)\n") }
	b.receipt = func() { did = append(did, "receipt"); out.WriteString("ACCEPTED   Candidate 6d1231d · Oracle v0\n") }
	return b, &out, &did
}

func TestActionBarFrames(t *testing.T) {
	b, _, _ := barHarness("", false)
	golden(t, "action-bar", b.render()+"\n")
	b.applied = true
	golden(t, "action-bar-applied", b.render()+"\n")
	c, _, _ := barHarness("", true)
	golden(t, "action-bar-color", c.render()+"\n")
}

// d opens the diff and comes back to the bar; r shows the summary; a
// applies once, after which the bar no longer offers it; q leaves.
func TestActionBarKeys(t *testing.T) {
	b, out, did := barHarness("xdraaq", false)
	b.run()
	if got := strings.Join(*did, ","); got != "diff,receipt,apply" {
		t.Errorf("actions: %s", got)
	}
	var screen vt
	screen.write(out.Bytes())
	golden(t, "action-bar-session", screen.String())
}

func TestActionBarLeavesOnEnterAndAtTheEndOfInput(t *testing.T) {
	for _, keys := range []string{"\na", "\ra", "qa", "\x03a", ""} {
		b, out, did := barHarness(keys, false)
		b.run()
		if len(*did) != 0 {
			t.Errorf("%q: %v after leaving", keys, *did)
		}
		var screen vt
		screen.write(out.Bytes())
		if s := strings.TrimSpace(screen.String()); s != "" {
			t.Errorf("%q: the bar stayed on the screen: %q", keys, s)
		}
	}
}

// The live view offers the bar only after an Accepted Run: there is no
// one-key apply for any other outcome (ADR-0015).
func TestNoActionBarUnlessAccepted(t *testing.T) {
	for _, o := range []run.Outcome{run.Rejected, run.Overridden, run.Cancelled, run.Parked, run.InfrastructureStop} {
		var out bytes.Buffer
		env := Env{Stdin: strings.NewReader("a"), Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
		h := newTUIHarness(t, false, 80)
		u := &tui{in: env.Stdin, out: &out, plain: &renderer{w: &out, frozen: h.f}, m: h.m}
		if code := afterRun(env, runFlags{}, u, t.TempDir(), &run.Result{Outcome: o, Candidate: "6d1231d9f00d"}); code != ExitOK || out.Len() != 0 {
			t.Errorf("%s: exit %d, %q", o, code, out.String())
		}
	}
}

// Only a single real keypress acts. Arrow keys (CSI and SS3, whole or
// split across reads), bracketed and unbracketed pastes, and uppercase
// letters never apply or open the diff.
func TestActionBarIgnoresEscapeSequencesAndPastes(t *testing.T) {
	for _, chunks := range [][]string{
		{"\x1b[A"}, {"\x1bOA"}, {"\x1b[D"}, {"\x1b[1;5D"}, {"\x1b", "[", "A"}, {"\x1b", "O", "a"},
		{"\x1b[200~a banana\x1b[201~"}, {"\x1b[200~", "a", "\x1b[201~"}, {"banana"}, {"aa"},
		{"A"}, {"D"}, {"R"}, {"\x1ba"},
	} {
		b, _, did := barChunks(append(chunks, "q", "a"), false)
		b.run()
		if len(*did) != 0 {
			t.Errorf("%q: %v", chunks, *did)
		}
	}
	// After a sequence, a real a still applies.
	b, _, did := barChunks([]string{"\x1b[A", "a", "q"}, false)
	b.run()
	if strings.Join(*did, ",") != "apply" {
		t.Errorf("a after an arrow key: %v", *did)
	}
}
