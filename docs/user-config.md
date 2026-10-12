# User-level Öge config

Personal, per-repo choices that never go into git (#126, #127, #129).

**File:** `$XDG_CONFIG_HOME/oge/config.toml`, else `~/.config/oge/config.toml` (on macOS too). A missing file is fine. A file that doesn't parse, or has an unknown key, refuses the Run with a Problem naming the file; Öge never falls back silently.

**Precedence:** flags > this file (for this repo) > `.oge/oge.toml` > installed default. `oge run --dry-run` shows `(user)` beside values that come from here.

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

Today `mode` and the stage `agent` / `model` take effect on a Run. `unattended`, `apply` and `[profiles]` are parsed and exposed for the TUI and Claude profiles wiring (#131, #139).

**Writing:** `pipeline.SaveRepoDefaults` is the only writer and runs only on an explicit save. It writes atomically (0600 file in a 0700 directory), keeps other repos' sections and `[profiles]`, and refuses to overwrite an invalid file. Comments are not preserved.
