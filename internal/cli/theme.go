package cli

import (
	tea "charm.land/bubbletea/v2"
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/erengun/oge/internal/pipeline"
)

// The palette presets (#174). This file is the one place that holds colours:
// the rest of the TUI asks for a role (ok, bad, accent) and never a shade.
// Status is never conveyed by colour alone: every coloured status keeps its
// glyph (✓ ✗ + -) and its word, so a preset only changes how it looks.

// palette is the colours of the roles. Bold and faint are attributes, not
// colours, so every preset shares them. A nil agent or user is allowed (the
// ansi preset has no neutral foreground or subtle background to offer): the
// role then falls back to plain text or bold.
//
//	accent  Öge's own marker and highlights
//	ok/bad  success and failure (always beside a glyph and words)
//	warn    caution: a stop, something too long to show
//	border  box and rule lines
//	user    background of the human's echoed message
//	agent   the bullet before the agent's tool lines
//	diffAdd/diffDel  inline diff lines
//	ask     the permission and question prompt
type palette struct {
	accent, ok, bad           color.Color
	warn, border, user, agent color.Color
	diffAdd, diffDel, ask     color.Color
}

// themeState is the live view's current preset. Every copy of a styles
// shares one, so choosing a theme in the setup panel repaints the view
// that is already open.
type themeState struct {
	name   string // one of pipeline.KnownThemes
	dark   bool   // the terminal's background, once it has said (see known)
	known  bool
	custom *customTheme // the theme file's overrides (#175), nil without one
}

// palette is the colours now: the preset (the file's base, if it names one)
// with the file's overrides on top.
func (t *themeState) palette() palette {
	name := t.name
	if t.custom != nil && t.custom.base != "" {
		name = t.custom.base
	}
	return t.custom.overlay(paletteOf(name, t.dark, t.known))
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
		return palette{accent: rgb(0xc792ea), ok: rgb(0x7fd18b), bad: rgb(0xff7b72),
			warn: rgb(0xe3b341), border: rgb(0x6e7681), user: rgb(0x2d333b), agent: rgb(0xc9d1d9),
			diffAdd: rgb(0x7fd18b), diffDel: rgb(0xff7b72), ask: rgb(0x79c0ff)}
	case "light":
		return palette{accent: rgb(0x8250df), ok: rgb(0x1a7f37), bad: rgb(0xcf222e),
			warn: rgb(0x9a6700), border: rgb(0x8c959f), user: rgb(0xeaeef2), agent: rgb(0x57606a),
			diffAdd: rgb(0x1a7f37), diffDel: rgb(0xcf222e), ask: rgb(0x0969da)}
	case "daltonized":
		// Okabe-Ito: blue and orange, never red against green.
		if known && !dark {
			return palette{accent: rgb(0xcc79a7), ok: rgb(0x0072b2), bad: rgb(0xd55e00),
				warn: rgb(0x8a6d00), border: rgb(0x888888), user: rgb(0xe6ebf0), agent: rgb(0x555555),
				diffAdd: rgb(0x0072b2), diffDel: rgb(0xd55e00), ask: rgb(0x0072b2)}
		}
		return palette{accent: rgb(0xcc79a7), ok: rgb(0x56b4e9), bad: rgb(0xe69f00),
			warn: rgb(0xf0e442), border: rgb(0x7a7a7a), user: rgb(0x2a2f36), agent: rgb(0xbbbbbb),
			diffAdd: rgb(0x56b4e9), diffDel: rgb(0xe69f00), ask: rgb(0x56b4e9)}
	}
	// ansi: the 16 basic colours; the user's palette decides the shades.
	return palette{accent: lipgloss.Magenta, ok: lipgloss.Green, bad: lipgloss.Red,
		warn: lipgloss.Yellow, border: lipgloss.BrightBlack,
		diffAdd: lipgloss.Green, diffDel: lipgloss.Red, ask: lipgloss.Cyan}
}

// styles paint text; without colour each is the identity, so a NO_COLOR
// frame holds no escape sequences at all.
type styles struct {
	bold, dim, accent, ok, bad     func(string) string
	warn, border, user, agent, ask func(string) string
	diffAdd, diffDel               func(string) string
	th                             *themeState // nil without colour
}

// newStyles is the 16-colour set, or none: what every view starts with.
func newStyles(colour bool) styles {
	if !colour {
		id := func(s string) string { return s }
		return styles{id, id, id, id, id, id, id, id, id, id, id, id, nil}
	}
	th := &themeState{name: "ansi"}
	paint := func(pick func(palette) color.Color) func(string) string {
		return func(t string) string {
			return lipgloss.NewStyle().Foreground(pick(th.palette())).Render(t)
		}
	}
	// paintOr paints in a role's colour, or with fallback when the preset has
	// none for it (nil: the ansi preset's user and agent).
	paintOr := func(pick func(palette) color.Color, bg bool, fallback func(string) string) func(string) string {
		return func(t string) string {
			c := pick(th.palette())
			if c == nil {
				return fallback(t)
			}
			if bg {
				return lipgloss.NewStyle().Background(c).Bold(true).Render(t)
			}
			return lipgloss.NewStyle().Foreground(c).Render(t)
		}
	}
	bold := func(t string) string { return lipgloss.NewStyle().Bold(true).Render(t) }
	plain := func(t string) string { return t }
	return styles{
		bold:    func(t string) string { return lipgloss.NewStyle().Bold(true).Render(t) },
		dim:     func(t string) string { return lipgloss.NewStyle().Faint(true).Render(t) },
		accent:  paint(func(p palette) color.Color { return p.accent }),
		ok:      paint(func(p palette) color.Color { return p.ok }),
		bad:     paint(func(p palette) color.Color { return p.bad }),
		warn:    paint(func(p palette) color.Color { return p.warn }),
		border:  paint(func(p palette) color.Color { return p.border }),
		user:    paintOr(func(p palette) color.Color { return p.user }, true, bold),
		agent:   paintOr(func(p palette) color.Color { return p.agent }, false, plain),
		ask:     paint(func(p palette) color.Color { return p.ask }),
		diffAdd: paint(func(p palette) color.Color { return p.diffAdd }),
		diffDel: paint(func(p palette) color.Color { return p.diffDel }),
		th:      th,
	}
}

// diffLine paints one inline-diff line (indented by the caller): additions
// and deletions in their colours, the rest faint. The +/- stays, so the
// change is never shown by colour alone.
func (s styles) diffLine(l string) string {
	switch t := strings.TrimLeft(l, " "); {
	case strings.HasPrefix(t, "+++"), strings.HasPrefix(t, "---"):
		return s.dim(l)
	case strings.HasPrefix(t, "+"):
		return s.diffAdd(l)
	case strings.HasPrefix(t, "-"):
		return s.diffDel(l)
	}
	return s.dim(l)
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

// selected paints the VISUAL selection in reverse video: an attribute, not a
// colour, so every theme and NO_COLOR show it.
func (s styles) selected(t string) string { return lipgloss.NewStyle().Reverse(true).Render(t) }
