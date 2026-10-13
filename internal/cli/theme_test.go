package cli

import (
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/erengun/oge/internal/pipeline"
)

func themed(name string, dark, known bool) styles {
	st := newStyles(true)
	st.setTheme(name)
	if known {
		st.setDark(dark)
	}
	return st
}

func isZero(c color.Color) bool { return c == nil }

func TestEveryPresetHasEveryRole(t *testing.T) {
	def := paletteOf("ansi", false, false)
	for _, name := range pipeline.KnownThemes {
		for _, bg := range []struct{ dark, known bool }{{false, false}, {true, true}, {false, true}} {
			p := paletteOf(name, bg.dark, bg.known)
			if (isZero(p.accent) && !isZero(def.accent)) || (isZero(p.ok) && !isZero(def.ok)) || (isZero(p.bad) && !isZero(def.bad)) {
				t.Errorf("%s %+v: palette has a missing colour: %+v", name, bg, p)
			}
			st := themed(name, bg.dark, bg.known)
			for role, paint := range map[string]func(string) string{"accent": st.accent, "ok": st.ok, "bad": st.bad} {
				if out := paint("x"); !strings.Contains(out, "\x1b[") || ansi.Strip(out) != "x" {
					t.Errorf("%s %+v %s paints %q", name, bg, role, out)
				}
			}
			if out := st.bold("x") + st.dim("x"); !strings.Contains(out, "\x1b[") {
				t.Errorf("%s: bold/dim paint nothing", name)
			}
		}
	}
}

func TestPresetsDiffer(t *testing.T) {
	seen := map[string]string{}
	for _, name := range []string{"ansi", "dark", "light", "daltonized"} {
		st := themed(name, true, true)
		got := st.ok("x") + st.bad("x") + st.accent("x")
		if prev, dup := seen[got]; dup {
			t.Errorf("%s paints like %s", name, prev)
		}
		seen[got] = name
	}
}

func TestAutoFollowsTheBackground(t *testing.T) {
	st := themed("auto", false, false)
	unknown := st.ok("x")
	if unknown != themed("ansi", false, false).ok("x") {
		t.Error("auto before the terminal answers is not the 16 colours")
	}
	st.setDark(true)
	if st.ok("x") != themed("dark", true, true).ok("x") {
		t.Error("auto on a dark background is not dark")
	}
	st.setDark(false)
	if st.ok("x") != themed("light", false, true).ok("x") {
		t.Error("auto on a light background is not light")
	}
}

func TestDaltonizedAvoidsRedAndGreen(t *testing.T) {
	for _, dark := range []bool{true, false} {
		p := paletteOf("daltonized", dark, true)
		okR, okG, okB, _ := p.ok.RGBA()
		badR, badG, badB, _ := p.bad.RGBA()
		if okB <= okR || okB <= okG {
			t.Errorf("dark=%v: ok is not blue", dark)
		}
		if badR <= badB || badR <= badG {
			t.Errorf("dark=%v: bad is not orange", dark)
		}
		if p.ok == p.bad {
			t.Errorf("dark=%v: ok and bad are one colour", dark)
		}
	}
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

func TestAnsiUsesOnlySixteenColours(t *testing.T) {
	st := themed("ansi", true, true)
	out := st.accent("a") + st.ok("b") + st.bad("c") + st.bold("d") + st.dim("e")
	ms := sgr.FindAllStringSubmatch(out, -1)
	if len(ms) == 0 {
		t.Fatal("nothing painted")
	}
	for _, m := range ms {
		for _, part := range strings.Split(m[1], ";") {
			n, err := strconv.Atoi(part)
			if err != nil && part != "" {
				t.Fatalf("odd escape %q", m[0])
			}
			switch {
			case n == 0 || n == 1 || n == 2 || n == 22 || n == 39 || (n >= 30 && n <= 37) || (n >= 90 && n <= 97):
			default:
				t.Errorf("escape %q is not a 16-colour code", m[0])
			}
		}
	}
	// And a truecolor preset does use 38;2, so the check above can fail.
	if !strings.Contains(themed("dark", true, true).ok("x"), "38;2;") {
		t.Error("dark is not truecolor, so the ansi check proves nothing")
	}
}

func TestNoColorHasNoEscapes(t *testing.T) {
	st := newStyles(colorAllowed(func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}))
	st.setTheme("dark") // NO_COLOR wins over a configured theme
	if st.themeName() != "" || askBackground(st) != nil {
		t.Errorf("a colourless set has theme %q", st.themeName())
	}
	for _, paint := range []func(string) string{st.bold, st.dim, st.accent, st.ok, st.bad} {
		if got := paint("x"); got != "x" {
			t.Errorf("NO_COLOR painted %q", got)
		}
	}
}

func TestUnknownThemeIsIgnored(t *testing.T) {
	st := themed("dark", true, true)
	st.setTheme("neon")
	if st.themeName() != "dark" {
		t.Errorf("theme = %q", st.themeName())
	}
	st.setTheme("")
	if st.themeName() != "auto" {
		t.Errorf("empty theme = %q", st.themeName())
	}
}

// Status is never colour alone: with every colour stripped, an ended stage
// still says ✓ or ✗ and its words.
func TestStatusKeepsGlyphAndWordWithoutColour(t *testing.T) {
	for _, name := range pipeline.KnownThemes {
		st := themed(name, true, true)
		for glyph, paint := range map[string]func(string) string{"✓": st.ok, "✗": st.bad} {
			if got := ansi.Strip(paint(glyph + " implement done")); !strings.HasPrefix(got, glyph) {
				t.Errorf("%s: %q lost its glyph", name, got)
			}
		}
	}
}
