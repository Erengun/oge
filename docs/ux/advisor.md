# The Advisor: a second opinion inside the Chat

Status: approved. The user took D-A1…D-A6 at the recommended options. Slice 1 is #287. Direction: #64 and `docs/positioning.md`. Placement: #29.

## Why

Öge was designed by hand: one model asked a question, the user copied it to another model, then copied the answer back. That loop works, but the human is the middleware. They notice the agent is stuck, pick what context to paste, wait, judge the reply and paste it back, every time. The Advisor is that loop with Öge as the bridge. Öge notices or is asked, builds the context, gets a second opinion from a fresh Session, and routes it to the implementer as one more turn. The trust model doesn't change: advice is a Claim, and only a Check decides.

## Model

A **Consultation** is one request for advice. It opens a **fresh, read-only advisor Session** in a disposable clone of the Chat's newest settled turn's commit (never a turn's in-flight edits), gives it an Öge-built Briefing, collects one answer and closes it.
- There is no Stage, no Run and no standing agent. Nothing is opened, pre-warmed or paid for until a Consultation starts.
- A Consultation lives in the Chat, between turns and cuts, like a turn does. It is recorded in the Chat log.
- Advice reaches the implementer only as an **advice turn**: an Öge-authored turn in the live Session, like D5's feedback turn.

## What the user sees

**Asking (`/advise`).** The composer stays live, and the implementer keeps working if it's mid-turn.

```
› /advise it keeps mocking time.Now in every test; is there a cleaner seam?
  ⠋ advising · claude opus · fresh, read-only · 23s                 esc cancel
```

**The advice** prints as one block when it lands. If the implementer is streaming, the block waits until that turn settles, so the transcript stays in order.

```
  Advisor · claude opus · fresh · 41s · $0.18                   a Claim, not checked
  │ Inject a Clock at Scheduler construction instead of patching time.Now:
  │ 1. add `type Clock interface{ Now() time.Time }` in internal/sched;
  │ 2. NewScheduler takes it; production passes realClock{};
  │ 3. the three tests build a fakeClock, which removes the monkey-patching
  │    helper in sched_test.go:40.
  ↵ send to agent   e edit first   esc dismiss
```

**Send.** One key. The advice is sent verbatim as a dim Öge turn, queued behind a turn in flight (nothing enters a running turn):

```
  Öge → agent: An advisor suggests (a second opinion, not checked): Inject a Clock…
```

`e` opens the advice in the composer. What the user sends from there is **their** turn (D-A4). `esc` dismisses it. The Consultation stays in the Chat log, and nothing reaches the agent.

**Automatic: the feedback budget is spent.** Today the third failed send-back ends the chain and waits for the user. With the Advisor, Öge consults once instead (D-A3, D-A5):

```
  ✗ 2 criteria failing (AC-2, AC-4) · feedback turns spent (3/3)
  ⠋ advising · claude opus · fresh, read-only · 31s
  Advisor · claude opus · fresh · 52s · $0.24                   a Claim, not checked
  │ The failures are one bug: Parse trims before it splits, so …
  Öge → agent: An advisor suggests (a second opinion, not checked): The failures…
```

If that cut fails too, the chain stops as it does today: `✗ still failing after 3 tries; your call`. That's one automatic Consultation per chain.

**The agent asks.** An AskUserQuestion already reaches the composer as a question (a Host request). It gains one key:

```
  agent asks: Store sessions in Redis or keep the in-memory map?
    1 Redis   2 in-memory   a ask the advisor   esc deny
```

`a` runs a Consultation on that question and shows the advice under it, with its suggested option highlighted. **The human still answers.** The answer records who gave it (`By: human`) and the Consultation the human saw (`AdvisedBy`). Answering without the human is the Decider's job (#29 places it first), never the Advisor's.

**A stall** without a failing Check (a turn that loops, or a reply that only asks) isn't a trigger yet. It is fog until dogfooding gives a signal (D-A5).

**Latency.** A fresh `claude -p` with a Briefing and some repo reading takes tens of seconds, sometimes minutes. That's acceptable only because nothing waits on it: the composer is live, the implementer isn't paused, and Esc cancels. The happy path never pays for the Advisor. A Chat that never consults opens no extra process and adds no latency.

## Trust

**Advice is a Claim.** A Consultation can never:
- create Evidence, issue or influence a Verdict, choose an accepting edge or resolve a Gate;
- answer a Host request (the human answers, and later the Decider);
- write to the Workspace, the Run repository or the Oracle (its clone is discarded);
- enter the Task or QA's Briefing: QA never sees advice, as it never sees the agent's narrative;
- see held-out tests, QA's text or Öge's private state;
- run in every Run. It's event-driven, with a per-chain cap on automatic Consultations.

**What the Advisor gets: an Öge-built Briefing** (ADR-0009). Each item is a Briefing-manifest entry with its source and hash:
- the question: the user's text, the agent's AskUserQuestion verbatim, or Öge's templated trigger ("the Check still fails after 3 feedback turns");
- the Task: the user's turns since the last delivery, verbatim (D7);
- the diff of the newest turn against the Snapshot;
- the newest Verdict's failures through the same held-out filter a feedback turn uses: counts, criterion ids, visible output;
- the implementer's **last reply, verbatim, labelled agent text (a Claim)** (D-A2). Never the full transcript and never a summary of it;
- read access to a disposable clone of the newest turn's commit, so it can grep and read code.

**Held-out tests are absent by construction,** not by a permission rule. They live in Öge's private state, never in a Workspace or its clones, and the private state roots are denied (`DenyRead`, `StateRoots`), as for the implementer.

**Read-only** has no single switch in `LaunchSpec`, so it means three things:
- the Session runs in a disposable clone (ADR-0010) whose writes are discarded;
- write tools are denied where the adapter can prove it, and the record says which applies;
- **every Ask from the advisor Session is denied without surfacing.** A permission prompt from an advisor would make the trust layer visible.

This is not described as sandbox enforcement where it's only discard (ADR-0018's rule).

**Chat log records** (written ahead, like turns):
- `ConsultationStarted`: id, trigger (`user`, `feedback_budget`, `ask`), requested by (`user`, `oge`, `agent`), the last turn started, the question's hash, the Briefing-manifest ref, and the advisor identity: agent, model, effort, profile, and the auth source as `system/init` reports it;
- the Session's process record and residue sweep, as for any Attempt;
- `ConsultationEnded`: outcome (`advised`, `cancelled`, `failed`), the advice blob's hash, Usage and cost, and duration;
- `AdviceSent`: the Consultation id, the turn it became, and `verbatim` (an Öge turn) or `edited` (a user turn). An auto-sent one is `by=oge`.

**Receipt.** A cut's Run already records its Chat turn range. The Receipt gains one disclosure when advice reached the agent inside that range:

```
Advice: 2 consultations
        sent at turn 4 (claude opus, fresh, automatic) · a Claim, not checked
        on the agent's question at turn 4 (claude opus): shown to you · a Claim, not checked
```

", automatic" marks a `feedback_budget` Consultation. An ask Consultation (`a`+Enter on the agent's question) whose advice landed was shown to you, never sent to the agent, so the compact Receipt lists it as shown. A Consultation asked after a delivery belongs to the next cut.

Dismissed Consultations don't change the work, so they stay out of the compact Receipt; `/receipt` lists them.

**Invariants.**
- *Kept:* only a Check issues a Verdict; held-out tests reach neither the implementer nor the Advisor; QA starts fresh and sees neither advice nor narrative; the Task is the user's text.
- *Weakened, disclosed:*
  1. Agent text enters a Briefing: the Advisor's, never QA's. It is labelled a Claim in the manifest (ADR-0009 amendment).
  2. Öge turns can carry another model's text into the implementer Session, where they used to carry only Check feedback. Each is manifested with its Consultation id and disclosed on the Receipt.

## Which agent advises

| | Same vendor, fresh Session | Cross-vendor (Codex advising Claude) |
|---|---|---|
| Available | Now: the Chat's own adapter | After #60. ADR-0018 is one app-server per Run; a Chat-scoped one is #60's question |
| Independence | Fresh context, no shared transcript; the same model's blind spots | A different model. #10 found no direct evidence that it catches more |
| Terms of service | No new exposure: one more Öge-driven `claude -p` on the user's configured auth | Most exposure (#19, #30). Öge drives a second vendor's CLI programmatically. `docs/provider-terms.md` and the vendor enquiries (#19, 9d) are still pending |
| Cost and quota | Shares the implementer's quota | Spreads load. Needs a second login |
| Auth | Unchanged | Unchanged while each CLI uses its own auth. An advisor on a *different auth than its CLI's default* (say, API key) is per-role auth selection, which breaks #19's decision 1 and needs a new ADR (`ANTHROPIC_API_KEY` beats the subscription in `-p`) |

**User-invoked** cross-vendor consultation is a human keystroke, and Öge is the transport, like running the other CLI yourself. **Automatic** cross-vendor calls are where the exposure sits. Cost is shown in every advice header from Usage. A quota or auth failure ends the Consultation with one line (`advisor unavailable: codex quota`), never the Chat.

## Decisions for the user

| # | Decision | Options | Recommendation |
|---|---|---|---|
| D-A1 | **Who advises** by default | (a) the implementer's agent and model, fresh; (b) the same agent, its strongest model at high effort; (c) cross-vendor | **(b)**, configurable as `[advisor] agent / model / effort`. Cross-vendor is opt-in after #60. Automatic cross-vendor calls stay off until the vendor enquiries are answered |
| D-A2 | **The transcript** in the Advisor's Briefing | (a) none; (b) the implementer's last reply, verbatim, labelled a Claim; (c) the full transcript | **(b)**. The middleware loop is mostly "here's what it said". (c) would make the Advisor inherit the implementer's framing and costs tokens. Needs the ADR-0009 amendment |
| D-A3 | **Automatic advice** delivery | (a) auto-send as an advice turn; (b) show it and wait for ↵ | **(a)**, once per chain. "Use Öge when you're not watching": the human may be away, and the Check still decides. `[advisor] auto = "ask"` gives (b) |
| D-A4 | **Advice and the Task** | (a) verbatim send is an Öge turn, outside the Task; edited text is the user's turn; (b) advice always enters the Task; (c) never, even edited | **(a)**. QA verifies what the user asked, not what a model suggested. An edit is the user taking ownership. An advice turn neither counts against nor resets D5's feedback budget |
| D-A5 | **Automatic triggers** | (a) feedback budget spent only; (b) also early, when two consecutive cuts fail the same criterion ids; (c) also a timed stall | **(a)** now. (b) after dogfooding, since it saves a doomed send-back. (c) only with a measured stall signal |
| D-A6 | **The agent-initiated route** | (a) `a` on AskUserQuestion (the human answers); (b) an Öge-provided consult tool in the Launch profile, so the agent consults by itself; (c) both | **(a)**. (b) teaches the agent about Öge, which goes against the invisible-layer rule. Revisit it if dogfooding shows `a` pressed on most questions. #29 keeps the Launch profile open to it |

## Build slices

Slices 1 and 2 are the thin first slice: `/advise`, same vendor, fresh read-only Session, advice shown, one key to send.

1. **The Consultation core** (Opus).
   - `run.Chat.Consult(ctx, question)`: a fresh advisor Session from the Chat's adapter, in a disposable clone of the newest turn's commit.
   - The read-only profile, with asks denied silently.
   - The Advisor Briefing and its manifest (D-A2), and the Verdict summary through `heldOutFilter`.
   - `ConsultationStarted` and `ConsultationEnded`, and the advice blob.
   - Tests on the fake agent.
   - Lands the ADR-0009 amendment and the GLOSSARY entries.
   - Depends on nothing new: the Chat exists.
2. **`/advise` and send to agent** (Sonnet).
   - The `advising…` status line, Esc to cancel, the advice block, and ↵ / e / esc.
   - `Chat.SendAdvice`: an Öge turn (`by=oge`, with the Consultation id) and a new manifest source. It is outside D5's budget and the Task.
   - It is a third turn kind in `Chat.send`, next to the user's turn and the feedback turn. It neither checks nor increments `c.feedbacks`, because the automatic case (slice 4) fires exactly when that guard trips.
   - `AdviceSent`.
   - Lands the ADR-0022 note: advisor text is shown as a block, and QA text still never streams.
   - Depends on 1.
3. **The Receipt disclosure** (Sonnet).
   - The Receipt builder reads the Chat log's Consultations whose `AdviceSent` falls in the cut's turn range.
   - `/receipt` lists the dismissed ones.
   - Depends on 2.
4. **Auto-consult on a spent feedback budget** (Opus).
   - On `ErrFeedbackBudget`, consult once per chain and auto-send it (D-A3, D-A5a).
   - `[advisor] auto` (on, ask or off), with the cap recorded.
   - Depends on 2.
5. **The advisor key on AskUserQuestion** (Sonnet).
   - `a` consults on the question and highlights the suggested option.
   - The answer records `advised_by` next to `answered_by=human` (#29).
   - Depends on 2.
6. **The cross-vendor advisor** (Opus).
   - `[advisor] agent = "codex"`, user-invoked only.
   - Automatic use waits on the provider-terms enquiry.
   - Depends on #60 (including the Chat-scoped app-server topology).
7. **Fog, not tickets yet:** the early trigger (D-A5b), stall detection, and the Öge consult tool (D-A6b). Each waits on dogfooding data.

## Glossary and ADR impact

Slice 1 (#287) landed the ADR-0009 amendment and the *Advisor* and *Consultation* entries. The rest land with the slices they describe.

- **GLOSSARY:**
  - *Advisor*: add "fresh, read-only Session; event-driven".
  - *Consultation*: add "recorded in the Chat log" and the three triggers.
  - New *Advice turn*: an Öge turn carrying a Consultation's advice; never Task text.
  - *Host request*: an answer may record `advised_by`.
- **ADR-0009:** the Advisor Role kind's Briefing, and agent text as a manifest item labelled a Claim.
- **ADR-0010:** the advisor's disposable clone, with writes discarded.
- **ADR-0011:** Consultation records are Evidence that a Consultation happened. The advice inside them is a Claim.
- **ADR-0005:** the advisor Session's asks are denied without surfacing.
- **ADR-0022:** the advice block as a transcript surface.
- **A new ADR, only if needed:** per-role auth selection (an advisor on a different auth from its CLI's default), which reopens #19's decision 1.
- **docs/ux/chat-first.md:** the two weakened invariants above, once approved.
- **docs/positioning.md:** no change. Direction step 3 already describes this.

## Risks

1. **Advice that is confidently wrong** steers the implementer away from a passing path. The Check still decides, but the chain costs a cut. Measure whether advised chains end Accepted more often than unadvised ones before widening the triggers.
2. **Cost surprise.** A strong model at high effort, auto-triggered, on a large diff. Every header shows the cost, and the automatic cap is one per chain.
3. **The middleware returns.** If users routinely press `e` to fix the advice, the Briefing is missing something. Watch the edit rate.
