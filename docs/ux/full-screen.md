# The full-screen home

Bare `oge` on a terminal draws on the alternate screen (ADR-0022, amendment of 2026-10-19). `oge "task"`, `--plain`, no-TTY and `--json` are untouched. The layout is after pitago; the trust rules are Öge's own.

```
 oge · repo · main                                   ╭ SESSION ─────────────╮
  ❯ fix Add                                          │ ● working            │
  ╭─ run_command ─────╮                              │ agent  agy           │
  │ $ go test ./...   │                              │ model  …             │
  ╰───────────────────╯                              │ step   run_command … │
  ● view_file add_test.go                            │ time   12s           │
  Fixed Add: …                                       │ ATTENTION            │
 oge › ✓ Accepted · 1 file · 1 passed                │ TRUST                │
  add.go +1 -1                                       │ CHANGES              │
   @@ -1,3 +1,3 @@                                   │ add.go        +1 -1  │
   …                                                 │                      │
 ▸ /help — keys and commands                         │                      │
 (1/6) Tab complete · Enter accept · Esc close       │                      │
 ╭───────────────────────────────────────────────╮   │                      │
 │ ❯ Ask a question or describe a change         │   │                      │
 ╰─ ● ready · enter send · / commands ─ Fast · agy ╯  ╰──────────────────────╯
```

## Regions

- **Header** (1 row): `oge · repo · branch`.
- **Transcript viewport**: everything the home printed above its box, top-aligned while it fits and pinned to the bottom once it does not. It has its own scroll: wheel, PgUp and PgDn, `ctrl+end` (and End on an empty box). Scrolled up, a chip on its last row says how many lines lie below; clicking it or pressing End jumps to the latest. Sending a message jumps there too. Empty, it shows a small welcome (`● ready`, the hints).
- **Live area**: what used to be the inline frame, unchanged in content: the box, a turn's ask or question, panels, the action row. It keeps its bottom rows if it is taller than the screen.
- **Popup**: the `/` command list sits directly above the box: `▸ /cmd — description` rows (at most 8, fewer on a short screen), `(n/N) Tab complete · Enter accept · Esc close`. Tab and Enter complete as they always did; a delivery command still needs its second, typed Enter.
- **Input**: a bordered box, `❯` prompt, two text rows on a tall screen. The status is inside the bottom border: the state dot and word, the keys that work now, and the Launch profile on the right. Keys are dropped from the right, then the profile, so 80 columns hold the state and the main keys. The border takes the accent colour; a shell line (`!`) uses the warning colour and a `shell` label.
- **Toast**: a transient notice (copied, nothing to copy) over the viewport's top right for three seconds. It never holds a question.
- **Sidebar** (36 columns, from 110 columns up, `ctrl+t` toggles): SESSION (state, agent, model, mode, step, time, tokens and cost when the agent reported them), ATTENTION (whether Öge needs the human, and why), TRUST (verifying status, the last cut's Verdict, Agent limitations), CHANGES (the cut's files with `+N -M`). It reads state; it decides nothing.

## Transcript blocks

- The human's message: a tinted bar with `❯`.
- Replies: markdown (headings, lists, quotes, framed tables, inline marks, fenced code with a light highlight). The raw text is what `/copy`, `alt+y` and `/export` take.
- A shell tool step (`run_command`, `Bash`, ...): a rounded box with the command. Other steps: `●`, the bold tool name, the dim target. Öge's events carry no tool output, so there is no result tree yet.
- A cut's diff: the git headers are dropped; each file is a line with its path and `+N -M`, hunks are painted, a summary ends it; long diffs fold after 14 lines.
- `oge ›` notices are dim, not accent.
- `ctrl+o` unfolds long commands and diffs (in a Run's own view it keeps expanding the progress). `ctrl+g` stays the editor key.

## Mouse

The mouse is on: the wheel scrolls the viewport (or moves in an open popup or picker), a drag selects within the pane (clamped to it, scrolling at its edges) and copies on release (OSC 52, redacted like `/copy`). `alt+y` copies the last reply. Shift-drag still selects natively in most terminals.

## Editor

`$EDITOR` runs through `tea.Exec` as before; Bubble Tea releases and takes the alternate screen back (issue #228's renderer settle is kept).
