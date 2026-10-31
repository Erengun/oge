# Öge

**Öge makes delegation actually feel like delegation.**

Öge is the supervision layer for coding agents. Give it a task: it runs your coding agent, checks the work, and brings you in when your judgment is needed.

> **Status: pre-alpha.** Claude Code, Codex, and Google Antigravity (`agy`) runs, interactive chat, independent QA, repair loops, Receipts, and local delivery are implemented. Follow [milestone M1](https://github.com/Erengun/oge/milestone/1) for the first usable release.

## Build from source

Requires Go 1.27 and Git. To run tasks, use macOS or Linux with a supported coding agent installed and logged in (Claude Code, Codex, or Google Antigravity `agy`). Windows supports `--dry-run`, but cannot run tasks yet.

```sh
git clone https://github.com/Erengun/oge.git
cd oge
CGO_ENABLED=0 go build -o oge ./cmd/oge
./oge --version
```

Put the binary on your `PATH`, then open the Git repository you want to work on. Öge uses your existing agent login (Claude Code, Codex, or `agy` OS keyring session); it never asks for, reads, or stores provider credentials.

## Quick start

Start an interactive chat:

```sh
oge
```

On first use in a Go project, Öge proposes test commands and file patterns. Review the setup and the config diff before saving `.oge/oge.toml`. Other projects need explicit configuration; automatic setup currently recognises Go only.

Ask a question or describe a change. Replies stream into the terminal, and follow-ups stay in the same agent conversation. A conversation with no pending changes skips QA and test execution. When files change, Öge lets already queued follow-ups finish, then checks an immutable copy in the background while the composer stays available.

Use `/diff` to inspect the latest checked change, `/apply` to bring it into your working tree, and `/receipt` to see the evidence. Applying keeps the chat open. To apply successful changes automatically, enable **apply on accept** in setup (Tab) and save it for this repo. Decisions and conflicts still need your attention. `/help` lists everyday commands; `/help advanced` shows additional controls.

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

# Run with Google Antigravity (agy)
oge "fix the payment retry bug" --agent agy

# Set reasoning effort for agy (low, medium, high, xhigh, max)
oge "fix the payment retry bug" --agent agy --effort high

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

Standard mode needs checks with structured reports (`go-test-json` or `junit`). Project checks and protected test patterns live in `.oge/oge.toml`; agents cannot change them during a Run. Blind mode and `resume`, `status`, `cancel`, `init`, and `doctor` are not implemented yet.

## Where Öge is heading: the Desk

> **Planned, not shipped.** Everything in this section is design direction under way. Today's `oge` opens one Chat at a time.

The Desk is a full-screen home for several Chats at once, built so that supervising more than one agent adds less context switching, not more:

- **A rail with exact agent state.** Öge drives Claude Code, Codex, and `agy` over their structured streams and does not scrape the screen, so it knows whether an agent is working, waiting on you, or done. Each Chat keeps a fixed place in the rail.
- **An Inbox you triage one card at a time.** Approvals and decisions arrive as cards with the evidence attached, and you open them when you choose to.
- **Nothing interrupts you while you type.** The one exception is a Gate that has waited too long.
- **A "since you left" cue when you switch Chats.** It shows your last message, then what changed and ran, ending at the open question.
- **Öge stops prompting agents on your behalf.** A failed Check will ask you instead of automatically sending the agent a feedback turn.

Öge is not a terminal multiplexer. Tools such as [herdr](https://github.com/herdrdev/herdr) are terminal workspaces that host agents' terminals. Öge supervises the agents it drives and does not replace your terminal for shells, servers, or watchers.

Details: [Desk UX](docs/ux/desk.md) and [ADR-0024](docs/adr/0024-several-chats-one-attention-queue.md). The evidence behind it is in [herdr, Duet and human attention](docs/research/herdr-and-attention.md).

## Research Öge builds on

The Desk's attention rules come from work on interruptions, resumption, and supervising several automated workers. Each entry says what Öge takes from it.

**When to interrupt**

- Iqbal & Bailey (2008), CHI, [10.1145/1357054.1357070](https://doi.org/10.1145/1357054.1357070). Deferring notifications to task breakpoints lowers frustration; Öge holds notices while you compose and releases them when you send.
- Adamczyk & Bailey (2004), CHI, [10.1145/985692.985727](https://doi.org/10.1145/985692.985727). Interrupting between subtasks is far less annoying than mid-task; Öge never pushes during typing.
- McFarlane (2002), *Human-Computer Interaction* 17:63-139, [10.1207/S15327051HCI1701_2](https://doi.org/10.1207/S15327051HCI1701_2). Negotiated interruptions work best overall; you choose when to look, and only a stale Gate pushes.
- Züger et al. (2017), CHI, [10.1145/3025453.3025662](https://doi.org/10.1145/3025453.3025662). An automatic interruptibility signal (FlowLight) reduced interruptions; Öge reads "is the input box in use" for free.
- Matthews et al. (2004), UIST, [10.1145/1029632.1029676](https://doi.org/10.1145/1029632.1029676). Notifications come in levels from ignorable to demanding; Öge uses a fixed level table.
- Weiser & Brown (1996), "Designing Calm Technology". Information moves between the periphery and the centre of attention; the rail stays a quiet glyph, state, and age.

**Cost of switching, and getting back**

- Mark, Gudith & Klocke (2008), CHI, [10.1145/1357054.1357072](https://doi.org/10.1145/1357054.1357072). Interrupted work gets done faster but at the price of stress and effort; Öge judges the Desk by real time and stress, not felt speed.
- Mark, González & Harris (2005), CHI, [10.1145/1054972.1055017](https://doi.org/10.1145/1054972.1055017). A rearranged desktop destroys reorientation cues; each Chat keeps its rail position and number.
- Altmann & Trafton (2002), *Cognitive Science* 26:39-83. Pending goals are recovered through cues; the "since you left" cue supplies them.
- Trafton et al. (2003), *IJHCS* 58:583-603. A warning before an interruption helps people resume; Öge keeps your draft and place in each Chat.
- Parnin & Rugaber (2011), *Software Quality Journal* 19:5-34. Programmers rarely resume editing straight away; Öge treats resumption as a first-class moment.
- Parnin & DeLine (2010), CHI, [10.1145/1753326.1753342](https://doi.org/10.1145/1753326.1753342). Automatic resumption cues beat notes alone, and developers prefer chronological snippets; the switch cue is chronological.
- Leroy (2009), *OBHDP* 109(2):168-181. Attention stays partly on the task you left; Öge keeps switches cheap rather than pretending they are free.
- Leroy & Glomb (2018), *Organization Science* 29(3):380-397. A short ready-to-resume plan reduces that residue; the per-Chat draft and scroll position act as one.

**How many agents, and how to check on them**

- Olsen & Wood (2004), CHI, [10.1145/985692.985722](https://doi.org/10.1145/985692.985722). Fan-out is work time divided by interaction time; Öge lengthens the first (verify, repair) and shortens the second (evidence on the card).
- Cummings & Mitchell (2008), *IEEE Transactions on Systems, Man, and Cybernetics, Part A* 38(2):451-460. Waiting and lost situation awareness limit how many automated workers one person can supervise; Öge soft-caps active Chats.
- Cowan (2001), *Behavioral and Brain Sciences* 24:87-114. Working memory holds only a few chunks; this limits how many contexts you can keep in mind.
- Fitz et al. (2019), *Computers in Human Behavior* 101:84-94. Batching notifications improved attention and mood; Öge prefers an Inbox over a drip of alerts.
- Mark et al. (2016), CHI, [10.1145/2858036.2858262](https://doi.org/10.1145/2858036.2858262). People who check on their own initiative do better than those driven by notifications; triage is self-initiated.
- Jin & Dabbish (2009), CHI, [10.1145/1518701.1518979](https://doi.org/10.1145/1518701.1518979). Waiting and habitual checking are common self-interruptions, and an agent's run time is that kind of wait.
- Parasuraman & Manzey (2010), *Human Factors* 52(3):381-410. Automation complacency appears under multitask load, even in experts; every approval card carries its evidence and there is no bulk apply.
- Becker et al. / METR (2025), [arXiv:2507.09089](https://arxiv.org/abs/2507.09089). Developers using one agent were slower while believing they were faster; Öge measures rather than trusts impressions.

**An honest limit.** No study yet measures one developer supervising several coding agents. These findings come from neighbouring fields and are transfers, not proof. Öge measures its own users. The full note, with evidence grades and two commonly misquoted figures corrected, is [herdr, Duet and human attention](docs/research/herdr-and-attention.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development checks, commit sign-off, and licensing rules. Tests use fakes or recorded sessions, so CI spends no model quota.

Design decisions live in [`docs/adr/`](docs/adr/), vocabulary in [GLOSSARY.md](GLOSSARY.md), and product direction in [Positioning](docs/positioning.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
