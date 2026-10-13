# User-level Öge config

Personal, per-repo choices that never go into git (#126, #127, #129).

**File:** `$XDG_CONFIG_HOME/oge/config.toml`, else `~/.config/oge/config.toml` (on macOS too). A missing file is fine. A file that doesn't parse, or has an unknown key, refuses the Run with a Problem naming the file; Öge never falls back silently.

**Precedence:** flags > this file (for this repo) > `.oge/oge.toml` > installed default. `oge run --dry-run` shows `(user)` beside the mode and the Stage bindings that come from here. `--standard`, `--fast` and `--blind` are the mode flags, so a flag can always override the mode set here.

```toml
schema = 1

[profiles]                       # extra Claude profiles kept outside ~/.claude-<name>
side = "/abs/path/to/claude-dir"

[repos."/abs/repo/root"]         # keyed by the repo root's absolute path
mode = "standard"                # fast | standard | blind
unattended = false
apply = false

[repos."/abs/repo/root".stages.implement]
agent = "claude@z"               # agent[@profile], carried as written
model = "sonnet"

[repos."/abs/repo/root".stages.verify]
agent = "claude@work"
```

**Keys.** A repo is keyed by the clean, absolute path of its root with symlinks resolved; `SaveRepoDefaults` writes that form and a key that isn't clean and absolute is a Problem. A git worktree is a separate repo with its own key.

**Bindings.** A Stage's `agent` takes `agent[@profile]` and brings its own `model` (leave it out for the agent's default); a `model` alone keeps the project's agent. A profile must be one Öge found (`~/.claude-<name>`) or one listed in `[profiles]`. Profiles are the only thing `.oge/oge.toml` may not bind. `agent` and `model` values that look like credentials are refused (ADR-0006).

**Trust.** `mode = "fast"` and `unattended = true` here are trust-weakening options (unattended removes the optional Gates, ADR-0008): dry-run lists them under "Trust-weakening options in effect" and the Receipt lists it under "Not covered", like `setup network = on`. `--require held-out` refuses it and names this file; pass `--standard`.

`mode`, the Stage bindings, `[profiles]`, `unattended` (as `--unattended`) and `apply` (as `--apply`) take effect on a Run. A typed flag beats them. The TUI setup (Tab on the home) sets the same keys for the session, with the source `tui` (it beats this file, like a flag), and offers once to save them here.

**Writing:** `pipeline.SaveRepoDefaults` is the only writer and runs only on an explicit save. It writes atomically (0600 file in a 0700 directory, through a symlinked config), keeps other repos' sections and `[profiles]`, and refuses to overwrite an invalid file. Comments are not preserved.

**Notifications.** The live view rings the terminal bell and sends a desktop notification (OSC 9) when a Run needs you (a Gate opens) and when it ends. The texts are fixed ("oge: waiting for you", "oge: Run accepted", "oge: Run ended: <outcome>"), never agent text. The `[notify]` table is global, not per repo, and every key defaults to on:

```toml
[notify]
attention = true   # a Gate opened
run_end = true     # the Run ended, any outcome
bell = true        # ring the bell with either
```

`/notify on|off` in the home turns both alerts (and so the bell) on or off for the session. Nothing is sent in plain mode, without a TTY, with `--unattended`, or in dry-run. Under tmux the OSC goes through the passthrough (needs `allow-passthrough on`); under GNU screen only the bell rings; foot and rxvt get OSC 777.

**Theme.** `theme` is a global key (not per repo) that sets the live view's palette: `auto` (the default: follows the terminal's background, and is the terminal's own 16 colours until it answers), `dark`, `light`, `daltonized` (blue and orange, never red against green) or `ansi` (16 colours only). An unknown value is a Problem.

```toml
schema = 1
theme = "daltonized"
```

The setup panel has a `theme` row; picking one repaints the open view at once, and [s] saves it here with the other choices. `NO_COLOR` still wins: no colour at all, and the row says so. A theme only changes colour: every status keeps its glyph and its word (`✓`, `✗`, `+`, `-`), so none is shown by colour alone.

**Custom theme.** For your own colours, put a `theme.toml` next to the user config (`$XDG_CONFIG_HOME/oge/theme.toml`, else `~/.config/oge/theme.toml`). It overrides colours on top of a base preset. Keys are `base` (one of the presets above; without it, the preset chosen by `theme`), and the colour roles `accent`, `ok` and `bad`, each `"#rrggbb"` or an ANSI colour number 0 to 15.

```toml
base = "dark"
accent = "#ff9e64"
ok = 10
```

The file is strict: an unknown key or a bad colour is named in a one-line notice in the home (with the file), and the view uses the base preset alone; it never stops oge. The file is re-read when the composer opens after a Run, and on Ctrl+L, never in the middle of a Run. `NO_COLOR` still wins, and the file changes colours only: every status keeps its glyph and its word.

**Vim mode.** `vim = true` is a global key (not per repo) that gives the composer vim's INSERT and NORMAL modes. It is off by default. Esc leaves INSERT for NORMAL (menus and pickers close on Esc first; in NORMAL on an empty box it does nothing), and `i a I A o O` go back to INSERT. In NORMAL: `h j k l`, `w e b` (words are by grapheme, as in the Emacs word keys), `0 ^ $`; counts work on them (`3w`). Edits: `x D`, the operators `d c y` with a motion (`w e b $ 0 ^ h l j k`) or a text object (`iw`, `i"`, `i(`), `dd cc yy` for whole lines, `p P` to put, `u` to undo, and `.` to repeat the last change (typed text included). `cw` acts like `ce`, as in vim. The register is the Ctrl+K and Ctrl+Y kill ring. Enter runs the task in both modes. `v` and `V` start VISUAL and VISUAL LINE: motions (with counts) extend the selection, shown in reverse video; `d` or `x`, `c`, `y` act on it and leave, `p` replaces it with the register (and the register gets what it replaced), and Esc or the same key again leaves. A visual command is not what `.` repeats. In NORMAL, `k` on the first line and `j` on the last walk the prompt history, as ↑ and ↓ do. The mode shows under the box as `-- INSERT --`, `-- NORMAL --`, `-- VISUAL --` or `-- VISUAL LINE --`. The setup-panel row is not there yet; set the keys in the file.

`vim_escape = "jj"` is a second global key: in INSERT, the two characters typed within 300 ms act as Esc, and the first one is taken back out of the text. It needs `vim = true`, takes exactly two printable characters, and is off when absent.

**Queued Task.** While a Run is in progress and no Gate is open, typing fills a plain-text `next ›` box under the Run (no vim modes); Enter keeps it and Ctrl+X cancels it. Nothing is sent to the Run, Esc still goes to the Run, and the first Ctrl+C clears the box before it cancels the Run. When the Run ends the text moves into the composer and waits for Enter.

```toml
schema = 1
vim = true
vim_escape = "jj"
```

## Mouse

The mouse works only while a list is open: scroll the wheel over the slash menu, the `/runs` picker or the `/diff` viewer to move the highlight, or the diff. Mouse reporting is off the rest of the time, on purpose. Öge prints the transcript into your terminal's own scrollback, and turning mouse reporting on would take over drag-to-select, copy, and the terminal's wheel scrollback for the whole session. So the composer and the transcript don't capture the mouse: select and copy text, and scroll back, the way your terminal does. Clicks aren't handled either: an inline frame has no known screen row to map a click to.

## Shell mode

A composer line that starts with `!` is a shell command, not a Task: `!git status` runs in your `$SHELL` (`cmd /C` on Windows) in the repo root, between Runs only, with no stdin. Its output (the last 2000 lines or 256 KB) and exit status print above the composer. Ctrl+C stops it (SIGINT to its process group, SIGKILL after a second) and does not quit Öge. The command never reaches an agent, a Briefing or the Ledger, and it goes to a shell history of its own, not the Task history. Shell mode takes one line: input with newlines is refused. To send a Task that starts with `!`, type `\!`.
