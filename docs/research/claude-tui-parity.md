# Claude Code TUI feature inventory for Öge parity

Research for [#148](https://github.com/Erengun/oge/issues/148), feeding the decision in [#143](https://github.com/Erengun/oge/issues/143). Research only: no decision is made here and no code changed. Claude Code version on this machine: 2.1.296 (`claude --version`); docs read 2026-10-12.

## Sources

All pages accessed 2026-10-12. Primary only.

- Interactive mode (keys, vim, history, queue, `!`, `@`, `/btw`, task list, suggestions): <https://code.claude.com/docs/en/interactive-mode>
- Commands reference (built-in commands and bundled skills): <https://code.claude.com/docs/en/commands>
- Keybindings: <https://code.claude.com/docs/en/keybindings>
- Terminal config (Shift+Enter, notifications, themes, paste, vim): <https://code.claude.com/docs/en/terminal-config>
- Fullscreen rendering (mouse, scroll, transcript search, `/focus`, `/tui`): <https://code.claude.com/docs/en/fullscreen>
- Status line: <https://code.claude.com/docs/en/statusline>
- Permission modes: <https://code.claude.com/docs/en/permission-modes>
- Output styles: <https://code.claude.com/docs/en/output-styles>
- `claude --help` run locally (flags only; no session started).

**Coverage limits.** I read the pages above in full. For MCP, hooks, IDE, checkpointing/rewind, session resume and agent view I relied on their one-line entries in the commands reference and on what the interactive-mode and keybindings pages say about them; those linked pages were not read in full, so rows marked (cmd-ref) should be re-checked before a ticket is written. Features I did not inspect: spell check as you type, voice dictation internals, PR review footer.

## How Öge differs (what the classes are measured against)

- A Run is supervised work: implementer, verifier, harness-run Checks, Accepted, then apply. The pipeline is frozen at Run start (GLOSSARY: Run). The TUI is a view over Ledger-derived state (ADR-0022).
- Öge launches Claude as `claude -p` stream-json with an empty `--setting-sources`, `--strict-mcp-config` and its own `--settings` (ADR-0004). The user's MCP servers, hooks, plugins, skills, memory and custom commands are not active inside a Run. Anything in Claude Code that lives in those is therefore "agent-owned" and currently switched off.
- Öge never reads, stores or uses the provider credential and has no login flow (ADR-0006). Any feature that needs Öge itself to make a model call has no way to do it except by launching an agent.
- ADR-0015: exit codes are a contract; no default choice at a Gate, Enter alone does nothing, no `--yes`/`--force`; the terminal never shows raw frames, credentials, held-out source outside the recorded inspect view, forbidden Briefing content, or unredacted output. ADR-0022 adds: keybindings may navigate and select but never shortcut a full-word trust-weakening entry.
- Exists today (`internal/cli/tui_home.go`, `tui_setup.go`, `tui.go`): composer with arrows, Ctrl+A/B/F/H/D/N/P/U, Home/End; newline on Ctrl+J, Shift+Enter, Alt+Enter; Ctrl+E opens the external editor; Enter runs; Esc and Ctrl+C leave an empty box, Ctrl+C clears a non-empty one; Tab opens the setup panel (agents, models, profiles, mode, toggles; trust-weakening marked); a Run view with Ctrl+O and Ctrl+C; Gate screens (`tui_gate.go`).

Class: **fits** = works as is for supervising a Run. **adapt** = different meaning in Öge. **agent-owned** = lives in the agent's session; Öge passes through or shows it. Size S/M/L is a rough build estimate. Ticketed: #144 history, #145 slash commands, #146 `@file`, #147 status line + transcript.

## The table

Guarantee-risk column: **none**, or the guarantee named. Rows with a guarantee risk other than "none" are the candidates for human decision.

### Input editing and keys

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 1 | Readline editing | Ctrl+A/E/K/U/W/Y, Alt+B/F/D, Alt+Y paste cycle, Ctrl+_ undo; Ctrl+W deletes to whitespace ([interactive-mode](https://code.claude.com/docs/en/interactive-mode), 2026-10-12) | fits | Composer already has half. Add kill ring, word motions, undo. Ctrl+E stays "editor" in Öge (it is end-of-line in Claude Code) | none | S | partly exists |
| 2 | Multiline | `\`+Enter, Ctrl+J, Shift+Enter, Option+Enter, paste ([terminal-config](https://code.claude.com/docs/en/terminal-config)) | fits | Ctrl+J, Shift+Enter, Alt+Enter exist; add `\`+Enter | none | S | partly exists |
| 3 | External editor | Ctrl+G or Ctrl+X Ctrl+E ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | fits | Ctrl+E exists; add Ctrl+G alias | none | S | exists |
| 4 | Large paste placeholder | >800 chars or >3 lines collapses to `[Pasted text #N +L lines]`, full text sent; invisible Unicode stripped on Enter ([terminal-config](https://code.claude.com/docs/en/terminal-config#paste-large-content)) | fits | The pasted text is the Task. Expand before the Task is recorded so the Ledger holds the real text | none | S | open |
| 5 | Ctrl+C / Esc semantics | Ctrl+C interrupts, else clears input, second press exits; Up restores cleared draft; Esc Esc clears draft or opens rewind ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | adapt | Empty box: leave. Box with Task: Ctrl+C clears (exists). During a Run, Ctrl+C is Run interrupt, exit 130, resumable. Draft restore on Up is new | ADR-0015 (130 is a status, must stay) | S | partly exists |
| 6 | Stash prompt | Ctrl+S stashes the draft, again restores it with mode ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | fits | Composer-only | none | S | open |
| 7 | Redraw, suspend | Ctrl+L redraw, Ctrl+Z suspend (Unix) | fits | Bubble Tea handles suspend; add Ctrl+L | none | S | open |
| 8 | Shortcut help | `?` on empty input toggles shortcut panel | fits | Panel listing Öge keys | none | S | open |
| 9 | Vim mode | `/config` Editor mode; INSERT/NORMAL/VISUAL, motions, d/c/y operators, text objects, `.`, `u`, `vimInsertModeRemaps` ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#vim-editor-mode)) | fits | Composer-only mode; Enter still submits in INSERT as in Claude Code. A setting in Öge config | none | L (3 slices) | open |
| 10 | Emoji shortcodes | `:name:` inserts emoji | fits | Low value | none | S | open, probably skip |
| 11 | Voice dictation | Hold/tap Space | fits | Out of scope for M1 unless asked | none | L | open, not recommended |
| 12 | Prompt suggestions | Grey next-prompt suggestion made by a background model request to the session model ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#prompt-suggestions)) | adapt | Needs a model call. Öge has no credential and no model client, so it would have to launch an agent just to suggest text. A local suggestion (last Task, git-history example) needs no model | ADR-0006 (if it ever calls a model itself) | S local / L model | open |

### History, search, slash commands, mentions, attachments

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 13 | Prompt history, Up/Down, Ctrl+R | Per-directory history, dedupe, scopes session/project/all, Tab accepts, Enter executes ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#command-history)) | fits | Task history | none | M | #144 |
| 14 | Slash commands (built-in) | `/` menu, mid-prompt completion, ~90 commands ([commands](https://code.claude.com/docs/en/commands)) | adapt | Öge-owned command set; most Claude commands do not apply (see rows below) | per command | M | #145 |
| 15 | Custom commands and skills | Markdown commands and skills appear in the `/` menu ([commands](https://code.claude.com/docs/en/commands)) | agent-owned | Inactive in a Run because ADR-0004 launches with empty setting sources. #145 should state this | ADR-0004 launch profile, Briefing (ADR-0009) | S (decision text) | #145 |
| 16 | `@file` mention | `@` path autocomplete ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | adapt | Refers to the Snapshot, not a live tree | held-out isolation (a mention must not resolve a held-out path) | M | #146 |
| 17 | Image paste | Ctrl+V/Cmd+V inserts `[Image #N]` chip ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | adapt | An image belongs to the Task. Which roles see it (implementer only, or verifier Briefing) is an Öge decision; stream-json image input needs a spike | Briefing contents (ADR-0009), held-out isolation | M | open |
| 18 | `!` shell mode | `!cmd` runs outside the agent and adds output to context, outside the sandbox ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#shell-mode-with-prefix)) | adapt | Runs in the user's repo, not the Run repository, so it can change the tree under a Snapshot, and its output enters the agent context outside the Briefing | Oracle, held-out isolation, Run repository rule | M | open |
| 19 | `/btw`, `/recap` | Side question or summary from existing context with no tools ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#side-questions-with-%2Fbtw)) | agent-owned | Model calls only available through an agent session. Öge's own recap is the Receipt | ADR-0006 | M | open, not recommended |

### Status, transcript, tasks, plan

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 20 | Built-in status line | Footer rows for model, mode, context, cost | adapt | Stage, elapsed, attention line, scope/check status (ADR-0022) | ADR-0015 never-shown list | M | #147 |
| 21 | Custom status line command | `statusLine` runs a user script every update (300ms debounce, optional `refreshInterval`) with JSON on stdin: model, cost, context_window, session_id, transcript_path, vim.mode, worktree, pr, prompt_cache and more ([statusline](https://code.claude.com/docs/en/statusline)) | adapt | Would let a user script read Run state. The JSON Öge feeds must be a fixed, redacted schema | ADR-0006 and ADR-0015 (script sees fields), held-out isolation | M | open |
| 22 | Transcript viewer | Ctrl+O toggles; shows tool use with timestamp/model; Ctrl+E show-all ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#transcript-viewer)) | adapt | Expandable activity from normalised events, `-v`/`-vv` levels | ADR-0015 (no raw frames, redacted) | M | #147 (Ctrl+O exists) |
| 23 | Transcript search and export | In fullscreen: `/` search, `n`/`N`, `{`/`}` prompt jumps, `[` write to scrollback, `v` open in `$EDITOR` ([fullscreen](https://code.claude.com/docs/en/fullscreen#search-and-review-the-conversation)) | adapt | Same on the redacted view; export file must be the redacted text | ADR-0015 | M | open (after #147) |
| 24 | Verbose and focus views | `/focus` shows last prompt, one-line tool summary, final response | fits | Matches Öge's terse default (ADR-0019 §8) and `-v`/`-vv` | none | S | exists in effect |
| 25 | Task list (todos) | Ctrl+T toggles Claude's checklist, up to 5 shown ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#task-list)) | agent-owned | Show the agent's plan items inside the activity line if the stream exposes them; display only | none (must pass redaction) | M | open |
| 26 | Plan mode | `/plan`, Shift+Tab mode `plan`: read-only exploration ([permission-modes](https://code.claude.com/docs/en/permission-modes)) | adapt | Öge has no free-chat plan phase; a plan stage would be a pipeline Stage, not a TUI toggle | frozen Pipeline | L | open, needs product call |
| 27 | Diff view | `/diff`: working-tree and per-turn diffs; panel in fullscreen ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#review-changes-with-%2Fdiff)) | adapt | Candidate versus Snapshot in the Run repository; held-out source only through the recorded inspect view | held-out isolation (ADR-0015 recorded view) | M | open |

### Run control

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 28 | Permission-mode cycling | Shift+Tab cycles default (Manual), acceptEdits, plan, auto, bypassPermissions ([permission-modes](https://code.claude.com/docs/en/permission-modes)) | adapt | Öge launches the agent in `default` with a host PreToolUse hook (ADR-0004); the user-facing equivalent is Öge's mode (setup panel). One key must never weaken it | ADR-0022/0015 (keystroke cannot shortcut a trust-weakening choice), ADR-0004 | S for safe cycle / M | open, decision |
| 29 | Interrupt and redirect | Esc interrupts, keeps work, next message steers ([interactive-mode](https://code.claude.com/docs/en/interactive-mode)) | adapt | Run-level interrupt exists. Mid-turn steering of a stage changes what the agent was told after the Briefing | Briefing (ADR-0009), frozen Pipeline | M | open, decision |
| 30 | Queued messages, Ctrl+Enter send-now | Enter while working queues; sends after tool calls or turn end ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#queue-messages-while-claude-works)) | adapt | Safe form: queue the next Task, started after this Run ends. Mid-Run injection is the same question as row 29 | as row 29 | M | open, decision |
| 31 | Background tasks | Ctrl+B backgrounds shells/agents; `/tasks`; `/background`, `/fork`, `claude agents` ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#background-bash-commands)) | adapt | Two meanings. (a) agent's background shells: agent-owned, show only. (b) detaching a Run: an attended Run needs a terminal and parking is unattended only | ADR-0015 (attended needs terminal, exit 10), ADR-0008 | M | open, decision for (b) |
| 32 | Usage-limit wait | Waits and auto-continues after a claude.ai limit resets ([interactive-mode](https://code.claude.com/docs/en/interactive-mode#wait-for-a-usage-limit-to-reset)) | agent-owned | Öge reports Infrastructure stop, exit 11, resumable | ADR-0015 exit 11 | S | open |
| 33 | `/resume`, session list | Picker by id or name; `/rename`, `/clear name` ([commands](https://code.claude.com/docs/en/commands), cmd-ref) | adapt | Picker over Runs from the Ledger; `oge resume`/`status` exist | none | M | open |
| 34 | `/rewind`, `/branch`, checkpointing | Restore code and/or conversation to an earlier point (cmd-ref) | adapt | Ledger and Oracle history are append-only (GLOSSARY: Oracle). Restoring state would contradict that; at most "start a new Run from Candidate N" | Oracle history, Ledger | L | open, decision |

### Model, cost, context

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 35 | Model picker | Option+P, `/model`, `/effort`, `/fast` apply live ([interactive-mode](https://code.claude.com/docs/en/interactive-mode), [keybindings](https://code.claude.com/docs/en/keybindings#model-picker-actions)) | adapt | Per-Stage model choice is in the setup panel (exists). Hotkey to open it from the composer is S. No mid-Run change: the Pipeline is frozen | none (verifier independence is already shown in setup) | S | exists, hotkey open |
| 36 | Thinking and fast toggles | Option+T, Option+O | agent-owned | Pass as agent launch options if the setup panel's toggles do not already | none | S | partly exists |
| 37 | Cost display | `/usage`, `/cost`, `cost.total_cost_usd` in the status line ([statusline](https://code.claude.com/docs/en/statusline)) | adapt | Sum of agent-reported usage per stage and Run; estimate only | none (must not claim a bill) | S | open, overlaps #147 |
| 38 | Context display and compaction | `/context` grid, `context_window.used_percentage`, `/compact`, `/autocompact` | agent-owned | Show percent if the stream reports it; compaction is the agent's | none | S | open |

### Look and configuration

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 39 | Themes | `/theme`: auto, dark, light, daltonized, ANSI; custom JSON in `~/.claude/themes/` with token overrides, hot reload ([terminal-config](https://code.claude.com/docs/en/terminal-config#match-the-color-theme)) | fits | Öge palette presets plus custom file. Status must not rely on colour alone | none | M | open |
| 40 | Output styles | `/output-style`: Concise, Explanatory, Learning, Proactive, custom; changes the agent system prompt ([output-styles](https://code.claude.com/docs/en/output-styles)) | agent-owned | Not passed: it would change agent instructions outside the Briefing. Öge's own verbosity is `-v` and terse default | Briefing (ADR-0009), launch profile (ADR-0004) | S (decision text) | open |
| 41 | Keybinding config | `~/.claude/keybindings.json`, contexts, actions, chords, hot reload, reserved keys ([keybindings](https://code.claude.com/docs/en/keybindings)) | adapt | Öge file with its own contexts. Gate contexts must refuse single-key bindings for typed full-word entries | ADR-0022/0015 | M-L | open, decision |
| 42 | Notifications | Terminal bell or desktop notification when Claude finishes or waits; `preferredNotifChannel`, Notification hook; OSC 7501 session status ([terminal-config](https://code.claude.com/docs/en/terminal-config#get-a-terminal-bell-or-notification)) | fits | Fire on "Attention needed" and on Run end. Fixed strings only, since notifications leave the terminal | ADR-0015 never-shown list (if body carried detail) | S | open |
| 43 | Terminal setup | `/terminal-setup` writes Shift+Enter bindings, Option-as-Meta, tmux notes | adapt | Öge prints instructions or a doctor hint; it does not edit terminal config files | none | S | open |
| 44 | Fullscreen, mouse, scroll | `/tui fullscreen`: alt screen, click-to-position, wheel, scrollbars, copy-on-select ([fullscreen](https://code.claude.com/docs/en/fullscreen)) | fits | Depends on what Bubble Tea mode Öge runs; mouse in lists and composer | none | M | open |
| 45 | Accessibility | `--ax-screen-reader` flat text output (`claude --help`) | fits | `--plain` exists; check it meets the same intent | none | S | exists in effect |
| 46 | Copy and export | `/copy [N]`, `/export` plain text (commands) | fits | Copy Receipt or last stage text; export must be the redacted view | ADR-0015 | S | open |
| 47 | Basics | `/help`, `/doctor`, `/status`, `/config`, `/exit`, `/clear`, `/release-notes`, `/feedback` | adapt | `oge doctor`, `oge status` exist; `/help`, `/exit`, `/setup` map to #145 | none | S | #145 |
| 48 | Login and logout | `/login`, `/logout` | agent-owned | Öge has no credential or login flow | ADR-0006 | none | not building |

### MCP, hooks, extensions, IDE

| # | Feature | Claude Code behaviour | Class | In Öge | Guarantee risk | Size | Status |
|---|---|---|---|---|---|---|---|
| 49 | MCP, hooks, plugins, skills, subagents, memory, `/permissions` | `/mcp`, `/hooks`, `/plugin`, `/skills`, `/agents`, `/memory`, `/permissions` (cmd-ref) | agent-owned | Disabled inside a Run by the launch profile (ADR-0004: empty setting sources, strict MCP config, auto memory off). A read-only "what the Run agent is allowed" view is possible | held-out isolation, Oracle (hooks/MCP in a Run could reach checks or held-out), ADR-0004 | S read-only | open; enabling is a decision |
| 50 | IDE integration | `/ide`, selection and diagnostics via the IDE MCP server (cmd-ref; [interactive-mode](https://code.claude.com/docs/en/interactive-mode) mentions the built-in IDE MCP server) | adapt | MCP is off in a Run, so this would be Öge-side context attach. Defer | Briefing, ADR-0004 | L | open, defer |
| 51 | Directory scope | `/add-dir`, `/cd` change what the agent may touch (cmd-ref) | adapt | The scope is the Snapshot and Gate-approved scope | Gates (scope), held-out isolation | M | open, not recommended |
| 52 | Cloud and remote | `/teleport`, `/remote-control`, `/schedule`, `/ultrareview`, `/desktop`, `/mobile`, `/chrome`, `/loop`, `/goal` (cmd-ref) | agent-owned | Out of scope for the TUI | none | none | not building |

## Counts

Counted from the table's Class column (52 rows):
fits 17, adapt 25, agent-owned 10. Ticketed already: rows 13 (#144), 14-16 (#145, #146), 20 and 22 (#147), 47 in part (#145). Rows 21, 35 and 37 overlap #147's scope and should be checked against it before slicing.

Guarantee-risk rows (anything other than "none"): 5, 12, 15, 16, 17, 18, 19, 20, 21, 22, 23, 26, 27, 28, 29, 30, 31, 32, 34, 40, 41, 42, 46, 48, 49, 50, 51. Of those, rows needing a human decision before any build are listed under "Needs a human decision" below.

## Proposed ticket slices

Each slice is sized for at most about 45 minutes of agent work with targeted tests (golden for plain mode, model tests for the TUI, per ADR-0022). "DECIDE" marks slices that must wait for a human decision because they touch a guarantee. Slices are independent unless a dependency is named.

### A. Composer editing (rows 1-8)
- **A1** Kill ring and word motions: Ctrl+K, Ctrl+W, Ctrl+Y, Alt+B/F/D, undo. Composer only.
- **A2** `\`+Enter newline, Ctrl+G editor alias, Ctrl+S stash and restore, Ctrl+L redraw.
- **A3** Large-paste placeholder with expansion before the Task is recorded; strip invisible Unicode and show the cleaned text for confirmation.
- **A4** Draft restore: Ctrl+C clears and Up brings it back; table-driven test of Esc/Ctrl+C meaning per screen (home, Run, Gate), confirming exit 130 is untouched.
- **A5** `?` shortcut panel generated from the key table (so it cannot drift).

### B. Vim mode (row 9), after A1
- **B1** Mode state, INSERT/NORMAL, h j k l w e b 0 $ ^, setting in Öge config.
- **B2** Operators and edits: x, dd, D, cc, cw, yy, p, u, `.`, and text objects iw, i", i(.
- **B3** VISUAL and VISUAL LINE, `jj` remap setting, history interaction with #144 (j/k at buffer edge).

### C. Run display (rows 20-25, 27, 37, 38), after #147
- **C1** Agent plan/todo items shown in the activity line, display only, redacted (row 25); needs a look at what stream-json exposes.
- **C2** Cost and context percent from agent usage events, labelled as estimates (rows 37, 38); skip if #147 already shows them.
- **C3** Transcript search (`/`, `n`, `N`) and `{`/`}` prompt jumps on the redacted transcript (row 23).
- **C4** Transcript export to scrollback or `$EDITOR`, redacted (rows 23, 46), plus `/copy` of the Receipt.
- **C5** Diff inspect view: Candidate versus Snapshot, file list and per-file diff (row 27). DECIDE only if it would show held-out files; otherwise reuse the recorded-view rule.

### D. Notifications and terminal (rows 42, 43, 44, 45)
- **D1** Bell and desktop notification on "Attention needed" and on Run end, fixed strings only, off by default or on by default as the human prefers; setting in config.
- **D2** `oge doctor` terminal hints (Shift+Enter, Option-as-Meta, tmux passthrough) instead of a terminal-setup command that edits files.
- **D3** Mouse in the composer and lists (click to position, wheel in lists), if the Bubble Tea mode allows it.
- **D4** `--plain` check against the screen-reader intent (flat text, no decoration).

### E. Themes (row 39)
- **E1** Palette presets: auto, dark, light, daltonized, ANSI-only; status never colour-only.
- **E2** Custom theme file with token overrides and hot reload.

### F. Sessions (row 33)
- **F1** Run picker over the Ledger (list, filter, open `status`, resume), the Öge `/resume`.
- **F2** Optional Run label/rename, if the Ledger can hold it without touching Evidence.

### G. Setup hotkeys (rows 35, 36)
- **G1** One hotkey from the composer to open the setup panel's model box; no mid-Run change.
- **G2** Check the setup panel toggles cover thinking and fast mode as launch options; add if missing.

### H. Read-only agent-config view (row 49)
- **H1** `/agent-config` style screen: states what the Run agent is launched with (no user MCP, hooks, plugins, memory) and why. Pure display of the ADR-0004 profile.

### I. Needs a human decision (do not slice further until decided)
Each needs a one-paragraph decision; the follow-up build slice is noted.

| ID | Decision needed | Guarantee at stake | Follow-up slices once decided |
|---|---|---|---|
| D-1 | Shift+Tab mode cycling (row 28): allow, and which modes? Safe form cycles only trust-neutral or strengthening options and types a word for anything weaker | ADR-0022 typed entries, ADR-0004 | 1 slice |
| D-2 | Steering or queueing messages into a running stage (rows 29, 30): allow, or queue the next Task only | Briefing (ADR-0009), frozen Pipeline | 2 slices (queue next Task; steer, if allowed) |
| D-3 | `!` shell mode (row 18): allow, and where it runs (user repo or Run repo) | Oracle, held-out isolation, Run repository | 2 slices |
| D-4 | Image paste (row 17): which roles receive images | ADR-0009, held-out isolation | 1 spike plus 1 build |
| D-5 | Custom status line command (row 21): allow, and the redacted JSON schema | ADR-0006, ADR-0015 | 2 slices (schema; runner with timeout) |
| D-6 | Keybinding config (row 41): allow, and which contexts are locked (Gate entry) | ADR-0022, ADR-0015 | 3 slices (action table refactor; file loader and validation; docs) |
| D-7 | Background Runs (row 31b): detach an attended Run? | ADR-0015, ADR-0008 | 2 slices |
| D-8 | Rewind, branch, fork (row 34): not building, or "new Run from Candidate N" only | Oracle history, Ledger | 1 slice if the second option |
| D-9 | User MCP, hooks, plugins, skills, output styles, custom commands inside a Run (rows 15, 40, 49): keep disabled (current position) or allow some | held-out isolation, Oracle, ADR-0004 | none if kept disabled |
| D-10 | Features needing a model call from Öge itself: prompt suggestions, `/btw`, `/recap` (rows 12, 19) | ADR-0006 | none unless a local-only form is chosen |
| D-11 | `/add-dir`, `/cd`, IDE context (rows 50, 51): not recommended; confirm | Gates (scope), Briefing | none |
| D-12 | Plan phase (row 26): a pipeline Stage or nothing | frozen Pipeline | product call, size L |

Rows 48 and 52 (login, cloud and remote) are proposed as not building; they need no ticket.

## Notes for whoever turns slices into tickets

- Everything in A, B, E, D1-D4, F, G, H is view or composer work with no path to the Ledger, Gates, Checks or credentials, so none needs a decision.
- Keep the plain-mode golden tests and exit-code tests (ADR-0015) in the acceptance criteria of every slice that touches key handling in a Run or Gate screen.
- Rows marked (cmd-ref) rest on one-line descriptions; read the linked feature page before writing the ticket.
