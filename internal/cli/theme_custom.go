package cli

import (
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/BurntSushi/toml"
	"github.com/erengun/oge/internal/pipeline"
)

// The custom theme file (#175): token overrides on top of a base preset, in
// theme.toml next to the user config. It only changes colours; glyphs and
// words are not in it, so status is never shown by colour alone.

// customThemeFile is the file's name in the config directory.
const customThemeFile = "theme.toml"

// customTheme is a parsed theme file. A nil colour keeps the base's.
type customTheme struct {
	base string // "" keeps the preset the view is on
	// colours holds the file's role colours by key; a missing key keeps
	// the base's.
	colours map[string]color.Color
}

// themeKeys are the roles a theme file can override, with the palette
// field each one sets.
var themeKeys = map[string]func(*palette) *color.Color{
	"accent":   func(p *palette) *color.Color { return &p.accent },
	"ok":       func(p *palette) *color.Color { return &p.ok },
	"bad":      func(p *palette) *color.Color { return &p.bad },
	"warn":     func(p *palette) *color.Color { return &p.warn },
	"border":   func(p *palette) *color.Color { return &p.border },
	"user":     func(p *palette) *color.Color { return &p.user },
	"agent":    func(p *palette) *color.Color { return &p.agent },
	"diff_add": func(p *palette) *color.Color { return &p.diffAdd },
	"diff_del": func(p *palette) *color.Color { return &p.diffDel },
	"ask":      func(p *palette) *color.Color { return &p.ask },
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// customThemePath is where the file is, or "" without a config home.
func customThemePath(getenv func(string) string) string {
	p := pipeline.UserConfigPath(getenv)
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), customThemeFile)
}

// parseColour reads "#rrggbb" or an ANSI colour number 0-15.
func parseColour(v any) (color.Color, bool) {
	switch x := v.(type) {
	case string:
		if hexColour.MatchString(x) {
			var n uint32
			_, err := fmt.Sscanf(x[1:], "%06x", &n)
			return rgb(n), err == nil
		}
	case int64:
		if x >= 0 && x <= 15 {
			return lipgloss.ANSIColor(uint8(x)), true
		}
	}
	return nil, false
}

// parseCustomTheme reads a theme file strictly: an unknown key or a bad
// colour is a Problem, and then the caller uses the base preset alone.
func parseCustomTheme(data []byte) (*customTheme, []pipeline.Problem) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		// The parser's message may quote the file; the position is enough.
		var perr toml.ParseError
		if errors.As(err, &perr) {
			return nil, []pipeline.Problem{{Key: perr.LastKey, Msg: "not valid TOML"}}
		}
		return nil, []pipeline.Problem{{Msg: "not valid TOML"}}
	}
	var (
		c     customTheme
		probs []pipeline.Problem
	)
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := raw[k]
		switch k {
		case "base":
			s, _ := v.(string)
			if !slices.Contains(pipeline.KnownThemes, s) {
				probs = append(probs, pipeline.Problem{Key: k, Msg: "must be one of " + strings.Join(pipeline.KnownThemes, ", ")})
				continue
			}
			c.base = s
		case "accent", "ok", "bad", "warn", "border", "user", "agent", "diff_add", "diff_del", "ask":
			col, good := parseColour(v)
			if !good {
				probs = append(probs, pipeline.Problem{Key: k, Msg: "must be #rrggbb or an ANSI colour 0-15"})
				continue
			}
			if c.colours == nil {
				c.colours = map[string]color.Color{}
			}
			c.colours[k] = col
		default:
			probs = append(probs, pipeline.Problem{Key: k, Msg: "unknown key (base, accent, ok, bad, warn, border, user, agent, diff_add, diff_del, ask)"})
		}
	}
	if len(probs) > 0 {
		return nil, probs
	}
	return &c, nil
}

// overlay is p with the file's colours on top.
func (c *customTheme) overlay(p palette) palette {
	if c == nil {
		return p
	}
	for k, col := range c.colours {
		*themeKeys[k](&p) = col
	}
	return p
}

// reloadCustomTheme re-reads the theme file and applies it, and returns a
// one-line notice when the file is bad (the base preset is then used). A
// missing file clears any earlier custom theme. Nothing happens without
// colour: NO_COLOR wins. It is called between Runs, never during one.
func (s styles) reloadCustomTheme(getenv func(string) string) string {
	if s.th == nil {
		return ""
	}
	s.th.custom = nil
	path := customThemePath(getenv)
	if path == "" {
		return ""
	}
	data, err := readSmallFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("oge: reading %s: %v; using the %s preset", path, err, s.th.name)
	}
	c, probs := parseCustomTheme(data)
	if len(probs) > 0 {
		return fmt.Sprintf("oge: %s: %s; using the %s preset", path, probs[0], s.th.name)
	}
	s.th.custom = c
	return ""
}
