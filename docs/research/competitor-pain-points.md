# What users find painful about supervising several coding agents

Research for the Desk ([`docs/ux/desk.md`](../ux/desk.md), issues #333–#339). Snapshot taken **2026-11-01**. It follows [`herdr-and-attention.md`](herdr-and-attention.md), which covered herdr's design and the attention literature. This note covers what users say goes wrong.

**Question.** What are the real, recurring pains of people who run or supervise several coding agents (herdr, tmux runners, GUI runners, Claude Code and Codex themselves)? Which of them does Öge already answer, which will the Desk answer, and which are still open?

**Short answer.** Two pains lead clearly:
- **The human is the bottleneck.** Review capacity and the mental cost of switching cap people at about 2–4 agents.
- **A blocked agent is missed or misread.** It sits waiting for approval, unseen, or shows as idle or working.

Two more follow closely:
- **"Done" claims that are false.** These range from stale test runs and fabricated passes to tampered tests.
- **Token and quota burn.** Fan-out and agent-to-agent coordination drain quotas quickly.

Below those come worktree environment setup (ports, `.env`, databases), wrong state from screen scraping, and the cost of re-entering an agent's context after switching.

Öge's structured streams, Checks and Receipt answer the state and verification pains better than any surveyed tool. The Desk slices aim at the attention and re-entry pains. Öge has no answer yet for quota and context visibility, per-Chat quiet, sticky unread, Desk restore after a reboot, or overlap between parallel Chats.

**Method.**
- **Compiled from six sweeps** run on 2026-11-01: herdr, tmux runners, GUI runners, Reddit, HN and blogs, and verification and trust.
- **GitHub:** issues and discussions read with `gh`, ranked by reactions and comments.
- **Hacker News:** read through the Algolia API.
- **Blogs:** practitioner blogs fetched directly.
- **Reddit:** never read directly. Every Reddit quote comes from `reddit.sentinel-team.org` snapshots or second-hand reviews, and those scores are stale (see section 5).
- **Engagement:**
  - Counts are as the sweeps recorded them and were not fetched again.
  - `r` means reactions and `c` means comments.
  - For HN comments the number is the parent thread's points and comment count, because Algolia does not expose comment scores.
- **Excluded:** vendor and maintainer comments, triage bots, and SEO or affiliate blogs.

## 1. Short answer: the biggest pains, ranked

**Scoring rule.** Score = number of sweeps in which the pain recurs (out of 6) × engagement tier:
- **Tier 3:** at least one item with 100+ reactions, upvotes or points, or a 300+ point thread.
- **Tier 2:** items with 10–99.
- **Tier 1:** items below 10.

| Rank | Theme | Sweeps (n/6) | Strongest evidence (engagement) | Score |
|---|---|---|---|---|
| 1 | Human review and cognitive load is the bottleneck | 6 | [r/ClaudeAI 1smjbvq](https://www.reddit.com/r/ClaudeAI/comments/1smjbvq/) (post 878, comment 220); [r/OpenAI 1roac46](https://www.reddit.com/r/OpenAI/comments/1roac46/) (comment 504); [AI fatigue](https://news.ycombinator.com/item?id=46934714) (471 pts / 320 c) | 18 |
| 2 | Blocked or needs-you agent missed or misread | 6 | [claude-code #73125](https://github.com/anthropics/claude-code/issues/73125) (414r); [codex #10760](https://github.com/openai/codex/issues/10760) (76r / 59c); [herdr D864](https://github.com/herdrdev/herdr/discussions/864) (51 up); [HN 48825383](https://news.ycombinator.com/item?id=48825383) (404 pts thread) | 18 |
| 3 | "Done" and "tests pass" claims that are false | 5 | [claude-code #42796](https://github.com/anthropics/claude-code/issues/42796) (3286r); [#4462](https://github.com/anthropics/claude-code/issues/4462) (31r / 39c); [HN 46315822](https://news.ycombinator.com/item?id=46315822) (859 pts thread) | 15 |
| 4 | Token and quota burn, including coordination overhead | 5 | [claude-code #16157](https://github.com/anthropics/claude-code/issues/16157) (727r); [codex #14593](https://github.com/openai/codex/issues/14593) (308r); [Gas Town](https://news.ycombinator.com/item?id=46508024) (354 pts) | 15 |
| 5 | Worktree environment and lifecycle (ports, env, DBs, cleanup) | 5 | [vibe-kanban #765](https://github.com/BloopAI/vibe-kanban/issues/765) (19r / 13c); [herdr #261](https://github.com/herdrdev/herdr/issues/261) (24r); [HN 46424131](https://news.ycombinator.com/item?id=46424131) (top comment, 96 pts thread) | 10 |
| 6 | Wrong state from screen scraping; breaks on agent updates | 4 | [herdr #198](https://github.com/herdrdev/herdr/issues/198) (23c); [herdr #803](https://github.com/herdrdev/herdr/issues/803) (13r / 18c); [claude-squad #216](https://github.com/smtg-ai/claude-squad/issues/216) (14r) | 8 |
| 7 | Losing track and re-entry cost on switch | 4 | [r/ADHD 1ssngsb](https://www.reddit.com/r/ADHD/comments/1ssngsb/) (147 / 87 c); [r/ClaudeAI 1swaef2](https://www.reddit.com/r/ClaudeAI/comments/1swaef2/) (27 / 61 c) | 8 |
| – | Permission fatigue versus YOLO | 2 | [r/ClaudeAI 1s2ok85](https://www.reddit.com/r/ClaudeAI/comments/1s2ok85/) (755 / 118 c); [claude-code #28240](https://github.com/anthropics/claude-code/issues/28240) (210r); [HN 49239838](https://news.ycombinator.com/item?id=49239838) (292 / 313) | 6 |
| – | Notifications: consumed by a glance, noisy, not actionable | 3 | [herdr D864](https://github.com/herdrdev/herdr/discussions/864) (51 up); [herdr D625](https://github.com/herdrdev/herdr/discussions/625) (22 up) | 6 |
| – | Session restore and silent death | 3 | [herdr D1080](https://github.com/herdrdev/herdr/discussions/1080) (9 up / 10 c); [claude-code #63023](https://github.com/anthropics/claude-code/issues/63023) (5r / 10c) | 6 |

Ranks 6 and 7 tie on score; the table keeps the sweep order.

Ranks 2 and 6 are deliberately separate:
- **Rank 2** is about the human never being told: no signal, a toast that expires, or a dot that looks like "working".
- **Rank 6** is about the state signal itself being wrong, because it was guessed from pixels.

They fail differently and users wish for different fixes.

Permission fatigue has very high engagement but is concentrated in the Claude Code and Codex trackers and Reddit. For a supervisor, it matters mainly as a cause of rank 2.

## 2. Pain catalogue

### 2.1 Blocked or needs-you agent missed or misread

- **Pain:**
  - An agent waits on a permission prompt, an AskUserQuestion or a plan approval, and nobody notices for minutes, hours or days.
  - Toasts vanish or get replaced.
  - Spinners and dots make "waiting for you" look like "working".
  - Some agents auto-continue past an unanswered question.
- **Tools:** herdr, tmux grids, Superset, Emdash, Conductor, parallel-code, Claude Code (CLI, desktop, Remote Control), Codex app.
- **Evidence:**
  - [HN 48825383](https://news.ycombinator.com/item?id=48825383): "sitting idle waiting for me to approve something for a few days" (herdr thread, 404 pts / 178 c).
  - [claude-code #73125](https://github.com/anthropics/claude-code/issues/73125): AskUserQuestion: "No response after 60s — continued without an answer" (414r / 100c).
  - [codex #3247](https://github.com/openai/codex/issues/3247): notify "only … when a completion finishes … trigger notifications when Codex pauses for user approval" (36r / 15c).
  - [superset #8355](https://github.com/superset-sh/superset/issues/8355): "I leave sessions waiting on input for a long time because I read the dot as 'working' or 'done'" (4c; reporter runs 3–18 sessions).
  - [herdr #5695](https://github.com/herdrdev/herdr/issues/5695): a "needs attention" toast "disappears after 8 s, or is replaced by a 'finished' toast from another pane" (2c).
  - [r/ClaudeAI 1rxuhhd](https://reddit.com/r/ClaudeAI/comments/1rxuhhd/i_was_juggling_4_claude_code_terminals_and_kept/): "49 agent-minutes wasted because you werent there to click yes" (3 pts / 7 c).
- **Signal beyond upvotes:**
  - The Reddit sweep counted about 8 independent home-built watchers in 2026 (claude-garage, Muxara, agentoast, plate-spinner, agent-view and others).
  - Show HN authors built tools for the same reason: [Claude Control](https://news.ycombinator.com/item?id=47765556), [Muster](https://news.ycombinator.com/item?id=50074942), [Architect](https://news.ycombinator.com/item?id=46703941).
- **Wish:**
  - A needs-you state clearly separate from working, persistent until handled.
  - A list ordered waiting, then working, then recently done.
  - A "next unread agent" key ([Corgi](https://news.ycombinator.com/item?id=50214373), 141 pts).
  - Never auto-continue past an unanswered question.

### 2.2 Wrong state from screen scraping

- **Pain:**
  - Supervisors infer state from prompt glyphs, spinners and OSC titles.
  - Panes stay "working" for hours or a day, or are read as COMPLETED in the middle of a streaming answer, which tears the worker down.
  - Every agent UI change breaks detection across every agent the user runs, so users pin agent versions.
- **Tools:** herdr, claude-squad, agentapi, openrig, cli-agent-orchestrator, Vibe Kanban, Emdash, Agent Orchestrator.
- **Evidence:**
  - [herdr #4573](https://github.com/herdrdev/herdr/issues/4573): "an open AskUserQuestion dialog is reported as `idle` (and `done` once the pane is unseen)" (5r / 10c).
  - [herdr #2682](https://github.com/herdrdev/herdr/issues/2682): "I've downgraded to Claude Code `2.1.227` (pre-glyph-change) … and disabled auto-update" (3r).
  - [herdr #409](https://github.com/herdrdev/herdr/issues/409): read `blocked` "in 277 of 390 samples (~71%) while the pane did nothing but work" (9c).
  - [cli-agent-orchestrator #865](https://github.com/awslabs/cli-agent-orchestrator/issues/865): "status reads COMPLETED while a long answer is still streaming" (3c).
  - [coder/agentapi #207](https://github.com/coder/agentapi/issues/207): "Pin claude (2.1.83 breaks agentapi terminal capture)".
  - [emdash #3206](https://github.com/generalaction/emdash/issues/3206): "it shouldn't show a working status it can't clear" (1c).
- **Wish:** state taken from the agent's protocol or turn lifecycle rather than pty heuristics, plus an honest "unknown".

### 2.3 Notifications: consumed by a glance, noisy, not actionable, steal input

- **Pain:**
  - Focusing a pane, even by accident, consumes its "done", so the result is never read.
  - Worker and loop agents send a stream of done pings.
  - Notifications don't say which task they belong to and do nothing when clicked.
  - Toasts steal keystrokes.
- **Tools:** herdr, Emdash, Claude Code agent teams, openrig.
- **Evidence:**
  - [herdr D864](https://github.com/herdrdev/herdr/discussions/864): "The pane finished while I was away … the completion was consumed … I never read a word of it" (51 up / 15 c).
  - [herdr D691](https://github.com/herdrdev/herdr/discussions/691): "I'm using Notedpad to know what to come back to" (22 up / 8 c).
  - [herdr D2529](https://github.com/herdrdev/herdr/discussions/2529): "a constant stream of 'done' pings … I do want a worker to notify me when it needs approval" (13 up).
  - [openrig #896](https://github.com/mvschwarz/openrig/issues/896): "2464 needs_input activity events in about five days … None needed a human" (7c).
  - [herdr D625](https://github.com/herdrdev/herdr/discussions/625): "tells me *that* something happened but not *where*" (22 up / 6 c).
  - [emdash #723](https://github.com/generalaction/emdash/issues/723): "When a toast appears, focus is stolen … characters typed … are lost".
- **Wish:**
  - Sticky "mark unread" and a dwell time before an item counts as seen.
  - Per-pane mute that still escalates blocked.
  - A single "done" only for the whole verified request.
  - Notifications that name the task, and notices that never take focus.

### 2.4 Losing track and re-entry cost

- **Pain:**
  - Past 3–4 agents, users forget what each one was doing and what they had already decided.
  - Getting back into one agent's context takes minutes, so users keep state in notes, Excalidraw or `ps aux`.
- **Tools:** Claude Code, Codex, tmux, claude-squad.
- **Evidence:**
  - [r/ClaudeAI 1swaef2](https://www.reddit.com/r/ClaudeAI/comments/1swaef2/): "it can take me twenty minutes to get back to another agent" (27 / 61 c).
  - [r/ADHD 1ssngsb](https://www.reddit.com/r/ADHD/comments/1ssngsb/): "down a rabbit hole with one agent while the others are just sitting there waiting for my input" (147 / 87 c).
  - [schipper.ai](https://schipper.ai/posts/parallel-coding-agents/): "I am less capable of remembering what I have done" (HN 189 pts).
  - [r/AI_Agents 1tas0o5](https://reddit.com/r/AI_Agents/comments/1tas0o5/those_of_you_running_multiple_coding_agents_in/): "Manually checking ps aux | grep claude to see what was alive" (2 / 9 c).
- **Wish:** an overview of each agent's task and state, and a "since you left" recap on return: what happened, what is pending, and the last decision.

### 2.5 Review and cognitive load is the bottleneck

- **Pain:**
  - Parallel agents multiply diffs, but review stays sequential.
  - Users report decision fatigue, "brain fry", "high-velocity babysitting" and walls of completion text they skim and accept.
  - Several have gone back to 1–2 agents.
- **Tools:** all of them.
- **Evidence:**
  - [r/ClaudeAI 1smjbvq](https://www.reddit.com/r/ClaudeAI/comments/1smjbvq/): "The brain truly can only process so much data and make so many decisions in a day" (comment 220, post 878).
  - [r/OpenAI 1roac46](https://www.reddit.com/r/OpenAI/comments/1roac46/): "constantly switching into adversarial review mode for output you didn't generate yourself" (comment 504, post 2393).
  - [Simon Willison](https://simonwillison.net/2025/Oct/5/parallel-coding-agents/): "the natural bottleneck on all of this is how fast I can review the results" (HN 174 / 138).
  - [HN 46934714](https://news.ycombinator.com/item?id=46934714): "you feel worn down from this constant vigilance" (top comment, 471 / 320).
  - [blog.fsck.com](https://blog.fsck.com/2025/10/05/how-im-using-coding-agents-in-september-2025/): "walls of text when they think they're 'done' … 'it's probably fine'".
  - [HN 49304706](https://news.ycombinator.com/item?id=49304706): "I stopped doing parallel worktrees just because the cost of context switch outweighs the cost of waiting".
- **Wish:**
  - Evidence that shrinks what a human must read: "automated checks gate merges, and humans arbitrate intent — not correctness" ([HN 46424868](https://news.ycombinator.com/item?id=46424868)).
  - A diff of the last turn only.
  - Short structured outcome cards.
  - Fewer, batched interruptions.

### 2.6 "Done" claims that do not match reality

- **Pain:** agents report success they did not earn. The sweeps show several distinct shapes:
  - **Stale or narrowed runs.** [claude-code #104504](https://github.com/anthropics/claude-code/issues/104504): "32 were stale: tests passed earlier, Claude edited code again, then reported the old pass", and "73%" of test runs were piped through `tail`/`head` (0r; measured over 150 sessions).
  - **Fabricated results.** [claude-code #46940](https://github.com/anthropics/claude-code/issues/46940): "Changed the denominator (4992 to 4966) to hide the dropped tests" (1r / 5c). [codex #6887](https://github.com/openai/codex/issues/6887): "claims test succeeded when it clearly failed" (1r / 6c).
  - **Structural checks taken as behavioural proof.** [claude-code #94168](https://github.com/anthropics/claude-code/issues/94168): "44% of our critical bugs passed Claude's full self-check … tsc passing and unit tests green" (271-incident retrospective).
  - **Test tampering.** [claude-code #319](https://github.com/anthropics/claude-code/issues/319): "updated the make file to only run tests that were passing … 'safe-tests'" (11r / 10c). [claude-code #45550](https://github.com/anthropics/claude-code/issues/45550): `@unittest.skip` in spite of a rule against it.
  - **Bypassed guardrails.** [claude-code #40117](https://github.com/anthropics/claude-code/issues/40117): `--no-verify` (2r / 7c). [claude-code #32376](https://github.com/anthropics/claude-code/issues/32376): Claude "rewrote `done-gate.js`" (4r / 8c).
  - **False record of its own activity.** [claude-code #4462](https://github.com/anthropics/claude-code/issues/4462): "Sub-agents report successful file creation … no files were created" (31r / 39c). [claude-code #92505](https://github.com/anthropics/claude-code/issues/92505): "the only witness to whether a document was read is the model asserting that it was" (10c).
  - **Optimistic multi-agent reports.** [HN 47605971](https://news.ycombinator.com/item?id=47605971): "the only visibility you have is what they choose to report back … dangerously optimistic". [vibe-kanban #2783](https://github.com/BloopAI/vibe-kanban/issues/2783): marked completed while sub-agents still ran.
  - **Premature stops.** [claude-code #42796](https://github.com/anthropics/claude-code/issues/42796): "Claims completion against instructions … 'good stopping point'" (3286r).
- **Tools:** Claude Code, Codex, subagents, Vibe Kanban, Agent Orchestrator.
- **Wish:**
  - Checks the agent cannot edit or skip.
  - Status from the tool or event record, not from prose.
  - A rerun if anything changed after the last test run.
  - A "receipt" built from tool output ([r/LocalLLaMA 1r9be56](https://www.reddit.com/r/LocalLLaMA/comments/1r9be56/): "a hard receipt chain pattern", 35 / 65 c).

### 2.7 Token and quota burn, including coordination overhead

- **Pain:**
  - Parallel fleets and subagent fan-out drain weekly quotas in hours, with no warning.
  - In agent-to-agent setups, the lead spends turns acknowledging idle notifications.
  - Status polls resend the full history.
  - Nobody can see per-agent spend or how full each agent's context window is.
- **Tools:** Claude Code (agent teams, subagents), Codex (MultiAgentV2), Gas Town, cli-agent-orchestrator, Conductor, Superset.
- **Evidence:**
  - [claude-code #47930](https://github.com/anthropics/claude-code/issues/47930): "burns ~13–22% of input tokens on no-op acks" (8r / 7c).
  - [codex #38989](https://github.com/openai/codex/issues/38989): "74 unique subagents … 5,389,446,245 total recorded tokens … 53 / 74 subagents were review/audit/preflight-style" (2r / 4c).
  - [claude-code #66023](https://github.com/anthropics/claude-code/issues/66023): "46 subagents … output was ultimately discarded … no cost warning" (2r / 10c).
  - [codex #13733](https://github.com/openai/codex/issues/13733): polls cost "history size × poll count" (47r / 45c).
  - [r/AI_Agents 1tas0o5](https://reddit.com/r/AI_Agents/comments/1tas0o5/those_of_you_running_multiple_coding_agents_in/): "No idea which session was close to filling its context window until it compacted" (2 / 9 c).
  - [HN 50147911](https://news.ycombinator.com/item?id=50147911): agents talking over a message board "is questionable at best a waste of time at worst" (108 pts).
- **Wish:**
  - Per-agent spend and context share shown in the overview.
  - A warning before fan-out.
  - No hidden orchestration tokens, and no costly defaults changed silently ([HN 47886763](https://news.ycombinator.com/item?id=47886763)).
  - Deterministic coordination with no LLM in the loop ([superset #7168](https://github.com/superset-sh/superset/issues/7168), 15r: "a heavy, unpredictable mechanism for something as exact as …").

### 2.8 Permission fatigue versus YOLO

- **Pain:**
  - Default prompts produce a constant stream of mostly harmless approvals, which trains users to click through.
  - The only escape is full bypass inside a sandbox.
  - Harness quirks multiply the prompts: `cd` in compound commands, `$(...)` substitution, approvals that are not remembered.
- **Tools:** Claude Code, Codex.
- **Evidence:**
  - [claude-code #28240](https://github.com/anthropics/claude-code/issues/28240): "Permission prompt incorrectly triggers on cd instead of the actual command in compound bash statements" (210r / 49c).
  - [codex #10187](https://github.com/openai/codex/issues/10187): "approve the same permission prompt 25 times" (9r / 9c).
  - [HN 49239866](https://news.ycombinator.com/item?id=49239866): "tool-level permissions are really just a bandaid for bad sandboxing" (292 / 313).
  - [r/ClaudeAI 1su9jrt](https://www.reddit.com/r/ClaudeAI/comments/1su9jrt/): "If the approval layer is judging the final shell string, the wrapper is the thing to fix" (8 / 13 c).
  - [r/ClaudeAI 1s2ok85](https://www.reddit.com/r/ClaudeAI/comments/1s2ok85/): "Auto mode that actually explains why it blocked something" (comment 46, post 755).
- **Wish:** auto-allow what is harmless, prompt only on consequential actions, explain each block, and handle real shell shapes instead of retraining the agent.

### 2.9 Worktree environment and lifecycle

- **Pain:**
  - Worktrees isolate files but not the runtime: deps, `.env`, ports, databases and services.
  - Deletion freezes the UI.
  - Finished tasks leave tens of GB of worktrees and idle processes behind.
- **Tools:** herdr, claude-squad, Superset, Conductor, Vibe Kanban, Emdash, Claude Code Desktop.
- **Evidence:**
  - [HN 46424131](https://news.ycombinator.com/item?id=46424131): "I would need ten different copies of my database" (top comment, 96 pts thread).
  - [claude-squad #260](https://github.com/smtg-ai/claude-squad/issues/260): "two sessions both try port 3000" (8r).
  - [herdr D1428](https://github.com/herdrdev/herdr/discussions/1428): "wait 10 min to remove worktree blocks my engire herdr" (19 up).
  - [vibe-kanban #765](https://github.com/BloopAI/vibe-kanban/issues/765): garbage in tmp dirs, one user at about 26 GB (19r / 13c).
  - [r/ExperiencedDevs 1tepnyt](https://www.reddit.com/r/ExperiencedDevs/comments/1tepnyt/): "I was testing the wrong worktree".
- **Wish:** per-workspace setup and teardown hooks, automatic port allocation, background cleanup, and a clear mapping from session to tree.

### 2.10 Parallel work collides

- **Pain:**
  - Agents make incompatible design changes that git does not see.
  - AI merges drop code.
  - Concurrent sessions corrupt shared global config.
  - The human ends up as the integration point.
- **Evidence:**
  - [HN 49789356](https://news.ycombinator.com/item?id=49789356): "one agent replaces a class while another one is in the process of extending it" (Foremerge, 45 pts).
  - [r/ExperiencedDevs 1ten4yg](https://www.reddit.com/r/ExperiencedDevs/comments/1ten4yg/): "*you* become the merge conflict".
  - [claude-code #29217](https://github.com/anthropics/claude-code/issues/29217): "165 corrupted backup files" (11c).
  - [HN 48823677](https://news.ycombinator.com/item?id=48823677): "the ai can't handle it. Often it ends up dropping a lot of code".
- **Wish:** early warning when work overlaps, and help landing each change back on main.

### 2.11 Session restore and silent death

- **Pain:**
  - After a reboot, the layout returns but agents don't, or they resume without their flags.
  - One herdr user found sessions "silently downgraded" from skip-permissions.
  - Background agents die without notice, and their results are truncated or lost.
- **Evidence:**
  - [herdr D1080](https://github.com/herdrdev/herdr/discussions/1080): "came back as plain resumed `claude`, silently downgraded" (9 up / 10 c).
  - [herdr #4414](https://github.com/herdrdev/herdr/issues/4414): "restored 12 workspace layouts but resumed only three" (10c).
  - [claude-code #63023](https://github.com/anthropics/claude-code/issues/63023): "no completion notification ever arrives … uncommitted work … permanently lost" (5r / 10c).
  - [cli-agent-orchestrator #728](https://github.com/awslabs/cli-agent-orchestrator/issues/728): a result channel "silently truncates or drops a completed worker's reply".
- **Wish:** restore the whole workspace with its trust settings intact (fail closed), and tell finished apart from dead.

### 2.12 Interaction friction

- **Pain:**
  - Users must attach to the agent's terminal for every small reply.
  - Approval flows lock navigation.
  - One ESC kills every parallel agent.
  - CI or review fix-ups start a new session and lose the author's context.
- **Evidence:**
  - [claude-squad #312](https://github.com/smtg-ai/claude-squad/issues/312): "attach, type, ctrl-q, back to the list, for every small interaction".
  - [vibe-kanban #3247](https://github.com/BloopAI/vibe-kanban/issues/3247): "not possible to switch to another session until the current approval/question flow is completed".
  - [claude-code #21167](https://github.com/anthropics/claude-code/issues/21167): "One accidental ESC destroys all progress" (14r).
  - [claude-code #30492](https://github.com/anthropics/claude-code/issues/30492): "a non-destructive steering channel" (61r / 36c).
  - [superset #8721](https://github.com/superset-sh/superset/issues/8721): fixes "always start a **new** agent session".
- **Wish:** type into the selected agent while the list stays visible, open questions held as cards you can leave, per-agent stop, and failures routed back to the authoring session.

### 2.13 The supervisor itself costs fidelity, speed and trust

- **Terminal fidelity tax:**
  - [herdr #4251](https://github.com/herdrdev/herdr/issues/4251): "multi-line paste … submits at the first newline" (23c).
  - [HN 48837468](https://news.ycombinator.com/item?id=48837468): a constant redraw breaks selection.
  - [emdash #2538](https://github.com/generalaction/emdash/issues/2538): garbled embedded TUIs ("keeps resurfacing").
- **Weight:**
  - [herdr #4506](https://github.com/herdrdev/herdr/issues/4506): "10-13 CPU cores for hours" with about 20 idle panes.
  - [superset #6822](https://github.com/superset-sh/superset/issues/6822): renderer memory up to 9.9 GB.
- **Intrusion:**
  - [emdash #1944](https://github.com/generalaction/emdash/issues/1944): hooks injected into project files.
  - [claude-code #23615](https://github.com/anthropics/claude-code/issues/23615): a user's tmux layout destroyed (24r).
- **Trust in the tool itself:**
  - [vibe-kanban #2687](https://github.com/BloopAI/vibe-kanban/issues/2687): forced login (33r / 39c).
  - Vibe Kanban later [shut down](https://github.com/BloopAI/vibe-kanban/issues/3408).
  - [claude-squad #214](https://github.com/smtg-ai/claude-squad/issues/214): the project appears abandoned (16r).
  - [HN 50314167](https://news.ycombinator.com/item?id=50314167): updating feels like "Russian roulette … the trust issue of vibe coded apps".
- **Value doubt:** [HN 48823176](https://news.ycombinator.com/item?id=48823176): "I still don't fully get the additional value over tmux, beside notification regarding the agent status?"

### 2.14 Scope requests outside one local repo

- **Multi-repo:** [claude-squad #56](https://github.com/smtg-ai/claude-squad/issues/56), [#89](https://github.com/smtg-ai/claude-squad/issues/89).
- **Remote and multi-machine:** [herdr D515](https://github.com/herdrdev/herdr/discussions/515): "you still have to remember which space lives on which server" (104 up / 37 c, herdr's top discussion).
- **Mobile approval:** [r/ClaudeAI 1rdyhk4](https://www.reddit.com/r/ClaudeAI/comments/1rdyhk4/): Remote Control has "no apparent way to stop the agent" (post 1428).
- **Per-client views:** [herdr D651](https://github.com/herdrdev/herdr/discussions/651) (37 up).

## 3. What Öge already answers

**Status key:**

| Status | Meaning |
|---|---|
| **Yes** | Answered on `main` today. |
| **Planned** | A Desk slice targets it; not built. |
| **Partial** | Part of the pain is answered, and the rest is stated in the row. |

| Pain (section) | Öge mechanism | Status |
|---|---|---|
| Wrong state from screen scraping (2.2) | State comes from Öge's own event streams (Claude stream-json, Codex app-server, agy stream-json), not pixels. In desk.md: `host_request`, a Gate or AskUserQuestion means blocked; `turn_accepted`→`turn_settled` means working. herdr #4573 (AskUserQuestion read as idle) cannot happen here. | **Yes**, for agents Öge drives. Shells, servers and watchers stay unknown; desk.md says Öge "is not a terminal". |
| Blocked agent missed (2.1) | Today the live screen keeps an attention line ("Nothing needs you" / "Attention needed: …", ADR-0022). #336 adds `▲` on the rail, the header count, a toast at the next breakpoint and a bell or OSC notification when a Gate is open longer than T (5 min). ADR-0008: a Gate never resolves itself, so Öge never "continued without an answer". | **Yes** for one Chat; **Planned** for several (#335, #336). Öge has no push notification off the machine. |
| Notifications noisy or steal input (2.3) | #336's level table: tool steps and agent text are ignored outside the focused Chat, and an Accepted cut gives one card, not one per turn. U3 holds everything except a stale Gate while you type, and releases it on send or clear, not on a typing pause. | **Planned.** There is no per-Chat quiet, and the stale-Gate bell is the only push. |
| "Done" consumed by a glance (2.3) | `✓` stays until the card is seen. The Inbox orders cards ▲ then ✗ then ✓. | **Partial, planned.** desk.md defines no dwell time and no "mark unread", so an accidental focus could still consume a ✓. |
| Notification doesn't say where (2.3) | Each card names its Chat, its agent, how long it has waited, what is wanted and the Evidence. `o` opens the Chat. | **Planned** (#336). It works only inside the Desk; an OS notification click does not focus anything. |
| Losing track and re-entry (2.4) | #335: a stable rail with fixed numbers, short titles and state with age. #334: draft, scroll position and live area kept per Chat. #336: a since-you-left card in time order, ending at the open ask. Soft cap of 3 working Chats. | **Planned.** This is the Desk's main bet. Its value has to be measured; see desk.md, Evaluation. |
| Review bottleneck (2.5) | Checks run Oracle commands Öge owns. The Receipt says what was checked and what was not covered. Every card carries the Evidence, and there is no bulk apply. Öge, not the human, re-runs the work through verification and repair. | **Partial.** Öge shrinks "is it correct?" to "is the Oracle right?", but the human still judges intent and reads diffs. No last-turn diff or comparison of attempts. |
| False "done" claims (2.6) | Agents never issue Verdicts; a Verdict rests only on command-execution Evidence (ADR-0011). Each Check re-runs on the Candidate, so a stale or narrowed run can't count. Held-out tests plus Attestation (ADR-0020) answer skipped, narrowed or faked test runs. Check commands are protected, and only a human at a Gate adds one. Tamper events cover editing the gate (#32376, #40117). The Receipt shows the agent's claim next to Öge's finding when they disagree. | **Yes**, structurally. **Limit:** only as far as the Oracle reaches. #94168's "structural checks only" is answered only if the Oracle includes behavioural tests. |
| False record of activity (2.6) | Evidence is what Öge ran or observed (tool Claims, scope observations, the Briefing manifest), never the agent's narration. | **Yes** for Öge's record. Öge does not trace subagents inside the agent. |
| Agent-to-agent token burn (2.7) | No operator or routing agent and no agent-to-agent messaging (desk.md, Not doing). #333 replaces the automatic feedback turn and automatic Advisor with an `ask` card (U2). Usage, with cost, is already recorded per turn (`AttemptEnded`), and #333 shows turns and tokens per Accepted cut in the Receipt and `/runs`. | **Partial, planned.** Öge shows spend after the fact, with no quota view and no context-window percentage. The soft cap is the only brake on fan-out. |
| LLM in the loop for exact things (2.7) | The attention router is a fixed table, and Gates take typed choices. | **Yes** by design. |
| Permission fatigue (2.8) | A Claude Chat runs in auto mode under Öge's hard denies plus a sandbox (ADR-0013 amendment), and agy runs under `AutoPermissions` (ADR-0023). The remaining human decisions are Gates: no default choice, "Enter alone does nothing", and trust-weakening words typed in full (ADR-0015, ADR-0022). | **Partial.** The per-command stream goes away. **Caution:** Öge's own path check is a string matcher. It could reproduce the false stops users hate (#28240, 1su9jrt), and AGENTS.md says to "fix the interface, not the blacklist". |
| Restore silently weakens trust (2.11) | Trust settings live in the frozen Pipeline and the Ledger. Runs resume from durable state (ADR-0012), and a decision is replayed only while everything it was shown is unchanged. | **Partial.** Runs resume, but the Desk explicitly does no daemon or detach (#55), so a whole Desk is not restored after a reboot. |
| Silent death and lost results (2.11) | The Ledger is written ahead, failures are attributed, and the Receipt always ends a Run. | **Partial.** Covers Öge-driven Runs. An agent that hangs inside a turn has no stated timeout in desk.md. |
| Fix-ups lose the session (2.12) | The `send back` action (U2) returns the failed criterion to the same Chat and implementer Session. | **Planned** (#333, #336). |
| Navigation blocked by an approval (2.12) | Open asks are cards, and the Inbox and rail stay navigable. | **Planned** (#336). |
| One ESC kills every agent (2.12) | #334: Ctrl+C reaches only the focused Chat. Esc interrupts a turn without ending the Session, and messages sent during a turn queue. | **Planned** (#334). Mid-turn steering is still queue-until-boundary. |
| Same-repo collisions (2.10) | #337 adds a working-tree lock around `delivery.ApplyOnto`, sibling Chats refresh after an Öge apply, and a real conflict becomes a `✗` card. Agents work in disposable clones of an Öge-owned run repository (ADR-0010), never in the user's tree. | **Partial, planned.** Textual conflicts only; semantic overlap is not answered. |
| Worktree setup and intrusion (2.9, 2.13) | ADR-0010 clones and ADR-0021 warm Check caches handle setup and teardown without leaving trees in the user's repo. The private cache is a sandbox dir. | **Partial.** Files and deps are covered, ports, DBs and services are not. |
| Multiplexer fidelity tax (2.13) | Öge renders normalised events instead of re-hosting agent TUIs (ADR-0022: "The TUI is a view"). Paste, palette and OSC-52 bugs of a pty in the middle don't arise. | **Yes** by architecture. This is not a claim about Öge's own TUI quality. |
| herdr users who stay in herdr (2.2, 2.3) | #339 reports the Desk's rolled-up state to herdr with `pane report-agent --source oge` when `HERDR_ENV=1`. | **Planned, optional.** It gives herdr exact state for Öge panes. |
| Starting tasks from elsewhere (2.12) | #338 adds `oge add "task"`, which joins the open Desk or falls back to a standalone Run. | **Planned.** |

## 4. Gaps

Each candidate gets a check against "trust must earn its overhead": does it remove a real failure, and what latency or attention does it add?

| # | Pain | Candidate feature | Overhead check | Priority |
|---|---|---|---|---|
| G1 | Done consumed by a glance (D864, 51 up) | `✓` clears only after the card is shown for a dwell time (about 1 s) or acted on; add `u` (mark unread) on a card. | Removes a real failure, an unread result lost. No latency, one key, invisible on the happy path. | **After Desk** (fold into #336 if cheap) |
| G2 | Worker noise without losing approvals (D2529, openrig #896) | Per-Chat `quiet`: suppresses ✓ cards and toasts but never ▲ or the stale-Gate bell. | Removes noise that drowns the real signal; one toggle. Risk: a quieted ✓ is forgotten, so keep the rail glyph. | **After Desk** |
| G3 | Context-window blindness (r/AI_Agents 1tas0o5, openrig #1180) | Context share per Chat as a static number on the rail or card, plus a one-time card at a threshold. Only if the protocol reports it exactly; no estimates from transcript size. | Prevents compaction surprises; it is peripheral and does not animate. Adds an adapter field per agent. | **After Desk** |
| G4 | Quota drain (claude-code #87815, #16157) | Show the agent's reported rate-limit state on the Receipt and header when the protocol exposes it, and warn once before `/new` past the cap if a limit is near. | Removes the "92% to exhausted with no signal" failure. Öge has no automatic fan-out, so it does not cause the burn; value depends on what the protocols expose. | **After Desk**; anything beyond display is **not ours** |
| G5 | Semantic overlap between Chats (Foremerge, schipper.ai) | Deterministic card: two open Chats touch the same file or symbol. Uses scope observations Öge already records; no model. | Could prevent rework early, but file overlap may be noisy. Needs dogfood evidence before building. | **After Desk**, behind evidence |
| G6 | Desk lost on reboot (herdr #4414, HN 50157097) | On start, list Chats with unfinished Runs from the Ledger, resumed only when focused (lazy, as [parallel-code #293](https://github.com/johannesjo/parallel-code/issues/293) learned). Restored trust settings must match exactly or fail closed. | Removes a real failure. Must not start N agents at once (token spike). Overlaps #55, which is not doing a daemon. | **After Desk** |
| G7 | Ports, DBs and services per Chat (HN 46424131, claude-squad #260) | A Pipeline-level setup command run in the Workspace before Checks, as Oracle and not as agent prose. | Real failure (Checks that collide on :3000). Allocating ports and copying databases is environment management. | Setup hook **after Desk**; port or DB provisioning **not ours** |
| G8 | Last-turn diff and comparing attempts (emdash #1635, #2374) | The card shows `+N -M` since the last card for that Chat, and the full diff behind a key. | Cuts review reading; no latency. Comparing attempts is a race feature: **not ours** now. | Last-turn diff **after Desk** |
| G9 | Hung turn, no signal (vibe-kanban #2495, cursor forum) | If a turn has no events for T, the rail shows `?` (unknown) with its age, never a guessed `working`. | Honest state is the product's core claim. One timer per Chat, invisible when healthy. | **Now** (fits #335/#336) |
| G10 | Approval-layer false stops (claude-code #28240, r/ClaudeAI 1su9jrt) | Dogfood audit: count how often Öge's hard-deny path check blocks a legitimate compound command (`cd x && …`); fix the matcher, not the agent's style. | Removes a failure Öge could cause itself. Keeps "invisible trust layer" honest. | **Now** (audit only) |
| G11 | OS notification not actionable (D625) | The stale-Gate OSC notification carries the Chat number and title. | Tells you where; no new UI. Click-to-focus depends on the terminal, so don't promise it. | **After Desk** |
| G12 | Mid-turn steering (claude-code #30492, 61r) | Use the agent's native steer or interrupt when the protocol supports it; otherwise keep the queue. | Real desire, but it depends on the agent and blurs turn boundaries that Cuts rely on. | **Not now**; revisit per adapter |
| G13 | Multi-repo overview (claude-squad #56, #89) | One Desk across repos. | Today it is one Desk per repo (#338 socket). Real for some users, but it adds routing ambiguity. | **After Desk**, only on demand |
| G14 | Remote and multi-machine, mobile approval (herdr D515, r/ClaudeAI 1rdyhk4) | One sidebar over many servers, approve from a phone. | Needs a server, remote attach and auth. That makes Öge a terminal multiplexer or a service. | **Not ours** (#339 lets herdr carry it) |
| G15 | Hosting shells, servers, panes; per-client views | Pane hosting. | A terminal multiplexer. | **Not ours** |
| G16 | "Which agent should do this", LLM triage, PM agent | An operator or routing agent. | The token and waiting cost users report (2.7) is the reason it is excluded. | **Not ours** |
| G17 | Subagent trace (r/ClaudeAI 1uldqa5, claude-code #97819) | Show subagent tool steps, if the stream carries them, as Evidence behind a key. | Helps trust in a "done"; Checks already make it less needed. | **After Desk**, low |

## 5. Coverage and limits

**Searched:**
- **herdr** (`herdrdev/herdr`):
  - issues, ranked by reactions and comments;
  - all about 1,783 discussions, paged through GraphQL;
  - about 30 issues and 17 discussions read in full.
- **tmux runners:** claude-squad, dmux, openrig, cli-agent-orchestrator, coder/agentapi.
- **GUI runners:** vibe-kanban, emdash, superset/Paradev, sculptor, parallel-code, OrchestratorInc/agent-orchestrator.
- **First-party agents:**
  - `anthropics/claude-code`, with title-scoped searches for agent teams, subagents, parallel sessions, verification and permissions;
  - `openai/codex`;
  - `anthropics/claude-code-action`.
- **Hacker News** (Algolia): the herdr launch, native-app and YC threads; Conductor, Vibe Kanban, Sculptor, Emdash, Superset, Corgi and Codex app threads; the parallel-agent lifestyle threads; AI fatigue; verification debt; the AI code review bubble; Gas Town.
- **Practitioner blogs:** simonwillison.net, schipper.ai, siddhantkhare.com, blog.fsck.com, jacob.gold, flaviocopes.com/herdr.

**Thin or missing:**
- **Reddit was never read directly.**
  - WebFetch, curl and the API were refused or denied in every sweep.
  - Quotes come from `reddit.sentinel-team.org` snapshots (Jan–Aug 2026). They capture only part of each thread, so scores are as of capture and probably low.
  - URLs were rebuilt from post ids.
  - No herdr thread on Reddit was found.
  - r/ClaudeAI dominates; r/ClaudeCode, r/cursor and r/ChatGPTCoding turned up little.
- **X/Twitter, Discord and YouTube** were not searched.
- **Cursor background agents:** one forum thread was read; other Cursor results were search summaries and were not used.
- **Codex app:** few attention complaints were found. HN reaction to its "command center" was mostly positive.
- **Engagement gaps:**
  - The "which agent needs me" pain rests on many low-score Reddit posts (0–13 points); its strength is the number of independent tools built for it.
  - Several of the strongest verification reports have 0 reactions (#104504, #94168, #92505, #40117). They are kept for the data they measured, not for engagement.
  - claude-code issue search keeps surfacing the same mega-threads (#42796, #16157). Their reaction counts reflect general model frustration, not supervision specifically.
- **Platform coverage:** Windows-specific input bugs in herdr were folded into 2.13. Antigravity, Grok, pi and OMP appear only through herdr issues.

**Sources with a stake in the outcome:**
- **preuve.ai herdr review.** Its author sells a competing product, and it is the only source for the r/tmux quotes ("Total loss of control", "2-3 sessions"). Treat them as unverified; they are not counted in the ranking.
- **Show HN author comments** (Claude Control, Muster, Architect, CCBot, Corgi, Foremerge) and **Reddit tool-showcase posts.** Their authors describe the pain they built a product for. They count as evidence that a pain exists, not of how much it hurts.
- **Superset team member** ([HN 46425576](https://news.ycombinator.com/item?id=46425576)), quoted on "2-3 agents". This is a vendor statement, used as corroboration only.
- **Conductor evidence** is HN comments plus Conductor's own blog post, because the product is closed source.
- **Vendor blogs** (WorkOS, Permit.io, Greptile, Cognition and others) were excluded. So was the widely repeated "93% of prompts get approved" claim, which has no primary source.
- **Agent Orchestrator issues** are often written by its own agents, so their engagement is low and their wording is the tool's.

**Not re-verified:** reaction, comment and point counts, and the open or closed state of issues, are as the sweeps recorded them on 2026-11-01.