//go:build unix

package cli

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/erengun/oge/internal/agent"
	"github.com/erengun/oge/internal/agent/agy"
	"github.com/erengun/oge/internal/agent/fake/multiturn"
)

// shotReply is the markdown reply of the shots' third turn: a heading, a
// list, a fenced Go block and a table.
const shotReply = "## How Add is tested\n\n" +
	"`TestAdd` checks one case:\n\n" +
	"- it calls `Add(2, 3)`\n" +
	"- and fails unless the sum is **5**\n\n" +
	"```go\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n```\n\n" +
	"| file | role |\n|------|------|\n| add.go | the code |\n| add_test.go | the check |\n"

// TestTUIShots plays the bare home on fakeagy at two terminal sizes, with
// colour, and writes each scenario's final screen to OGE_TUI_SHOTS as
// <size>-<name>.ansi (the visible screen, one SGR run per style change)
// and .txt (the same, plain), and the session's whole output stream as
// <size>.raw. scripts/tui-shots.sh turns them into PNGs.
// It is not a check: it is how an agent working on the TUI sees it.
func TestTUIShots(t *testing.T) {
	dir := os.Getenv("OGE_TUI_SHOTS")
	if dir == "" {
		t.Skip("OGE_TUI_SHOTS is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := multiturn.BuildAgy(t)
	for _, size := range [][2]int{{160, 45}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			t.Parallel()
			shotSession(t, dir, bin, size[0], size[1])
		})
	}
}

func shotSession(t *testing.T, dir, bin string, cols, rows int) {
	f := newRunFixture(t)
	jsonStr := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	frames := []string{
		`{"dir": "out", "msg": {"event": "init", "conversation_id": "conv-1", "tools": ["run_command", "write_to_file", "view_file"], "model": "gemini-3.8-flash-high"}}`,
		// (b) yo: a one-line reply.
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "Hey! What are we working on?"}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "Hey! What are we working on?", "usage": {"total_tokens": 120}}}`,
		// (c) tool steps, then a markdown reply. agy's events carry no
		// tool output today; the frames do, for when the view shows it.
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_index": 1, "state": "ACTIVE", "step_type": "tool", "tool_info": {"name": "run_command", "parameters": {"CommandLine": "go test ./..."}}}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_index": 1, "state": "DONE", "step_type": "tool", "tool_info": {"name": "run_command", "parameters": {"CommandLine": "go test ./..."}, "output": "--- FAIL: TestAdd (0.00s)\n    add_test.go:7: Add(2, 3) != 5\nFAIL\nFAIL\tfx\t0.004s"}}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_index": 2, "state": "ACTIVE", "step_type": "tool", "tool_info": {"name": "view_file", "parameters": {"AbsolutePath": "add_test.go"}}}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_index": 2, "state": "DONE", "step_type": "tool", "tool_info": {"name": "view_file", "parameters": {"AbsolutePath": "add_test.go"}, "output": "package fx\n\nimport \"testing\"\n..."}}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": ` + jsonStr(shotReply) + `}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": ` + jsonStr(shotReply) + `, "usage": {"total_tokens": 900}}}`,
		// (d) a change: cut, verified, the Verdict line.
		`{"dir": "in"}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_index": 3, "state": "ACTIVE", "step_type": "tool", "tool_info": {"name": "write_to_file", "parameters": {"TargetFile": "add.go"}}}}`,
		`{"dir": "act", "write": {"path": "add.go", "content": "package fx\n\nfunc Add(a, b int) int { return a + b }\n"}}`,
		`{"dir": "out", "msg": {"event": "step_update", "step_type": "agent_response", "text_delta": "Fixed Add: it returned 0, now it returns a + b."}}`,
		`{"dir": "out", "msg": {"event": "result", "status": "SUCCESS", "response": "Fixed Add: it returned 0, now it returns a + b.", "usage": {"total_tokens": 1400}}}`,
		`{"dir": "in"}`,
	}
	fixture := filepath.Join(t.TempDir(), "session.ndjson")
	if err := os.WriteFile(fixture, []byte(strings.Join(frames, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ad := &agy.Adapter{Path: bin, CloseGrace: time.Second,
		Environ: func() []string { return append(os.Environ(), "OGE_FAKE_AGY_FIXTURE="+fixture) }}
	s := startHomeSessionWith(t, f, func(e *Env) {
		e.Agents = map[string]agent.Adapter{"agy": ad}
		// Colour on: a pipe reports neither a size nor a profile.
		getenv := e.Getenv
		e.Getenv = func(k string) string {
			switch k {
			case "NO_COLOR":
				return ""
			case "COLORTERM":
				return "truecolor"
			}
			return getenv(k)
		}
		e.programOptions = []tea.ProgramOption{tea.WithWindowSize(cols, rows),
			tea.WithColorProfile(colorprofile.TrueColor)}
	}, "--fast", "--agent", "agy")
	s.cols, s.rows = cols, rows
	shoot := func(name string) {
		t.Helper()
		v := s.term()
		v.write([]byte(s.out.String()))
		base := filepath.Join(dir, fmt.Sprintf("%dx%d-%s", cols, rows, name))
		if err := os.WriteFile(base+".ansi", []byte(v.screenANSI()), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(base+".txt", []byte(v.screenText()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() { s.settledScreen() }

	s.composer()
	settle()
	shoot("1-empty")

	s.type_("yo\r")
	s.expectScreen("What are we working on?")
	s.composer()
	settle()
	shoot("2-reply")

	s.type_("how is Add tested?\r")
	s.expectScreen("the check")
	s.composer()
	settle()
	shoot("3-markdown-tools")

	s.type_("fix Add\r")
	// verifying… lasts as long as the Check: shot if it is caught.
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		cur := s.screen(s.out.String())
		if strings.Contains(cur, "verifying") {
			shoot("4-change-verifying")
			break
		}
		if strings.Contains(cur, "Accepted") {
			t.Log("the Check ended before verifying… was caught")
			break
		}
	}
	s.expectScreen("Accepted")
	s.composer()
	settle()
	shoot("5-change-verdict")
	// The slash popup over the box, then cleared again.
	s.type_("/")
	s.expectScreen("Tab complete")
	settle()
	shoot("6-palette")
	s.type_("\x15") // ctrl+u: the box is empty again
	s.composer()
	s.quitChat(f)
	// The whole stream too, for a question the screens can't answer.
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%dx%d.raw", cols, rows)), []byte(s.out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Colours the shots' styles resolve against where a cell has none: the
// PNG renderer's background and text.
var (
	shotBG = color.RGBA{0x17, 0x17, 0x17, 0xff}
	shotFG = color.RGBA{0xc5, 0xc8, 0xc6, 0xff}
)

// visible is the screen's rows, the scrollback above it left out, padded
// to the screen's height.
func (t *vt) visible() (rows [][]rune, styles [][]uv.Style) {
	h := t.height
	if h == 0 {
		h = len(t.lines)
	}
	for i := t.top; i < t.top+h; i++ {
		var l []rune
		var st []uv.Style
		if i < len(t.lines) {
			l, st = t.lines[i], t.styles[i]
		}
		rows, styles = append(rows, l), append(styles, st)
	}
	return rows, styles
}

// screenText is the visible screen, plain.
func (t *vt) screenText() string {
	rows, _ := t.visible()
	var b strings.Builder
	for _, l := range rows {
		b.WriteString(strings.TrimRight(string(l), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// screenANSI is the visible screen with its colours, every row padded to
// the screen's width. Each style change resets and sets the whole style in
// truecolor, with faint and reverse resolved into colours: freeze, which
// draws the PNGs, reads little more than one colour per SGR sequence.
func (t *vt) screenANSI() string {
	rows, styles := t.visible()
	var b strings.Builder
	for i, l := range rows {
		last := ""
		for col := 0; col < t.cols(); col++ {
			r, st := ' ', uv.Style{}
			if col < len(l) {
				r = l[col]
				if col < len(styles[i]) {
					st = styles[i][col]
				}
			}
			if sgr := shotSGR(st); sgr != last {
				b.WriteString("\x1b[0m" + sgr)
				last = sgr
			}
			b.WriteRune(r)
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

// shotSGR is st as one truecolor SGR sequence, "" for no style.
func shotSGR(st uv.Style) string {
	fg, bg := st.Fg, st.Bg
	if st.Attrs&uv.AttrReverse != 0 {
		fg, bg = shotOr(bg, shotBG), shotOr(fg, shotFG)
	}
	if st.Attrs&uv.AttrFaint != 0 {
		fg = shotBlend(shotOr(fg, shotFG), shotOr(bg, shotBG))
	}
	var p []string
	if fg != nil {
		r, g, b, _ := fg.RGBA()
		p = append(p, fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8))
	}
	if bg != nil {
		r, g, b, _ := bg.RGBA()
		p = append(p, fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8))
	}
	for _, a := range []struct {
		on  bool
		sgr string
	}{{st.Attrs&uv.AttrBold != 0, "1"}, {st.Attrs&uv.AttrItalic != 0, "3"},
		{st.Underline != 0, "4"}, {st.Attrs&uv.AttrStrikethrough != 0, "9"}} {
		if a.on {
			p = append(p, a.sgr)
		}
	}
	if len(p) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(p, ";") + "m"
}

func shotOr(c, def color.Color) color.Color {
	if c == nil {
		return def
	}
	return c
}

// shotBlend is fg halfway to bg, as a terminal draws faint text.
func shotBlend(fg, bg color.Color) color.Color {
	fr, fgr, fb, _ := fg.RGBA()
	br, bgr, bb, _ := bg.RGBA()
	mid := func(a, b uint32) uint8 { return uint8((a + b) / 2 >> 8) }
	return color.RGBA{mid(fr, br), mid(fgr, bgr), mid(fb, bb), 0xff}
}
