# herdr, Duet and human attention: research for the Desk

Snapshot taken **2026-10-31**. This note is the evidence behind `docs/ux/desk.md` and [ADR-0024](../adr/0024-several-chats-one-attention-queue.md).

**Question.** Öge wants to move toward herdr's side: one person, several agents, and a glance that tells you which one needs you. Two things need to be known first:
- How should this be built so that it reduces context switching instead of adding to it?
- What does the evidence on human attention say about it?

**Short answer.**
- **herdr** is a well-made terminal multiplexer. It reads agent state off the screen, and its open and closed issues show that screen-reading is exactly where the attention signal fails.
- **Öge** drives its agents over structured streams, so it knows their state exactly. It can be the calmer supervision surface for the agents it drives. It does not replace a terminal for shells, servers or watchers.
- Routing has to be **deterministic**. Asking agents to decide on the human's behalf (automatic feedback turns, an automatic Advisor) cost tokens and waiting time in our own use.
- The design follows the interruption and supervisory-control literature. No primary study covers one developer supervising several coding agents, so all of it is a transfer from neighbouring fields, and Öge has to measure it.

**Method.**
- **Products.** herdr was read from its own repository with `gh api` (`docs/next/website/src/content/docs/*.mdx`, latest release v0.9.5, 2026-10-14) and from its issue tracker. Duet was read from its launch article.
- **Literature.** Papers were read in full where possible. Each source is tagged:
  - **[P]**: primary full text read;
  - **[A]**: primary abstract only;
  - **[S]**: secondary sources only.
- **Öge facts** cite the code on `main` at `c3077da`.

## 1. Claude Duet

Source: [claude.com/resources/articles/claude-duet](https://claude.com/resources/articles/claude-duet).

- **What it is.** A research-preview app in which Claude is "a proactive agent" for professionals and small businesses: Gmail, Slack, QuickBooks, Stripe and similar.
- **How you use it.** Everything happens in one ongoing thread. Tasks run in the background on Duet's own VM. You get a notification "when a task finishes or needs review".
- **Approvals.** Before a consequential action it "shows you exactly what it's about to do and waits for your approval".
- **Attention.** It checks connected apps "every few hours" and decides what needs your attention. Anthropic says it is still tuning that judgement.

**Relevance.** Duet does no coding-agent supervision, so it is not a competitor. It does confirm Öge's promise ("Hand off the task. Öge brings you back when the work is ready or genuinely needs you", `docs/positioning.md`) as a product shape that a large vendor ships.

## 2. herdr

Repository: [herdrdev/herdr](https://github.com/herdrdev/herdr). Apache-2.0, about 47.1k★, Rust, v0.9.5.

**What it is.** "The runtime your coding agents live on." It is a terminal workspace (workspaces, tabs, panes) with:
- a background server, so you can detach and reattach (`ctrl+b q`);
- layout restore after a restart;
- SSH and phone attach;
- `worktree.*` methods;
- a socket API and CLI (`agent start | prompt | wait | read`) that agents use to drive each other;
- plugins.

"herdr doesn't wrap or replace them; it owns their terminals" (README).

**Agent states** (`concepts.mdx`):

| State | Meaning |
|---|---|
| `blocked` | needs input, approval or a decision |
| `working` | actively running |
| `done` | finished, not yet seen |
| `idle` | ready for input, already seen |
| `unknown` | no confident reading |

State rolls up from pane to tab to workspace, so "you can jump straight to the pane that needs attention" (`agents.mdx`).

**How herdr knows the state** (`agents.mdx`, "How status works"):
- For supported agents, it "reads the live bottom of the pane" against per-agent **detection manifests**. Those are screen rules, updated remotely from herdr.dev.
- Integrations can report state themselves. Among the big agents only OpenCode, Pi, Kimi and Kilo do. Claude Code, Codex and Antigravity are read from the screen.
- Blocked detection is "deliberately strict". If no rule matches, herdr falls back to `idle`.
- Agents that do not drive their own TUI report through `pane report-agent` ([add-herdr-support.mdx](https://herdr.dev/docs/add-herdr-support/)).

**Where screen-reading fails the human.** In each case below the attention signal is wrong at the moment it matters:
- [#5546](https://github.com/herdrdev/herdr/issues/5546) (open): an Antigravity subagent approval shows as `idle`. "No notification fires… the pending subagent approval silently expires."
- [#5490](https://github.com/herdrdev/herdr/issues/5490) (open): Codex's "Implement this plan?" picker shows as `idle`, with no attention mark.
- [#1217](https://github.com/herdrdev/herdr/issues/1217): Claude is waiting on a monitor, but herdr shows "complete".
- [#5024](https://github.com/herdrdev/herdr/issues/5024): a false "finished" toast while background subagents still run.
- [#803](https://github.com/herdrdev/herdr/issues/803), [#3186](https://github.com/herdrdev/herdr/issues/3186), [#5336](https://github.com/herdrdev/herdr/issues/5336), [#4649](https://github.com/herdrdev/herdr/issues/4649): detection lost after wrapper or agent updates.
- [#318](https://github.com/herdrdev/herdr/issues/318): the agents panel listed panes in workspace order, so "a blocked agent at the bottom of the list stays at the bottom". Priority sorting was added later.
- [#5377](https://github.com/herdrdev/herdr/issues/5377) and [#4787](https://github.com/herdrdev/herdr/issues/4787): duplicate notifications.

**Öge inside herdr today.** Öge drives Claude, Codex and agy headlessly: stream-json (ADR-0004, ADR-0023) and app-server (ADR-0018). herdr's manifests therefore never see an agent in an Öge pane, and Öge shows up as a plain terminal. Self-reporting through `pane report-agent` is the only way for Öge to appear correctly in herdr.

**What herdr does not do.** Its docs say nothing about verification, roles or evidence. On the `competitors.md` rubric it scores P1–P3 and P5, the same as the other parallel runners.

## 3. What asking agents for help cost us

Code on `main`:
- A failed background Check sends the implementer an automatic feedback turn (`internal/cli/tui_chat.go` `cutEnded` → `SendFeedback`). The budget is 3 per chain (`internal/run/feedback.go:16`, D5), and there is no setting to turn it off.
- When the budget is spent, an automatic Advisor Consultation opens a fresh Session on the strongest model at high effort and sends its advice as one more turn. This happens once per chain (`internal/run/advisor.go:148`); `[advisor] auto` defaults to `on` (D-A3, D-A5).
- So the worst case for one failing chain is 3 implementer turns, plus 1 Consultation, plus 1 advice turn. Each turn produces a new cut and another QA and Check.

In use, the user's verdict was that this "returned to us as more output token usage and more waiting bloat" (2026-10-31). A persistent "operator" agent that routes for the human would add the same costs, plus one more conversation to supervise. Practitioner write-ups say the same: parallelism is bounded by verification strength, and agents worth running in parallel "run their own acceptance checks… pass or revert", with no "input required" interrupts ([Tuddenham](https://joshtuddenham.dev/blog/stop-multitasking/) [S]).

## 4. Human attention: the literature

### Interruption timing
- **Iqbal & Bailey 2008** [P], CHI, [10.1145/1357054.1357070](https://doi.org/10.1145/1357054.1357070). The OASIS system deferred notifications to task breakpoints.
  - Frustration and reaction time fell compared with immediate delivery, at a mean deferral cost of about 1.5 min.
  - Deferring only to *fine* breakpoints was rated **more** frustrating than immediate delivery (diagram editing: Immediate 4.5, Fine 5.5, Medium 2.6, Coarse 3.6, on a 7-point scale).
- **Adamczyk & Bailey 2004** [P], CHI, [10.1145/985692.985727](https://doi.org/10.1145/985692.985727). Interrupting at the best moment gave 56% less annoyance than the worst moment. The best moments were between subtasks.
- **McFarlane 2002** [P], *Human–Computer Interaction* 17:63–139, [10.1207/S15327051HCI1701_2](https://doi.org/10.1207/S15327051HCI1701_2). Of four ways to coordinate an interruption (immediate, negotiated, mediated, scheduled), **negotiated was best overall**. Immediate won only where small differences in timeliness are critical.
- **Züger et al. 2017** [P], CHI, [10.1145/3025453.3025662](https://doi.org/10.1145/3025453.3025662). FlowLight, an automatic interruptibility status light, cut interruptions by 46% (logs from 36 of 449 participants).

### Interruption cost and two misquotes
- **Mark, Gudith & Klocke 2008** [P], CHI, [10.1145/1357054.1357072](https://doi.org/10.1145/1357054.1357072). Interrupted people finished faster with no loss of quality, but with more stress, frustration, time pressure and effort. *It does not measure resumption time.*
- **Mark, González & Harris 2005** [P], CHI, [10.1145/1054972.1055017](https://doi.org/10.1145/1054972.1055017). Field observation:
  - 57% of working spheres were interrupted.
  - When people came back to interrupted work the same day, the mean time **until they returned** was 25 min 26 s, with 2.26 other spheres in between.
  - A rearranged desktop destroys the cues people use to reorient.
- **Misquote 1.** "23 minutes to refocus" appears in neither paper as a measured refocus time. The measured figure is the time until people came back.
- **Misquote 2.** "40% of productive time lost to switching" is Meyer's characterization on [APA's page](https://www.apa.org/topics/research/multitasking), not a result of Rubinstein, Meyer & Evans 2001 [S].

### Resumption
- **Altmann & Trafton 2002** [P], *Cognitive Science* 26:39–83. In the memory-for-goals model, a pending goal is retrieved through cues, and old goals interfere.
- **Trafton et al. 2003** [P], *IJHCS* 58:583–603. A warning before an interruption let people encode what they meant to do next, and they resumed faster.
- **Parnin & Rugaber 2011** [P], *Software Quality Journal* 19:5–34 (ICPC'09 data: 10,000 sessions, 85 programmers). Only 10% of sessions started editing within a minute, and only 7% started without first navigating elsewhere.
- **Parnin & DeLine 2010** [A], CHI, [10.1145/1753326.1753342](https://doi.org/10.1145/1753326.1753342). Automatic resumption cues **doubled** task-completion success compared with notes alone. Developers preferred the chronological, snippet-based cue.

### Attention residue
- **Leroy 2009** [A], *OBHDP* 109(2):168–181. Attention stays partly on a task you left. Finishing under time pressure reduced that residue.
- **Leroy & Glomb 2018** [A], *Organization Science* 29(3):380–397. A brief "ready-to-resume plan" (where to resume, what remains) reduced residue and protected performance on the next task.
- **Masicampo & Baumeister 2011** [A], *JPSP* 101(4):667–683, doi:10.1037/a0024192. Unfulfilled goals keep intruding on thought (the Zeigarnik effect); letting people formulate a specific plan for them eliminated the activation and interference effects. Caveat for Later (desk.md I1): the manipulation was plan making, not bare capture, so a one-line Later item is a weaker form; and their 2012 follow-up (*Social Cognition*) found specific plans can narrow attention to alternatives.

### Capacity: how many agents
- **Olsen & Wood 2004** [P], CHI, [10.1145/985692.985722](https://doi.org/10.1145/985692.985722). Fan-out = activity time ÷ interaction time: how long a robot works alone between human touches, divided by how long each touch takes.
- **Cummings & Mitchell 2008** [A], *IEEE SMC-A* 38(2):451–460. Modelled wait times cut predicted fan-out by up to 67%. Lost situation awareness was the largest factor.
- **Cowan 2001** [A], *BBS* 24:87–114. Working memory holds about 4 chunks (3–5). This limits how many contexts a person keeps in mind, not how many agents can run.

### Peripheral display, batching, checking
- **Matthews et al. 2004** [P], UIST, [10.1145/1029632.1029676](https://doi.org/10.1145/1029632.1029676). Five notification levels: ignore, change-blind, make aware, interrupt, demand attention. **Weiser & Brown 1996**, "Designing Calm Technology" (conceptual): information moves between the periphery and the centre of attention.
- **Fitz et al. 2019** [S], *Computers in Human Behavior* 101:84–94. Batching phone notifications **3× a day** improved attention, mood and productivity; hourly batching showed little benefit.
- **Mark et al. 2016** [A], CHI, [10.1145/2858036.2858262](https://doi.org/10.1145/2858036.2858262). Batching email raised *rated* productivity but showed no stress benefit. People who checked on their own initiative did better than those driven by notifications.
- **Jin & Dabbish 2009** [S], CHI, [10.1145/1518701.1518979](https://doi.org/10.1145/1518701.1518979). Self-interruption types include "waiting / filling time" and habitual checking. An agent's run time is exactly that kind of wait.
- **Parasuraman & Manzey 2010** [A], *Human Factors* 52(3):381–410. Automation complacency appears under multiple-task load, affects experts too, and is not fixed by training.

### Agents specifically
- **METR, Becker et al. 2025** [P], [arXiv:2507.09089](https://arxiv.org/abs/2507.09089). 16 developers working with one agent were **19% slower** while believing they were 20% faster.
- **Anthropic, "Measuring AI agent autonomy in practice" (Feb 2026)** [P], [anthropic.com](https://www.anthropic.com/research/measuring-agent-autonomy). Median Claude Code turn about 45 s; the 99.9th-percentile turn grew from under 25 to over 45 minutes.
- **The gap.** No primary study measures one developer supervising several coding agents. Everything in section 5 is transferred.

## 5. Design principles for Öge (each one is used in `docs/ux/desk.md`)

1. **Hold while composing; release at a coarse breakpoint (send), never at a typing pause.** Sources: Iqbal & Bailey 2008; Adamczyk & Bailey 2004; Züger 2017. Öge knows when the input box is non-empty, so this signal costs nothing.
2. **Negotiate. The human chooses when to look; only a stale Gate or an expiring approval pushes.** Source: McFarlane 2002.
3. **Use a fixed level table: ignore, change-blind, make aware, interrupt, demand.** The periphery stays static: a glyph, a coarse state and an age, with no tool names and no animation. Sources: Matthews 2004; Weiser & Brown 1996; Jin & Dabbish 2009.
4. **Keep spatial order stable.** Each Chat keeps its rail position and number. Priority lives in the glyph, the header count and the Inbox order, not in reordering. Source: Mark, González & Harris 2005.
5. **Show a resumption cue on every switch**: your last message, then what changed and what ran, in time order, ending at the open ask. Typing is never swallowed. Sources: Parnin & DeLine 2010; Altmann & Trafton 2002; Trafton 2003.
6. **Keep the draft and scroll position per Chat.** This is a free ready-to-resume plan. Source: Leroy & Glomb 2018.
7. **Soft-cap active Chats at about 3. Raise fan-out by making the agent work longer alone (verify, repair) and making each human touch shorter (evidence on the card).** Sources: Olsen & Wood 2004; Cummings & Mitchell 2008; Cowan 2001.
8. **Self-initiated triage (the Inbox) instead of a notification drip.** Sources: Mark 2016; Fitz 2019.
9. **Put evidence on every approval card, with no bulk apply.** Source: Parasuraman & Manzey 2010.
10. **Measure real time and stress, not felt speed.** Sources: METR 2025; Mark 2008.

## 6. Recommendation

- Build the **Desk** described in `docs/ux/desk.md`: several Chats, a rail and an Inbox, with exact state taken from Öge's own event streams.
- Make failed Checks and the Advisor ask instead of auto-sending.
- Do not build a multiplexer, an operator agent, or agent-to-agent messaging.
- Offer herdr users `pane report-agent` interop later, so an Öge pane shows the Desk's rolled-up state.
