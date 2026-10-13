package cli

import (
	"slices"
	"testing"
)

// The defaults #163's file overrides: action names and keys are a contract.
func TestActionDefaultKeys(t *testing.T) {
	want := []struct {
		ctx, name string
		keys      []string
	}{
		{"composer", "submit", []string{"enter"}},
		{"composer", "newline", []string{"ctrl+j", "shift+enter", "alt+enter"}},
		{"composer", "editor", []string{"ctrl+e", "ctrl+g"}},
		{"composer", "stash", []string{"ctrl+s"}},
		{"composer", "redraw", []string{"ctrl+l"}},
		{"composer", "keysPanel", []string{"?"}},
		{"composer", "cycleMode", []string{"shift+tab"}},
		{"composer", "model", []string{"alt+m"}},
		{"composer", "historySearch", []string{"ctrl+r"}},
		{"run", "expand", []string{"ctrl+o"}},
		{"run", "cancelQueue", []string{"ctrl+x"}},
	}
	if len(actionTable) != len(want) {
		t.Errorf("%d actions, want %d", len(actionTable), len(want))
	}
	for _, w := range want {
		if got := defaultKeyMap.actionKeys(w.ctx, w.name); !slices.Equal(got, w.keys) {
			t.Errorf("%s.%s: keys %v, want %v", w.ctx, w.name, got, w.keys)
		}
	}
}

func TestActionLookup(t *testing.T) {
	for _, c := range []struct{ ctx, key, want string }{
		{"composer", "ctrl+g", "editor"},
		{"composer", "alt+enter", "newline"},
		{"composer", "ctrl+r", "historySearch"},
		{"run", "ctrl+o", "expand"},
		{"run", "ctrl+x", "cancelQueue"},
		{"composer", "ctrl+x", ""}, // bound only in the run view
		{"composer", "ctrl+q", ""},
		{"nowhere", "enter", ""},
	} {
		if got := defaultKeyMap.actionFor(c.ctx, c.key); got != c.want {
			t.Errorf("actionFor(%s, %s) = %q, want %q", c.ctx, c.key, got, c.want)
		}
	}
	if got := defaultKeyMap.resolve("composer", "up"); got != "up" {
		t.Errorf("an unbound key resolves to itself, got %q", got)
	}
}

// A rebind is what the key table and the handlers both see.
func TestActionRebindReachesTheTable(t *testing.T) {
	kb := defaultKeyMap.with(map[keyOverride][]string{{"composer", "stash"}: {"ctrl+t"}})
	if kb.actionFor("composer", "ctrl+s") != "" || kb.actionFor("composer", "ctrl+t") != "stash" {
		t.Error("the rebind didn't take")
	}
	if defaultKeyMap.actionFor("composer", "ctrl+s") != "stash" {
		t.Error("the defaults changed")
	}
	found := false
	for _, b := range keyTable {
		if b.act == "composer.stash" && b.label(kb) == "ctrl+t *" {
			found = true
		}
	}
	if !found {
		t.Error("the key table kept the old key")
	}
}
