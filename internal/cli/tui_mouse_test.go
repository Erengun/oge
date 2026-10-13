package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func wheel(h *home, b tea.MouseButton) {
	h.Update(tea.MouseWheelMsg{Button: b})
}

func TestMouseOffWithoutOverlay(t *testing.T) {
	h := testHome(80)
	if m := h.View().MouseMode; m != tea.MouseModeNone {
		t.Fatalf("mouse mode with no overlay: %v", m)
	}
	typeText(h, "fix the bug")
	wheel(h, tea.MouseWheelDown) // ignored: nothing is open
	if h.comp.text() != "fix the bug" {
		t.Fatalf("wheel changed the composer: %q", h.comp.text())
	}
}

func TestMouseWheelPicker(t *testing.T) {
	h, _ := pickerHome(t)
	if m := h.View().MouseMode; m == tea.MouseModeNone {
		t.Fatal("mouse mode off while the picker is open")
	}
	before := h.picker.l.Index()
	wheel(h, tea.MouseWheelDown)
	if h.picker.l.Index() != before+1 {
		t.Fatalf("wheel down: %d -> %d", before, h.picker.l.Index())
	}
	wheel(h, tea.MouseWheelUp)
	if h.picker.l.Index() != before {
		t.Fatalf("wheel up: %d", h.picker.l.Index())
	}
	h.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m := h.View().MouseMode; m != tea.MouseModeNone {
		t.Fatalf("mouse mode after the picker closed: %v", m)
	}
}

func TestMouseWheelSlashMenu(t *testing.T) {
	h := testHome(80)
	typeText(h, "/re")
	if h.menu == nil || h.View().MouseMode == tea.MouseModeNone {
		t.Fatal("menu open but mouse off")
	}
	before := h.menu.Index()
	wheel(h, tea.MouseWheelDown)
	if h.menu.Index() != before+1 {
		t.Fatalf("wheel down: %d -> %d", before, h.menu.Index())
	}
	if h.comp.text() != "/re" {
		t.Fatalf("wheel edited the composer: %q", h.comp.text())
	}
}
