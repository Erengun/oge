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
