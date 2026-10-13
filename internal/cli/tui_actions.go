package cli

import "slices"

// The action table (#162): the home's app-level shortcuts, as context →
// action name → keys. Handlers ask which action a key means in their context
// (resolve) instead of comparing key strings, and the key table that /help and
// the `?` panel draw (tui_keytable.go) reads its key column from here, so a
// remap (#163) shows up in both.
//
// Not in this table, and still switched on by their handlers: the Gate's keys
// (locked, #163), vim-mode keys, the composer's plain text editing (Emacs
// motions, kill ring, undo), and menu/picker/setup navigation.

// Contexts: the composer is the home with no Run; the run view is a Run's
// progress, expanded or not (and the home's queued-Task box beside it).
const (
	ctxComposer = "composer"
	ctxRun      = "run"
)

// Action names. They are stable: #163's file spells them.
const (
	actSubmit        = "submit"
	actNewline       = "newline"
	actEditor        = "editor"
	actStash         = "stash"
	actRedraw        = "redraw"
	actKeysPanel     = "keysPanel"
	actCycleMode     = "cycleMode"
	actModel         = "model"
	actHistorySearch = "historySearch"
	actExpand        = "expand"
	actCancelQueue   = "cancelQueue"
)

type actionBinding struct {
	ctx, name string
	keys      []string // as tea.KeyPressMsg.String() spells them
}

var actionTable = []actionBinding{
	{ctxComposer, actSubmit, []string{"enter"}},
	{ctxComposer, actNewline, []string{"ctrl+j", "shift+enter", "alt+enter"}},
	{ctxComposer, actEditor, []string{"ctrl+e", "ctrl+g"}},
	{ctxComposer, actStash, []string{"ctrl+s"}},
	{ctxComposer, actRedraw, []string{"ctrl+l"}},
	{ctxComposer, actKeysPanel, []string{"?"}},
	{ctxComposer, actCycleMode, []string{"shift+tab"}},
	{ctxComposer, actModel, []string{"alt+m"}},
	{ctxComposer, actHistorySearch, []string{"ctrl+r"}},
	{ctxRun, actExpand, []string{"ctrl+o"}},
	{ctxRun, actCancelQueue, []string{"ctrl+x"}},
}

// actionFor is the action key means in ctx, or "" when it means none.
func actionFor(ctx, key string) string {
	for _, a := range actionTable {
		if a.ctx == ctx && slices.Contains(a.keys, key) {
			return a.name
		}
	}
	return ""
}

// resolve is the switch tag for a handler: the action name when key is bound
// in ctx, else the key itself, so one switch holds both the actions and the
// keys the table doesn't cover. (No key is spelled like an action name.)
func resolve(ctx, key string) string {
	if a := actionFor(ctx, key); a != "" {
		return a
	}
	return key
}

// actionKeys are the keys bound to the action in ctx (nil if there is none).
func actionKeys(ctx, name string) []string {
	for _, a := range actionTable {
		if a.ctx == ctx && a.name == name {
			return a.keys
		}
	}
	return nil
}

// setActionKeys rebinds one action: the seam for user overrides (#163).
func setActionKeys(ctx, name string, keys []string) bool {
	for i, a := range actionTable {
		if a.ctx == ctx && a.name == name {
			actionTable[i].keys = keys
			return true
		}
	}
	return false
}
