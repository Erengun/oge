package cli

import (
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The transcript draws the agent's replies as markdown: headings, lists,
// quotes, fenced code with a light highlight, pipe tables and the inline
// marks. It is a view: the reply's raw text stays the source of truth for
// /copy, alt+y, /export and the Ledger, and this file only ever turns it
// into lines to show. Without colour (NO_COLOR) the marks are dropped and
// nothing is painted, so no escape sequence is written. The framed tables
// follow pitago's (MIT, see THIRD_PARTY_NOTICES).

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdBullet  = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	mdNumber  = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	mdRule    = regexp.MustCompile(`^\s*([-*_])(\s*[-*_]){2,}\s*$`)
	mdTableSp = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	mdCode    = regexp.MustCompile("`([^`]+)`")
	mdBold    = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	mdItalic  = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*)\*`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// renderMarkdown is text drawn at width, every line indented by indent.
func (st styles) renderMarkdown(text string, width, indent int) string {
	pad := strings.Repeat(" ", indent)
	w := max(width-indent, 10)
	var out []string
	add := func(l string) { out = append(out, pad+l) }
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		trim := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~"):
			fence := trim[:3]
			lang := strings.TrimSpace(strings.TrimPrefix(trim, fence))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				code = append(code, lines[i])
			}
			for _, cl := range st.codeBlock(code, lang, w) {
				add(cl)
			}
		case mdHeading.MatchString(l):
			m := mdHeading.FindStringSubmatch(l)
			h := st.bold(st.inline(m[2]))
			switch len(m[1]) {
			case 1:
				h = st.ask(st.bold(strings.ToUpper(ansi.Strip(st.inline(m[2])))))
			case 2:
				h = st.ask(st.bold(ansi.Strip(st.inline(m[2]))))
			}
			for _, wl := range strings.Split(ansi.Wrap(h, w, ""), "\n") {
				add(wl)
			}
		case mdRule.MatchString(l):
			add(st.dim(strings.Repeat("─", min(w, 40))))
		case strings.HasPrefix(trim, ">"):
			var quote []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				quote = append(quote, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")))
			}
			i--
			for _, wl := range strings.Split(ansi.Wrap(st.inline(strings.Join(quote, " ")), w-2, ""), "\n") {
				add(st.dim("▎ ") + st.dim(wl))
			}
		case strings.Contains(trim, "|") && i+1 < len(lines) && mdTableSp.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			var tbl []string
			for ; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				tbl = append(tbl, lines[i])
			}
			i--
			for _, tl := range st.table(tbl, w) {
				add(tl)
			}
		case mdBullet.MatchString(l), mdNumber.MatchString(l):
			var lead, mark, body string
			if m := mdBullet.FindStringSubmatch(l); m != nil {
				lead, mark, body = m[1], "•", m[3]
				if len(lead) >= 2 {
					mark = "◦"
				}
			} else {
				m = mdNumber.FindStringSubmatch(l)
				lead, mark, body = m[1], m[2], m[3]
			}
			head := lead + mark + " "
			hw := ansi.StringWidth(head)
			wrapped := strings.Split(ansi.Wrap(st.inline(body), max(w-hw, 8), ""), "\n")
			for j, wl := range wrapped {
				if j == 0 {
					add(st.dim(lead+mark) + " " + wl)
				} else {
					add(strings.Repeat(" ", hw) + wl)
				}
			}
		case trim == "":
			add("")
		default:
			// A paragraph runs to the next blank line or block.
			para := []string{trim}
			for i+1 < len(lines) && isParaLine(lines, i+1) {
				i++
				para = append(para, strings.TrimSpace(lines[i]))
			}
			for _, wl := range strings.Split(ansi.Wrap(st.inline(strings.Join(para, " ")), w, ""), "\n") {
				add(wl)
			}
		}
	}
	return strings.Join(out, "\n")
}

// isParaLine reports whether lines[i] continues a paragraph.
func isParaLine(lines []string, i int) bool {
	l := lines[i]
	t := strings.TrimSpace(l)
	switch {
	case t == "", strings.HasPrefix(t, "```"), strings.HasPrefix(t, "~~~"), strings.HasPrefix(t, ">"),
		mdHeading.MatchString(l), mdBullet.MatchString(l), mdNumber.MatchString(l), mdRule.MatchString(l):
		return false
	case strings.Contains(t, "|") && i+1 < len(lines) && mdTableSp.MatchString(lines[i+1]):
		return false
	}
	return true
}

// italic is text in italics where there is colour; without it the marks
// are only dropped.
func (st styles) italic(s string) string {
	if st.th == nil {
		return s
	}
	return lipgloss.NewStyle().Italic(true).Render(s)
}

// inline draws the marks within a line: `code`, **bold**, *italic* and
// [links](url).
func (st styles) inline(s string) string {
	// Code spans first, held back so their text isn't read as marks.
	var spans []string
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, st.code(m[1:len(m)-1]))
		return "\x00" + string(rune('A'+len(spans)-1)) + "\x00"
	})
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		return p[1] + st.dim(" ("+p[2]+")")
	})
	s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
		p := mdBold.FindStringSubmatch(m)
		return st.bold(p[1] + p[2])
	})
	s = mdItalic.ReplaceAllString(s, "$1"+"\x01$2\x02")
	for {
		a := strings.IndexByte(s, 1)
		b := strings.IndexByte(s, 2)
		if a < 0 || b < a {
			break
		}
		s = s[:a] + st.italic(s[a+1:b]) + s[b+1:]
	}
	s = strings.NewReplacer("\x01", "", "\x02", "").Replace(s)
	for i, sp := range spans {
		s = strings.Replace(s, "\x00"+string(rune('A'+i))+"\x00", sp, 1)
	}
	return s
}

// code is an inline code span.
func (st styles) code(s string) string {
	if st.th == nil {
		return "`" + s + "`"
	}
	return st.warn(s)
}

// codeBlock is a fenced block's lines: a gutter, and the code lightly
// highlighted. The language, when given, is named dim above it.
func (st styles) codeBlock(code []string, lang string, width int) []string {
	var out []string
	gutter := st.dim("│ ")
	if lang != "" {
		out = append(out, st.dim("╭ "+lang))
	}
	for _, l := range code {
		l = strings.ReplaceAll(l, "\t", "    ")
		l = ansi.Truncate(highlight(st, l, lang), max(width-2, 4), "…")
		out = append(out, gutter+l)
	}
	return out
}

// Keywords of the languages the agents write most; a word is a keyword in
// any of them. There is no language detection beyond the fence's own.
var keywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`func return if else for range switch case default break continue go defer
		select chan map struct interface type package import var const nil true false
		def class from as with pass lambda yield async await try except finally raise in is not and or None True False self
		function let new this throw export extends static public private protected void int string bool
		fn impl pub use mut match enum trait where loop unsafe crate mod
		then fi do done echo`) {
		m[k] = true
	}
	return m
}()

// highlight paints one line of code: comments, strings, numbers and
// keywords. Without colour it is the line itself.
func highlight(st styles, line, lang string) string {
	if st.th == nil {
		return line
	}
	hash := false
	switch strings.ToLower(lang) {
	case "sh", "bash", "zsh", "shell", "py", "python", "yaml", "yml", "toml", "rb", "ruby":
		hash = true
	}
	var b strings.Builder
	rs := []rune(line)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '/' && i+1 < len(rs) && rs[i+1] == '/', hash && r == '#':
			b.WriteString(st.dim(string(rs[i:])))
			return b.String()
		case r == '"' || r == '\'' || r == '`':
			j := i + 1
			for j < len(rs) && rs[j] != r {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(rs))
			b.WriteString(st.ok(string(rs[i:j])))
			i = j
		case r >= '0' && r <= '9':
			j := i
			for j < len(rs) && (rs[j] >= '0' && rs[j] <= '9' || rs[j] == '.' || rs[j] == 'x' || rs[j] >= 'a' && rs[j] <= 'f') {
				j++
			}
			b.WriteString(st.warn(string(rs[i:j])))
			i = j
		case isIdent(r):
			j := i
			for j < len(rs) && (isIdent(rs[j]) || rs[j] >= '0' && rs[j] <= '9') {
				j++
			}
			w := string(rs[i:j])
			if keywords[w] {
				w = st.accent(w)
			}
			b.WriteString(w)
			i = j
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

func isIdent(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// table draws pipe-table rows in a frame; cells are cut to fit the width.
func (st styles) table(rows []string, width int) []string {
	var cells [][]string
	for i, r := range rows {
		if i == 1 {
			continue // the separator row
		}
		r = strings.TrimSpace(r)
		r = strings.TrimPrefix(r, "|")
		r = strings.TrimSuffix(r, "|")
		var row []string
		for _, c := range strings.Split(r, "|") {
			row = append(row, strings.TrimSpace(c))
		}
		cells = append(cells, row)
	}
	n := 0
	for _, r := range cells {
		n = max(n, len(r))
	}
	if n == 0 {
		return nil
	}
	cw := make([]int, n)
	for _, r := range cells {
		for j, c := range r {
			cw[j] = max(cw[j], ansi.StringWidth(ansi.Strip(st.inline(c))))
		}
	}
	// 3 cells of frame per column plus the last border.
	for sum := func() int {
		t := 1
		for _, c := range cw {
			t += c + 3
		}
		return t
	}; sum() > width; {
		k := 0
		for j := range cw {
			if cw[j] > cw[k] {
				k = j
			}
		}
		if cw[k] <= 4 {
			break
		}
		cw[k]--
	}
	line := func(l, m, r string) string {
		parts := make([]string, n)
		for j := range parts {
			parts[j] = strings.Repeat("─", cw[j]+2)
		}
		return st.dim(l + strings.Join(parts, m) + r)
	}
	var out []string
	out = append(out, line("┌", "┬", "┐"))
	for i, r := range cells {
		var b strings.Builder
		b.WriteString(st.dim("│"))
		for j := 0; j < n; j++ {
			c := ""
			if j < len(r) {
				c = st.inline(r[j])
			}
			if i == 0 {
				c = st.bold(ansi.Strip(c))
			}
			c = ansi.Truncate(c, cw[j], "…")
			b.WriteString(" " + c + strings.Repeat(" ", max(cw[j]-ansi.StringWidth(c), 0)) + " " + st.dim("│"))
		}
		out = append(out, b.String())
		if i == 0 {
			out = append(out, line("├", "┼", "┤"))
		}
	}
	return append(out, line("└", "┴", "┘"))
}
