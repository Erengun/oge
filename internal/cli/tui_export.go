package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/redact"
)

// /export and /copy (#168). Both pass the text through internal/redact
// before it leaves the process: nothing unredacted is written or sent.

// logScroll remembers what the home printed above the program, for /export.
func (h *home) logScroll(text string) {
	if text != "" {
		h.scroll = append(h.scroll, strings.TrimSuffix(text, "\n"))
	}
}

// redacted is text with the escape sequences stripped and the known secret
// patterns replaced.
func redacted(text string) string {
	return string(redact.Redact([]byte(ansi.Strip(text))))
}

func (h *home) exportCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/export")
	}
	if len(h.scroll) == 0 {
		return nil, "/export: nothing to export yet"
	}
	dir := h.exportDir
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dir = ""
		}
	}
	name := "oge-transcript-" + h.clock().Format("20060102-150405") + ".txt"
	path := filepath.Join(dir, name)
	body := redacted(strings.Join(h.scroll, "\n")) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return nil, "/export: " + clean(err.Error())
	}
	return tea.Println("transcript written to " + path + " (known secret patterns redacted)"), ""
}

func (h *home) copyCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/copy")
	}
	if h.last == nil {
		return tea.Println("nothing to copy yet"), ""
	}
	seq, n := h.copySeq()
	return tea.Sequence(tea.Raw(seq), tea.Println(fmt.Sprintf("copied %d lines (if your terminal supports OSC 52; run /terminal for help)", n))), ""
}

// copySeq is the OSC 52 for the last Receipt's redacted text and its line
// count.
func (h *home) copySeq() (string, int) {
	text := strings.TrimRight(redacted(h.last.receipt()), "\n")
	return osc52(text, h.tmux), strings.Count(text, "\n") + 1
}

// osc52 sets the clipboard to text; under tmux it goes through the
// passthrough like the notifications (every ESC doubled).
func osc52(text string, tmux bool) string {
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
	if tmux {
		return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

