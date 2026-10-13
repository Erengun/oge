package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// /terminal (#171): what Öge can tell about the terminal without writing
// anything, each line a hint naming the exact setting to change. Öge never
// edits terminal or tmux config files (#150): it only says what to do.

func (h *home) terminalCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/terminal")
	}
	return tea.Println(terminalHints(h.getenv)), ""
}

// terminalHints is the /terminal checklist for the environment getenv reads
// (nil reads nothing).
func terminalHints(getenv func(string) string) string {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	term, prog := getenv("TERM"), getenv("TERM_PROGRAM")
	tmux := getenv("TMUX") != ""
	screen := !tmux && (getenv("STY") != "" || strings.HasPrefix(term, "screen"))
	show := func(s string) string {
		if s == "" {
			return "unset"
		}
		return s
	}
	mux := "none"
	switch {
	case tmux:
		mux = "tmux"
	case screen:
		mux = "GNU screen"
	}
	lines := []string{
		"Terminal",
		"  TERM=" + show(term) + "  TERM_PROGRAM=" + show(prog) + "  COLORTERM=" + show(getenv("COLORTERM")) + "  multiplexer: " + mux,
		"Hints (Öge changes none of these for you)",
		"  Shift+Enter  can't be detected: if it submits, use Ctrl+J or \\ then Enter for a new line",
	}
	switch prog {
	case "Apple_Terminal":
		lines = append(lines, "  Alt+B/F/D/M  Terminal.app: Settings > Profiles > Keyboard > \"Use Option as Meta key\"")
	case "iTerm.app":
		lines = append(lines, "  Alt+B/F/D/M  iTerm2: Settings > Profiles > Keys > Left Option key: Esc+")
	default:
		lines = append(lines, "  Alt+B/F/D/M  if Alt types a symbol (macOS Terminal.app, iTerm2), turn on Option as Meta in the terminal's keyboard settings")
	}
	switch {
	case tmux:
		lines = append(lines,
			"  tmux         desktop alerts: set -g allow-passthrough on",
			"  tmux         modified keys (Shift+Enter, Alt+arrows): set -g extended-keys on",
			"  /copy        OSC 52 needs: set -g set-clipboard on",
		)
	case screen:
		lines = append(lines,
			"  screen       takes the bell only: no desktop alerts or OSC 52 clipboard under GNU screen",
		)
	default:
		lines = append(lines,
			"  /copy        uses OSC 52: enable clipboard access in the terminal if it asks (iTerm2: Settings > General > Selection > Applications in terminal may access clipboard)",
		)
	}
	switch {
	case getenv("NO_COLOR") != "":
		lines = append(lines, "  colour       NO_COLOR is set: unset it to use a theme")
	case term == "dumb" || term == "":
		lines = append(lines, "  colour       TERM="+show(term)+": set TERM (for example xterm-256color) for colour")
	case getenv("COLORTERM") != "truecolor" && getenv("COLORTERM") != "24bit":
		lines = append(lines, "  colour       256 colours at most: export COLORTERM=truecolor if your terminal supports it")
	default:
		lines = append(lines, "  colour       truecolor: pick a theme in /setup")
	}
	return strings.Join(lines, "\n")
}
