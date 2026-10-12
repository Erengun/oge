package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// printer prints two lines above its live line and then idles.
type printer struct{}

func (printer) Init() tea.Cmd                             { return tea.Sequence(tea.Println("a"), tea.Println("a")) }
func (m printer) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (printer) View() tea.View                            { return tea.NewView("live") }

// A real inline Bubble Tea program prints above its live line, and the
// screen vt rebuilds from the bytes has the lines in order with the live
// line at the bottom, whether or not the terminal is tall enough to scroll.
func TestVTShowsBubbleTeaInlinePrintingAboveTheLiveLine(t *testing.T) {
	for _, height := range []int{2, 3, 10} {
		out := &syncBuf{}
		p := tea.NewProgram(printer{}, tea.WithInput(nil), tea.WithOutput(out), tea.WithWindowSize(20, height), tea.WithoutSignals(), tea.WithEnvironment([]string{"TERM=xterm-256color"}))
		done := make(chan error, 1)
		go func() { _, err := p.Run(); done <- err }()
		// The program is idle once "live" follows both lines. Quitting
		// erases the live line, so read the screen before it.
		var got string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			screen := vt{height: height}
			screen.write(out.snapshot())
			got = screen.String()
			if strings.HasSuffix(got, "a\na\nlive\n") {
				break
			}
		}
		p.Quit()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// The renderer's first print scrolls one blank row into the
		// scrollback, as it does on a real terminal.
		if want := "\na\na\nlive\n"; got != want {
			t.Errorf("height %d: %q, want %q", height, got, want)
		}
	}
}

func screenAfter(height int, s string) *vt {
	v := &vt{height: height}
	v.write([]byte(s))
	return v
}

func TestVTInsertLinePushesRowsDownAndDropsThoseOffTheScreen(t *testing.T) {
	v := screenAfter(3, "1\r\n2\r\n3\x1b[A\x1b[L")
	if got, want := v.String(), "1\n\n2\n"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if v.row != 1 || v.col != 0 {
		t.Errorf("cursor at %d,%d", v.row, v.col)
	}
	// Without a height nothing is dropped.
	if got, want := screenAfter(0, "1\r\n2\x1b[A\x1b[2L").String(), "\n\n1\n2\n"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestVTLineFeedAtTheBottomScrollsIntoTheScrollback(t *testing.T) {
	v := screenAfter(2, "1\n2\n3")
	if got, want := v.String(), "1\n2\n3\n"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if v.top != 1 || v.row != 2 {
		t.Errorf("top %d row %d", v.top, v.row)
	}
}

func TestVTCursorMovesStayOnTheScreen(t *testing.T) {
	v := screenAfter(2, "1\n2\n3\x1b[9A")
	if v.row != v.top {
		t.Errorf("up left the screen: row %d top %d", v.row, v.top)
	}
	v.write([]byte("\x1b[9B"))
	if v.row != v.top+1 {
		t.Errorf("down left the screen: row %d top %d", v.row, v.top)
	}
}
