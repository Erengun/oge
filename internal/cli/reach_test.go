package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/erengun/oge/internal/herdr"
)

var reachT0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func chatAt(num int, repo, title, mark string) reachChat {
	return reachChat{num: num, repo: repo, title: title, mark: mark}
}

func texts(out []reachText) []string {
	var s []string
	for _, o := range out {
		s = append(s, o.text)
	}
	return s
}

func TestReachNeedsYouSentAtOnceThenCoalesced(t *testing.T) {
	r := &reach{stale: time.Hour}
	chats := []reachChat{chatAt(1, "api", "parser", "▲"), chatAt(2, "api", "auth", "●"), chatAt(3, "api", "ci", "●")}
	out, _ := r.observe(reachT0, true, chats)
	if got := texts(out); len(got) != 1 || got[0] != "oge · api · Chat 1 needs you" {
		t.Fatalf("first = %q", got)
	}
	// A second Chat inside the window waits for it.
	chats[1].mark = "▲"
	out, wake := r.observe(reachT0.Add(2*time.Minute), true, chats)
	if len(out) != 0 || wake != 13*time.Minute {
		t.Fatalf("inside the window: %q, wake %v", texts(out), wake)
	}
	out, _ = r.observe(reachT0.Add(14*time.Minute), true, chats)
	if len(out) != 0 {
		t.Fatalf("still inside: %q", texts(out))
	}
	out, _ = r.observe(reachT0.Add(15*time.Minute), true, chats)
	if got := texts(out); len(got) != 1 || got[0] != "oge · api · 2 Chats need you" {
		t.Fatalf("coalesced = %q", got)
	}
	// Nothing new: nothing more.
	if out, _ = r.observe(reachT0.Add(40*time.Minute), true, chats); len(out) != 0 {
		t.Fatalf("repeat = %q", texts(out))
	}
}

func TestReachStaleGateResendsOnce(t *testing.T) {
	r := &reach{stale: 30 * time.Minute}
	c := chatAt(2, "api", "auth", "▲")
	c.askedAt = reachT0
	out, _ := r.observe(reachT0, true, []reachChat{c})
	if len(out) != 1 {
		t.Fatalf("first = %q", texts(out))
	}
	if out, _ = r.observe(reachT0.Add(29*time.Minute), true, []reachChat{c}); len(out) != 0 {
		t.Fatalf("early = %q", texts(out))
	}
	out, _ = r.observe(reachT0.Add(30*time.Minute), true, []reachChat{c})
	if got := texts(out); len(got) != 1 || got[0] != "oge · api · Chat 2 needs you" {
		t.Fatalf("stale = %q", got)
	}
	if out, _ = r.observe(reachT0.Add(3*time.Hour), true, []reachChat{c}); len(out) != 0 {
		t.Fatalf("second stale = %q", texts(out))
	}
}

func TestReachSettledOnceAfterWork(t *testing.T) {
	r := &reach{stale: time.Hour}
	// Nothing worked: nothing settles.
	idle := []reachChat{chatAt(1, "api", "a", "○")}
	if out, _ := r.observe(reachT0, true, idle); len(out) != 0 {
		t.Fatalf("idle = %q", texts(out))
	}
	work := []reachChat{chatAt(1, "api", "a", "●"), chatAt(2, "api", "b", "●"), chatAt(3, "api", "c", "●"), chatAt(4, "api", "d", "●")}
	if out, _ := r.observe(reachT0.Add(time.Minute), true, work); len(out) != 0 {
		t.Fatalf("working = %q", texts(out))
	}
	done := []reachChat{chatAt(1, "api", "a", "✓"), chatAt(2, "api", "b", "✓"), chatAt(3, "api", "c", "✗"), chatAt(4, "api", "d", "○")}
	done[3].mark = "▲"
	out, _ := r.observe(reachT0.Add(2*time.Minute), true, done)
	want := "oge · api · settled · 2 ready · Chat 4 needs you · 1 failed"
	// The settled text already counts the Chat that needs the human.
	if got := texts(out); len(got) != 1 || got[0] != want || !out[0].blocked {
		t.Fatalf("settled = %q", got)
	}
	if out, _ = r.observe(reachT0.Add(3*time.Minute), true, done); len(out) != 0 {
		t.Fatalf("settled twice: %q", texts(out))
	}
}

func TestReachNothingWhilePresent(t *testing.T) {
	r := &reach{stale: time.Hour}
	c := []reachChat{chatAt(1, "api", "a", "▲")}
	if out, _ := r.observe(reachT0, false, c); len(out) != 0 {
		t.Fatalf("present = %q", texts(out))
	}
	// Away begins: the waiting Gate is news then.
	if out, _ := r.observe(reachT0.Add(time.Minute), true, c); len(out) != 1 {
		t.Fatalf("away = %q", texts(out))
	}
	// Back, then away again: a new absence starts clean.
	r.observe(reachT0.Add(2*time.Minute), false, c)
	if out, _ := r.observe(reachT0.Add(3*time.Minute), true, c); len(out) != 1 {
		t.Fatalf("second absence = %q", texts(out))
	}
}

func TestReachTitlesOnlyOnOptIn(t *testing.T) {
	c := []reachChat{chatAt(2, "api", "parser \x1b[31mred", "▲")}
	off := &reach{}
	if out, _ := off.observe(reachT0, true, c); texts(out)[0] != "oge · api · Chat 2 needs you" {
		t.Fatalf("titles off: %q", texts(out))
	}
	on := &reach{titles: true}
	out, _ := on.observe(reachT0, true, c)
	if got := texts(out)[0]; got != "oge · api · Chat 2 parser red needs you" {
		t.Fatalf("titles on: %q", got)
	}
	// Counts across Chats carry no titles even then.
	two := &reach{titles: true}
	out, _ = two.observe(reachT0, true, []reachChat{chatAt(1, "api", "x", "▲"), chatAt(2, "api", "y", "▲")})
	if got := texts(out)[0]; strings.Contains(got, "x") || got != "oge · api · 2 Chats need you" {
		t.Fatalf("two = %q", got)
	}
}

func TestReachRepoNames(t *testing.T) {
	r := &reach{}
	out, _ := r.observe(reachT0, true, []reachChat{chatAt(1, "api", "", "○"), chatAt(2, "web", "", "▲")})
	if got := texts(out)[0]; got != "oge · web · Chat 2 needs you" && !strings.Contains(got, "2 repos") {
		t.Fatalf("got %q", got)
	}
	if got := r.stoppedText([]reachChat{chatAt(1, "api", "", "○"), chatAt(2, "web", "", "○")}); got != "oge · 2 repos · the Desk stopped · run oge to restore" {
		t.Fatalf("stopped = %q", got)
	}
	if got := r.stoppedText([]reachChat{chatAt(1, "api", "", "○")}); got != "oge · api · the Desk stopped · run oge to restore" {
		t.Fatalf("stopped = %q", got)
	}
}

// fakeArgv records the argv it is asked to run.
type fakeArgv struct {
	mu   sync.Mutex
	runs [][]string
	err  error
}

func (f *fakeArgv) run(_ context.Context, argv []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, argv)
	return f.err
}

func (f *fakeArgv) got() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

func TestReachOutRunsArgvWithTextLastAndNoShell(t *testing.T) {
	f := &fakeArgv{}
	cfg := []string{"ntfy", "publish", "my topic"}
	o := &reachOut{argv: cfg, run: f.run}
	o.send("oge · api · Chat 1 needs you; rm -rf /")
	o.wait(time.Second)
	runs := f.got()
	if len(runs) != 1 || len(runs[0]) != 4 || runs[0][0] != "ntfy" || runs[0][2] != "my topic" || runs[0][3] != "oge · api · Chat 1 needs you; rm -rf /" {
		t.Fatalf("runs = %q", runs)
	}
	if len(cfg) != 3 {
		t.Fatal("the configured argv was changed")
	}
}

func TestReachOutShowsAFailureOnce(t *testing.T) {
	f := &fakeArgv{err: errors.New("boom")}
	o := &reachOut{argv: []string{"/usr/bin/ntfy"}, run: f.run}
	o.send("a")
	o.send("b")
	o.wait(time.Second)
	if got := o.failure(); got != "[notify] command ntfy failed: boom" {
		t.Fatalf("failure = %q", got)
	}
	if got := o.failure(); got != "" {
		t.Fatalf("shown twice: %q", got)
	}
}

// reachHome is a home with two Chats in one repo on a fake clock.
func reachHome(f *fakeArgv, titles bool) (*home, *time.Time) {
	h := testHome(70)
	h.chatView.num, h.chatView.name, h.root = 1, "parser", "/src/api"
	h.views = []*chatView{h.chatView}
	now := reachT0
	h.now = func() time.Time { return now }
	h.absence.last = now
	h.reachDesk = &reachDesk{reach: reach{titles: titles}, out: &reachOut{argv: []string{"push"}, run: f.run}}
	return h, &now
}

func TestHomeReachFiresOnlyWhileAway(t *testing.T) {
	f := &fakeArgv{}
	h, now := reachHome(f, false)
	h.chatView.attnMark = "▲"
	h.reachCheck(*now)
	h.reachDesk.out.wait(time.Second)
	if len(f.got()) != 0 {
		t.Fatalf("pushed while present: %q", f.got())
	}
	// Focus lost: away at once.
	h.noteInput(tea.BlurMsg{})
	*now = now.Add(time.Second)
	h.reachCheck(*now)
	h.reachDesk.out.wait(time.Second)
	if runs := f.got(); len(runs) != 1 || runs[0][1] != "oge · api · Chat 1 needs you" {
		t.Fatalf("runs = %q", runs)
	}
}

func TestHomeReachAfterTenQuietMinutes(t *testing.T) {
	f := &fakeArgv{}
	h, now := reachHome(f, true)
	h.chatView.attnMark = "▲"
	*now = now.Add(9 * time.Minute)
	h.reachCheck(*now)
	*now = now.Add(time.Minute)
	h.reachCheck(*now)
	h.reachDesk.out.wait(time.Second)
	if runs := f.got(); len(runs) != 1 || runs[0][1] != "oge · api · Chat 1 parser needs you" {
		t.Fatalf("runs = %q", runs)
	}
}

func TestHomeReachStoppedOnTerminate(t *testing.T) {
	f := &fakeArgv{}
	h, now := reachHome(f, false)
	h.reachStopped() // present: nothing
	if len(f.got()) != 0 {
		t.Fatalf("stopped while present: %q", f.got())
	}
	*now = now.Add(11 * time.Minute)
	h.reachStopped()
	if runs := f.got(); len(runs) != 1 || runs[0][1] != "oge · api · the Desk stopped · run oge to restore" {
		t.Fatalf("runs = %q", runs)
	}
}

func TestInterruptsTerminated(t *testing.T) {
	in := newInterrupts(func() {})
	in.signalled(syscall.SIGINT)
	if in.terminated() {
		t.Fatal("SIGINT is not a termination")
	}
	in.signalled(syscall.SIGTERM)
	if !in.terminated() {
		t.Fatal("SIGTERM not seen")
	}
}

type recordingHerdr struct {
	mu   sync.Mutex
	logs []string
}

func (r *recordingHerdr) Report(s herdr.State, m string) {
	r.mu.Lock()
	r.logs = append(r.logs, string(s)+"|"+m)
	r.mu.Unlock()
}
func (r *recordingHerdr) Release() {}

func TestHomeReachHerdrCarriesTheAwayText(t *testing.T) {
	f := &fakeArgv{}
	h, now := reachHome(f, false)
	rec := &recordingHerdr{}
	h.herdr = rec
	h.chatView.attnMark = "▲"
	// Present: the usual fixed word.
	h.reachCheck(*now)
	h.reportDesk()
	h.noteInput(tea.BlurMsg{})
	*now = now.Add(time.Second)
	h.reachCheck(*now)
	h.reportDesk()
	want := []string{"blocked|" + notifyWaiting, "blocked|oge · api · Chat 1 needs you"}
	if len(rec.logs) != 2 || rec.logs[0] != want[0] || rec.logs[1] != want[1] {
		t.Fatalf("herdr = %q", rec.logs)
	}
}

func TestReachStaleResendsCoalesceAtShippedDefault(t *testing.T) {
	r := &reach{stale: 5 * time.Minute}
	c1 := chatAt(1, "api", "a", "▲")
	c1.askedAt = reachT0
	c2 := chatAt(2, "api", "b", "●")
	c3 := chatAt(3, "api", "c", "●") // keeps the Desk from settling
	if out, _ := r.observe(reachT0, true, []reachChat{c1, c2, c3}); len(out) != 1 {
		t.Fatalf("first = %q", texts(out))
	}
	c2.mark, c2.askedAt = "▲", reachT0.Add(time.Minute)
	both := []reachChat{c1, c2, c3}
	for _, m := range []int{1, 5, 6, 14} {
		if out, _ := r.observe(reachT0.Add(time.Duration(m)*time.Minute), true, both); len(out) != 0 {
			t.Fatalf("minute %d sent %q", m, texts(out))
		}
	}
	out, _ := r.observe(reachT0.Add(15*time.Minute), true, both)
	if got := texts(out); len(got) != 1 || got[0] != "oge · api · 2 Chats need you" {
		t.Fatalf("coalesced = %q", got)
	}
	if out, _ = r.observe(reachT0.Add(45*time.Minute), true, both); len(out) != 0 {
		t.Fatalf("extra = %q", texts(out))
	}
}

// settled carries the opt-in title like needs-you does, and counts more
// than one waiting Chat in the plural.
func TestReachSettledTitleAndPlural(t *testing.T) {
	work := []reachChat{chatAt(1, "api", "a", "●"), chatAt(2, "api", "b", "●")}
	for _, c := range []struct {
		titles bool
		marks  [2]string
		want   string
	}{
		{true, [2]string{"✓", "▲"}, "oge · api · settled · 1 ready · Chat 2 b needs you"},
		{false, [2]string{"▲", "▲"}, "oge · api · settled · 2 need you"},
	} {
		r := &reach{stale: time.Hour, titles: c.titles}
		if out, _ := r.observe(reachT0, true, work); len(out) != 0 {
			t.Fatalf("working = %q", texts(out))
		}
		done := []reachChat{chatAt(1, "api", "a", c.marks[0]), chatAt(2, "api", "b", c.marks[1])}
		out, _ := r.observe(reachT0.Add(time.Minute), true, done)
		if got := texts(out); len(got) == 0 || got[0] != c.want {
			t.Errorf("titles=%v: %q, want %q", c.titles, got, c.want)
		}
	}
}
