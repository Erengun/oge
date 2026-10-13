package cli

import (
	tea "charm.land/bubbletea/v2"
	"image/color"
	"slices"

	"charm.land/lipgloss/v2"
	"github.com/erengun/oge/internal/pipeline"
)

// The palette presets (#174). This file is the one place that holds colours:
// the rest of the TUI asks for a role (ok, bad, accent) and never a shade.
// Status is never conveyed by colour alone: every coloured status keeps its
// glyph (✓ ✗ + -) and its word, so a preset only changes how it looks.

// palette is the colours of the three roles. Bold and faint are attributes,
// not colours, so every preset shares them.
type palette struct{ accent, ok, bad color.Color }

// themeState is the live view's current preset. Every copy of a styles
// shares one, so choosing a theme in the setup panel repaints the view
// that is already open.
type themeState struct {
	name  string // one of pipeline.KnownThemes
	dark  bool   // the terminal's background, once it has said (see known)
	known bool
}

func rgb(hex uint32) color.Color {
	return color.RGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

// paletteOf is the palette of a preset. auto follows the terminal's
// background, and is the 16 basic colours until the terminal has answered
// (they read on either background).
func paletteOf(name string, dark, known bool) palette {
	switch name {
	case "auto":
		if !known {
			return paletteOf("ansi", dark, known)
		}
		return paletteOf(map[bool]string{true: "dark", false: "light"}[dark], dark, known)
	case "dark":
		return palette{accent: rgb(0xc792ea), ok: rgb(0x7fd18b), bad: rgb(0xff7b72)}
	case "light":
		return palette{accent: rgb(0x8250df), ok: rgb(0x1a7f37), bad: rgb(0xcf222e)}
	case "daltonized":
		// Okabe-Ito: blue and orange, never red against green.
		if known && !dark {
			return palette{accent: rgb(0xcc79a7), ok: rgb(0x0072b2), bad: rgb(0xd55e00)}
		}
		return palette{accent: rgb(0xcc79a7), ok: rgb(0x56b4e9), bad: rgb(0xe69f00)}
	}
	// ansi: the 16 basic colours; the user's palette decides the shades.
	return palette{accent: lipgloss.Magenta, ok: lipgloss.Green, bad: lipgloss.Red}
}

// styles paint text; without colour each is the identity, so a NO_COLOR
// frame holds no escape sequences at all.
type styles struct {
	bold, dim, accent, ok, bad func(string) string
	th                         *themeState // nil without colour
}

// newStyles is the 16-colour set, or none: what every view starts with.
func newStyles(colour bool) styles {
	if !colour {
		id := func(s string) string { return s }
		return styles{id, id, id, id, id, nil}
	}
	th := &themeState{name: "ansi"}
	paint := func(pick func(palette) color.Color) func(string) string {
		return func(t string) string {
			return lipgloss.NewStyle().Foreground(pick(paletteOf(th.name, th.dark, th.known))).Render(t)
		}
	}
	return styles{
		bold:   func(t string) string { return lipgloss.NewStyle().Bold(true).Render(t) },
		dim:    func(t string) string { return lipgloss.NewStyle().Faint(true).Render(t) },
		accent: paint(func(p palette) color.Color { return p.accent }),
		ok:     paint(func(p palette) color.Color { return p.ok }),
		bad:    paint(func(p palette) color.Color { return p.bad }),
		th:     th,
	}
}

// setTheme switches the preset; an unknown name or a colourless set is left
// as it is. "" is auto.
func (s styles) setTheme(name string) {
	if name == "" {
		name = "auto"
	}
	if s.th != nil && slices.Contains(pipeline.KnownThemes, name) {
		s.th.name = name
	}
}

// setDark records the terminal's background, for auto and daltonized.
func (s styles) setDark(dark bool) {
	if s.th != nil {
		s.th.dark, s.th.known = dark, true
	}
}

// themeName is the preset in use, "" when there is no colour at all.
func (s styles) themeName() string {
	if s.th == nil {
		return ""
	}
	return s.th.name
}

// askBackground asks the terminal for its background colour, for auto and
// daltonized. It is asked whatever the theme is now, since the setup panel
// can switch to one that needs it. Nothing is asked without colour.
func askBackground(s styles) tea.Cmd {
	if s.th == nil {
		return nil
	}
	return tea.RequestBackgroundColor
}

// themeNotes are the setup panel's notes for each preset.
var themeNotes = map[string]string{
	"auto":       "follows the terminal's background",
	"dark":       "for a dark background",
	"light":      "for a light background",
	"daltonized": "blue and orange, no red against green",
	"ansi":       "the terminal's own 16 colours",
}
