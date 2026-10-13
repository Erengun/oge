package cli

import (
	"strings"
	"testing"
)

func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestTerminalHints(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		has  []string
		not  []string
	}{
		{"tmux", map[string]string{"TERM": "tmux-256color", "TMUX": "/s,1,0", "COLORTERM": "truecolor"},
			[]string{"multiplexer: tmux", "set -g allow-passthrough on", "set -g extended-keys on", "set -g set-clipboard on", "Ctrl+J"}, []string{"Use Option as Meta key"}},
		{"terminal.app", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"},
			[]string{"Use Option as Meta key", "256 colours"}, []string{"allow-passthrough"}},
		{"iterm2", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app", "COLORTERM": "truecolor"},
			[]string{"Left Option key: Esc+", "truecolor"}, []string{"allow-passthrough"}},
		{"xterm", map[string]string{"TERM": "xterm"},
			[]string{"multiplexer: none", "OSC 52", "if Alt types a symbol"}, []string{"allow-passthrough"}},
		{"screen", map[string]string{"TERM": "screen", "STY": "1.pts"},
			[]string{"multiplexer: GNU screen", "bell only"}, []string{"allow-passthrough"}},
		{"no-color", map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"},
			[]string{"NO_COLOR is set"}, nil},
	} {
		got := terminalHints(fakeEnv(c.env))
		for _, w := range c.has {
			if !strings.Contains(got, w) {
				t.Errorf("%s: lacks %q in\n%s", c.name, w, got)
			}
		}
		for _, w := range c.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: has %q", c.name, w)
			}
		}
	}
}

func TestTerminalGolden(t *testing.T) {
	golden(t, "terminal-tmux-iterm2", terminalHints(fakeEnv(map[string]string{
		"TERM": "tmux-256color", "TERM_PROGRAM": "iTerm.app", "TMUX": "/s,1,0", "COLORTERM": "truecolor",
	}))+"\n")
}

func TestTerminalCommand(t *testing.T) {
	h := testHome(80)
	h.getenv = fakeEnv(map[string]string{"TERM": "xterm"})
	typeText(h, "/terminal")
	if cmd, ok := h.slash("/terminal"); !ok || cmd == nil {
		t.Fatalf("/terminal printed nothing (ok %v)", ok)
	}
	if _, p := h.terminalCmd([]string{"x"}); !strings.HasPrefix(p, "usage:") {
		t.Errorf("args: %q", p)
	}
}
