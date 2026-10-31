package cli

import (
	"testing"
	"time"

	"github.com/erengun/oge/internal/herdr"
	"github.com/erengun/oge/internal/pipeline"
	"github.com/erengun/oge/internal/run"
	"github.com/erengun/oge/internal/task"
)

type fakeReporter struct{ got []string }

func (f *fakeReporter) Report(s herdr.State, msg string) { f.got = append(f.got, string(s)+"|"+msg) }
func (f *fakeReporter) Release()                          { f.got = append(f.got, "release") }

func TestHerdrRollup(t *testing.T) {
	cases := []struct {
		name string
		do   func(m *model)
		want string
	}{
		{"progress", func(m *model) { m.apply(progressMsg{kind: run.EvStarted}) }, "working|"},
		{"gate", func(m *model) { m.needsYou() }, "blocked|oge: waiting for you"},
		{"accepted", func(m *model) { m.runEnded(&run.Result{Outcome: run.Accepted}, false) }, "idle|"},
		{"refused", func(m *model) { m.runEnded(&run.Result{Outcome: run.Refused}, false) }, "blocked|Run ended: refused"},
		{"failed", func(m *model) { m.runEnded(nil, true) }, "blocked|Run ended: stopped"},
	}
	for _, c := range cases {
		m := newModel(task.Task{Title: "t"}, &pipeline.Frozen{}, styles{}, time.Now)
		f := &fakeReporter{}
		m.herdr = f
		c.do(m)
		if len(f.got) != 1 || f.got[0] != c.want {
			t.Errorf("%s: got %v, want %q", c.name, f.got, c.want)
		}
	}
}

func TestNotifyOffUnderHerdrUnlessSet(t *testing.T) {
	under := func(k string) string {
		return map[string]string{"HERDR_ENV": "1", "HERDR_BIN_PATH": "/h", "HERDR_PANE_ID": "p"}[k]
	}
	n := notifierFor(Env{Getenv: under}, runFlags{}, nil)
	if n == nil || n.attention || n.runEnd || n.bell {
		t.Errorf("under herdr: %+v, want all off", n)
	}
	on := true
	n = notifierFor(Env{Getenv: under}, runFlags{}, &pipeline.UserConfig{Notify: &pipeline.NotifyConfig{Attention: &on}})
	if !n.attention || n.runEnd || n.bell {
		t.Errorf("explicit attention: %+v", n)
	}
	n = notifierFor(Env{Getenv: func(string) string { return "" }}, runFlags{}, nil)
	if !n.attention || !n.runEnd || !n.bell {
		t.Errorf("outside herdr: %+v, want all on", n)
	}
}
