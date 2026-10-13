package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
		if len(h.scroll) > maxScroll {
			h.scroll = slices.Delete(h.scroll, 0, len(h.scroll)-maxScroll)
		}
	}
}

// maxScroll caps what /export keeps: the last lines (entries) printed.
const maxScroll = 5000

// maxOSC52 is the largest base64 payload /copy sends; terminals and tmux
// drop longer ones.
const maxOSC52 = 74000

// println prints text above the program and keeps it for /export.
func (h *home) println(text string) tea.Cmd {
	h.logScroll(text)
	return h.fit(text)
}

// fit prints text above the program in pieces that fit above the frame.
func (h *home) fit(text string) tea.Cmd {
	return printFitting(text, h.width, h.height, h.frameRows())
}

// frameRows is how tall a frame the print may land above: the taller of
// the frame on screen and the one this update draws next. The renderer
// moves up past the frame it last drew, which a stop or a reopened
// composer has already replaced in the model (#188).
func (h *home) frameRows() int {
	return max(h.shown, rows(h.view().Content))
}

// rows is how many rows a frame's text takes.
func rows(frame string) int {
	return strings.Count(frame, "\n") + 1
}

// printFitting prints text above a frame of frame rows on a terminal of
// width by height cells, in pieces that each fit above the frame. Bubble
// Tea's insertAbove moves the cursor up by the printed rows plus the
// frame's, and the terminal stops the cursor at its top row: a longer print
// lands over the frame and leaves its rows in the transcript (#233, #247).
func printFitting(text string, width, height, frame int) tea.Cmd {
	pieces := fitPieces(text, width, height, frame)
	if len(pieces) == 1 {
		return tea.Println(pieces[0])
	}
	cmds := make([]tea.Cmd, len(pieces))
	for i, p := range pieces {
		cmds[i] = tea.Println(p)
	}
	return tea.Sequence(cmds...)
}

// fitPieces splits text by lines into pieces of at most height-frame-1
// terminal rows each, counting a line wider than width as the rows it
// wraps to. An unknown height keeps it whole.
func fitPieces(text string, width, height, frame int) []string {
	if height <= 0 {
		return []string{text}
	}
	room := max(height-frame-1, 1)
	var pieces, piece []string
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		n := 1
		if w := ansi.StringWidth(line); width > 0 && w > width {
			n += w / width
		}
		if rows+n > room && len(piece) > 0 {
			pieces = append(pieces, strings.Join(piece, "\n"))
			piece, rows = nil, 0
		}
		piece, rows = append(piece, line), rows+n
	}
	return append(pieces, strings.Join(piece, "\n"))
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
	if h.exportDir == "" {
		return nil, "export: no private state dir: nothing was written"
	}
	if err := os.MkdirAll(h.exportDir, 0o700); err != nil {
		return nil, "export: no private state dir: " + clean(err.Error())
	}
	base := "oge-transcript-" + h.clock().Format("20060102-150405")
	body := redacted(strings.Join(h.scroll, "\n")) + "\n"
	var path string
	for i := 1; ; i++ {
		name := base + ".txt"
		if i > 1 {
			name = fmt.Sprintf("%s-%d.txt", base, i)
		}
		path = filepath.Join(h.exportDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) && i < 1000 {
			continue
		}
		if err != nil {
			return nil, "/export: " + clean(err.Error())
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, "/export: " + clean(werr.Error())
		}
		break
	}
	return h.println("transcript written to " + path + " (known secret patterns redacted)"), ""
}

func (h *home) copyCmd(args []string) (tea.Cmd, string) {
	if len(args) > 0 {
		return nil, usageOf("/copy")
	}
	if h.last == nil {
		return h.println("nothing to copy yet"), ""
	}
	seq, n := h.copySeq()
	if seq == "" {
		text := strings.TrimRight(redacted(h.last.receipt()), "\n")
		return h.println(text + "\n(too long for the clipboard; printed it instead)"), ""
	}
	return tea.Sequence(tea.Raw(seq), h.println(fmt.Sprintf("copied %d lines (if your terminal supports OSC 52; run /terminal for help)", n))), ""
}

// copySeq is the OSC 52 for the last Receipt's redacted text and its line
// count.
func (h *home) copySeq() (string, int) {
	text := strings.TrimRight(redacted(h.last.receipt()), "\n")
	if base64.StdEncoding.EncodedLen(len(text)) > maxOSC52 {
		return "", strings.Count(text, "\n") + 1
	}
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
