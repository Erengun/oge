package cli

import (
	tea "charm.land/bubbletea/v2"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// actionConsts maps the action constants' identifiers to their names.
func actionConsts(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "tui_actions.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) == 1 && len(vs.Values) == 1 && strings.HasPrefix(vs.Names[0].Name, "act") {
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok {
				out[vs.Names[0].Name], _ = strconv.Unquote(lit.Value)
			}
		}
		return true
	})
	return out
}

// handlerKeys reads the case labels of the key handlers' source, so the
// table is checked against what they really accept.
func handlerKeys(t *testing.T) map[string]bool {
	t.Helper()
	// file, receiver type, method
	handlers := [][3]string{
		{"tui_home.go", "composer", "key"},
		{"tui_edit.go", "composer", "editKey"},
		{"tui_vim.go", "home", "normalKey"},
		{"tui_vim.go", "home", "visualKey"},
		{"tui_vim.go", "home", "motion"},
		{"tui_home.go", "home", "update"},
		{"tui_home.go", "home", "searchKey"},
		{"tui_home.go", "home", "actionKey"},
		{"tui_slash.go", "home", "menuKey"},
		{"tui_at.go", "home", "atKey"},
		{"tui_runs.go", "home", "pickerKey"},
		{"tui_setup.go", "setupPanel", "key"},
		{"tui.go", "model", "Update"},
		{"tui_queue.go", "home", "queueKey"},
	}
	got := map[string]bool{}
	fset := token.NewFileSet()
	// A case label that is an action name (actEditor...) stands for the
	// action's keys: the handlers reach them through resolve.
	actNames := actionConsts(t)
	for _, h := range handlers {
		f, err := parser.ParseFile(fset, h[0], nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != h[2] {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); !ok || id.Name != h[1] {
				continue
			}
			found = true
			ast.Inspect(fn, func(n ast.Node) bool {
				if cc, ok := n.(*ast.CaseClause); ok {
					for _, e := range cc.List {
						if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							s, _ := strconv.Unquote(lit.Value)
							got[s] = true
						}
						if id, ok := e.(*ast.Ident); ok && actNames[id.Name] != "" {
							for _, a := range actionTable {
								if a.name == actNames[id.Name] {
									for _, k := range a.keys {
										got[k] = true
									}
								}
							}
						}
					}
				}
				return true
			})
		}
		if !found {
			t.Fatalf("handler %s.%s not found in %s", h[1], h[2], h[0])
		}
	}
	return got
}

// Every key a handler switches on is in the table (so the panel can't leave
// one out), and every key the table lists is one a handler switches on (so
// it can't promise one that isn't there). A handler's literal keys that are
// not shortcuts but answers to a prompt are listed here.
func TestKeyTableCoversTheHandlers(t *testing.T) {
	handled := handlerKeys(t)
	inTable := map[string]bool{}
	for _, b := range keyTable {
		if !slices.Contains(keyGroups, b.group) {
			t.Errorf("%q: unknown group %q", b.keys, b.group)
		}
		for _, k := range b.probes(nil) {
			inTable[k] = true
			if !handled[k] {
				t.Errorf("the table lists %q (%s) but no handler switches on it", k, b.keys)
			}
		}
	}
	// Not shortcuts: text the composer or a prompt takes as it is.
	notBindings := []string{" ", "space"}
	for k := range handled {
		if !inTable[k] && !slices.Contains(notBindings, k) {
			t.Errorf("a handler accepts %q but the key table doesn't list it", k)
		}
	}
	for _, g := range keyGroups {
		if !slices.ContainsFunc(keyTable, func(b keyBinding) bool { return b.group == g }) {
			t.Errorf("group %q is empty", g)
		}
	}
}

func TestShortcutPanelOpensOnlyOnAnEmptyComposer(t *testing.T) {
	h := testHome(80)
	typeText(h, "why?")
	if h.keys != nil || h.comp.text() != "why?" {
		t.Errorf("? in a Task: panel %v, text %q", h.keys != nil, h.comp.text())
	}
	h = testHome(80)
	typeText(h, "?")
	if h.keys == nil || h.comp.text() != "" {
		t.Fatalf("? on an empty box: panel %v, text %q", h.keys != nil, h.comp.text())
	}
	typeText(h, "x") // swallowed
	if h.comp.text() != "" {
		t.Errorf("a key reached the composer under the panel: %q", h.comp.text())
	}
	typeText(h, "?")
	if h.keys != nil {
		t.Error("? doesn't close the panel")
	}
	typeText(h, "?")
	h.Update(escKey())
	if h.keys != nil {
		t.Error("esc doesn't close the panel")
	}
}

func TestShortcutPanelGolden(t *testing.T) {
	h := testHome(80)
	typeText(h, "?")
	golden(t, "keys-panel", frame(h))
	for range 40 {
		h.Update(downKey())
	}
	golden(t, "keys-panel-end", frame(h))
	if n := strings.Count(frame(h), "\n"); n > 24 {
		t.Errorf("the panel frame is %d lines, past 24", n)
	}
}

// /help and the panel are the same table: every line of it is in both.
func TestHelpAndPanelAgree(t *testing.T) {
	help := advancedHelpText(nil)
	h := testHome(80)
	h.height = 0 // unwindowed: the whole table
	typeText(h, "?")
	panel := frame(h)
	for _, l := range keyLines(nil) {
		l = strings.TrimSpace(l)
		if !strings.Contains(help, l) {
			t.Errorf("/help lacks %q", l)
		}
		if !strings.Contains(panel, strings.Join(strings.Fields(l), " ")) && !strings.Contains(panel, l) {
			t.Errorf("the panel lacks %q", l)
		}
	}
}

func escKey() tea.KeyPressMsg  { return tea.KeyPressMsg{Code: tea.KeyEscape} }
func downKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }
