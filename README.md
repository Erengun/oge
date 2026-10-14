# Öge

**Öge makes delegation actually feel like delegation.**

Öge is the supervision layer for coding agents. Give it a task: it runs your coding agent, checks the work, and brings you in when your judgment is needed.

> **Status: pre-alpha.** Claude Code runs, interactive chat, independent QA, repair loops, Receipts, and local delivery are implemented. The CLI currently ships with the Claude adapter; Codex integration is still in progress. Follow [milestone M1](https://github.com/Erengun/oge/milestone/1) for the first usable release.

## Build from source

Requires Go 1.27 and Git. To run tasks, use macOS or Linux with Claude Code installed and logged in. Windows supports `--dry-run`, but cannot run tasks yet.

```sh
git clone https://github.com/Erengun/oge.git
cd oge
CGO_ENABLED=0 go build -o oge ./cmd/oge
./oge --version
```

Put the binary on your `PATH`, then open the Git repository you want to work on. Öge uses your existing Claude Code login; it never asks for, reads, or stores provider credentials.

## Quick start

Start an interactive chat:

```sh
oge
```

On first use in a Go project, Öge proposes test commands and file patterns. Review the setup and the config diff before saving `.oge/oge.toml`. Other projects need explicit configuration; automatic setup currently recognises Go only.

Ask a question or describe a change. Replies stream into the terminal, and follow-ups stay in the same agent conversation. A turn with no changes skips QA and test execution. When a turn changes files, Öge checks an immutable copy in the background while the composer stays available.

Use `/diff` to inspect the latest checked change, `/apply` to bring it into your working tree, and `/receipt` to see the evidence. Applying keeps the chat open. `/help` lists commands and shortcuts; Tab opens setup for modes, agents, models, and checks.

For a single task with a fresh agent session:

```sh
oge "fix the payment retry bug"
oge diff
oge receipt --md
oge apply
```

Runs work on private copies of your repository, including uncommitted changes and untracked files that Git does not ignore. Changes reach your working tree when you apply them. `oge apply` never commits; `oge branch fix-payment-retry` creates a local branch containing the result without checking it out.

## How the work is checked

In Standard mode, a fresh QA agent sees your task and the changed code, without the implementer's reasoning or claims. It can add independent tests whose source stays hidden from the implementer. Öge runs the configured checks and those tests itself. Failed checks feed a bounded repair loop.

The result becomes **Accepted** only on the evidence Öge collects. Each completed Run gets a **Receipt** stating what was checked, what passed, and what was not covered. `oge receipt --md` formats it for a PR; `oge receipt --json` exports it for tooling. These commands default to the repository's latest Run and also accept a Run ID.

| Mode | What happens |
| --- | --- |
| Standard (default) | Implement, then independent QA and checks. |
| `--fast` | Implement and run checks, without independent QA or held-out tests. |
| `--blind` | Independent QA writes tests before implementation, then checks run. |

For a task with specific acceptance criteria, write Markdown and pass it with `--task-file`:

```markdown
# Fix payment retries

## Acceptance criteria
- Retry a transient failure at most three times.
- Do not retry a declined payment.
```

```sh
oge --task-file task.md
```

## Useful options

```sh
# Preview the configuration and stages without starting an agent
oge "fix the payment retry bug" --dry-run

# Select a discovered Claude profile
oge "fix the payment retry bug" --agent claude@work

# Require a mode with independent held-out tests
oge --task-file task.md --require held-out

# Print plain lines and never prompt
oge --task-file task.md --plain --unattended
```

Configure the project before running unattended. `--unattended` never answers a required human decision for you. `--confirm` adds a final decision before acceptance; `--apply` applies an Accepted result automatically. Run `oge --help` for all flags. Personal defaults, themes, vim mode, and keybindings are described in [User configuration](docs/user-config.md).

## Current limits

Öge is designed to catch ordinary agent mistakes, test manipulation, and straightforward attempts to bypass verification. Checks currently run unsandboxed, with network access, under your OS privileges. The MVP does not claim isolation against deliberately hostile code. Each Receipt states its coverage limits; Fast mode explicitly discloses the absence of independent tests.

Standard and Blind modes need checks with structured reports (`go-test-json` or `junit`). Project checks and protected test patterns live in `.oge/oge.toml`; agents cannot change them during a Run. `resume`, `status`, `cancel`, `init`, and `doctor` are not implemented yet.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development checks, commit sign-off, and licensing rules. Tests use fakes or recorded sessions, so CI spends no model quota.

Design decisions live in [`docs/adr/`](docs/adr/), vocabulary in [GLOSSARY.md](GLOSSARY.md), and product direction in [Positioning](docs/positioning.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
