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
