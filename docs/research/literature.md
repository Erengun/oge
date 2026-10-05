# Self-verification, test bias and reward hacking in coding agents

> Moved from branch `research/literature` at commit [`62ba7a54fe28`](https://github.com/Erengun/oge/blob/62ba7a54fe284bf76a5f967d51dc59e29516ae85/research/literature.md). Content unchanged.

Research for [Erengun/oge#10](https://github.com/Erengun/oge/issues/10). Compiled 2026-10-04.

**Question.** What do papers, lab reports and benchmark authors say about (1) coding agents grading their own work, (2) tests written to match the implementation instead of the requirement, (3) reward hacking and test tampering, (4) whether fresh sessions, restricted context, different models or adversarial roles measurably improve verification, and (5) how to measure verifier independence? What does that mean for Öge's ContextPolicy, verifier write scope, evidence model and challenge role?

**How sure each claim is.** Every claim is tagged:

- **[T]**: I read the relevant section of the primary text myself (PDF text or HTML full text).
- **[A]**: from the paper's arXiv abstract or HTML page, read through a fetch-and-summarise tool. Numbers were cross-checked where two fetches disagreed. Venues are listed only where the arXiv or publisher page states them.
- **[G]**: vendor guidance or author opinion, not a measurement.

"Measured" means the source reports an experiment, with its sample size where known. "Opinion" means the source argues for something without testing it.

---

## 1. Self-verification: can a model grade its own work?

**Without outside feedback, self-correction mostly fails. With reliable outside feedback (tests, execution), it works.**

- Huang et al., *Large Language Models Cannot Self-Correct Reasoning Yet*, arXiv [2310.01798](https://arxiv.org/abs/2310.01798) (Oct 2023, ICLR 2024). Measured: "LLMs struggle to self-correct their responses without external feedback, and at times, their performance even degrades after self-correction." This covers reasoning tasks, not agentic coding. **[A]**
- Kamoi et al., *When Can LLMs Actually Correct Their Own Mistakes?*, TACL 2024, arXiv [2406.01297](https://arxiv.org/abs/2406.01297). A survey: "self-correction works well in tasks that can use reliable external feedback". Prompted self-feedback alone is not enough, and much earlier work used "impractical frameworks or unfair evaluations". **[A]**
- Olausson et al., *Is Self-Repair a Silver Bullet for Code Generation?*, ICLR 2024, arXiv [2306.09896](https://arxiv.org/abs/2306.09896). Measured on HumanEval and APPS with Code Llama, GPT-3.5 and GPT-4. Once compute cost is counted, self-repair gains are "often modest … and are sometimes not present at all". The bottleneck is the quality of the feedback. Feedback from a stronger model, or from a human, gave "substantially larger" gains. **[A]**

**Models prefer their own output.**

- Panickssery, Bowman, Feng, *LLM Evaluators Recognize and Favor Their Own Generations*, arXiv [2404.13076](https://arxiv.org/abs/2404.13076) (2024-04-15). Measured with GPT-4 and Llama 2. Models can recognise their own generations out of the box. The paper finds "a linear correlation between self-recognition capability and the strength of self-preference bias", and fine-tuning experiments suggest the link is causal. **[A]**
- Chae et al., *Self- and Other-Labels Induce Bidirectional Bias in LLM Judges*, arXiv [2608.18091](https://arxiv.org/abs/2608.18091) (2026-06-06, rev. 2026-09-06). Measured with 10 LLMs. A "self" or "other" label alone moves scores in opposite directions: judges "inflate scores for self-labeled selections and deflate those for other-labeled ones". Blind evaluation removed the self-preference. **[A]**
- Zhao, Esmaeili, Fard, *Bias in the Loop: Auditing LLM-as-a-Judge for Software Engineering*, arXiv [2604.16790](https://arxiv.org/abs/2604.16790) (2026-04-18). Measured on code generation, repair and test generation. Judge verdicts change with prompt wording and presentation even when the code is identical. The biases are large enough to "change task-level conclusions and alter relative model rankings". **[A]**
- Alexopoulos et al., *Measuring and Exploiting Contextual Bias in LLM-Assisted Security Code Review*, arXiv [2603.18740](https://arxiv.org/abs/2603.18740) (2026-03-19, rev. 2026-09-23). Measured on 6 LLMs (exploratory) and 33 CVEs across 20 projects, against the Claude Code and CodeRabbit review pipelines. Framing in PR metadata (title, description, commit message) biases the reviewer. LLM-refined framing attacks got 32 of 33 vulnerabilities (97%) past review. Template attacks did not work. **[A]**

**Agents also misreport their own results**, which matters because an agent's final summary is its self-verification.

- [Claude 4 system card](https://www-cdn.anthropic.com/6d8a8055020700718b0c49369f60816ba2a7c285/Claude%204%20System%20Card.pdf) (Anthropic, May 2025, revised). In transcript 6.3.A, Claude Sonnet 3.7 special-cases all four test inputs and then reports "all tests pass", describing the special cases as "special cases for the test examples to ensure exact matching". **[T]**
- [GPT-5 system card](https://cdn.openai.com/gpt-5-system-card.pdf) (OpenAI, 2025-08-13), Table 9. On agentic coding tasks with an unresolvable impediment (11 impediment types), the coding deception rate was **0.17 for gpt-5-thinking and 0.47 for o3**. **[T]**
- GPT-5.5 system card (OpenAI, 2026-04-23), [deploymentsafety.openai.com/gpt-5-5](https://deploymentsafety.openai.com/gpt-5-5). In Apollo Research's external evaluation, GPT-5.5 "lied about completing an impossible programming task in 29% of samples", against 7% for GPT-5.4 and 10% for GPT-5.3 Codex. Caveat: in a related condition, 52% of samples explicitly named the setup as an evaluation. **[A]** So newer models do not reliably misreport less.
- Guo et al., *Are Your Agents Upward Deceivers?*, arXiv [2512.04864](https://arxiv.org/abs/2512.04864) (2025-12-04). Measured on 200 tasks and 11 LLMs. Agents under constraints hide failure by guessing, simulating results, substituting sources and fabricating files. Prompt-based mitigation had "only limited effectiveness". **[A]**

**Verifiers that accept wrong code put a ceiling on any generate-and-check loop.**

- Stroebl, Kapoor, Narayanan, arXiv [2411.17501](https://arxiv.org/abs/2411.17501) (2024-11-26, rev. 2026-03-26). Measured on HumanEval and MBPP. If unit tests accept wrong solutions, resampling cannot push accuracy past a fixed ceiling, whatever the compute budget. The optimal number of attempts is "often fewer than 10". **[A]**

## 2. Test-generation bias: tests that match the code, not the requirement

This is the best-measured sub-question. Several independent groups find that tests written by an LLM that has seen the code inherit the code's bugs.

- Huang et al., *AgentCoder*, arXiv [2312.13010](https://arxiv.org/abs/2312.13010) (Dec 2023, rev. May 2024). Section 4.7, RQ6, asks whether the programmer and the test designer should be separate agents (GPT-4, HumanEval and MBPP). With one agent doing both, test accuracy was 61.0% and 51.8%, coverage 72.5% and 75.9%, and pass@1 71.3% and 79.4%. With separate agents: test accuracy 87.8% and 89.9%, coverage 87.5% and 89.5%, pass@1 79.9% and 89.9%. The authors' explanation, which is opinion: tests "designed by the same agent that generates the code can be biased by the code and lose objectivity." **[A]**
- Konstantinou, Degiovanni, Papadakis, arXiv [2410.21136](https://arxiv.org/abs/2410.21136) (2024-10-28). Measured on 24 Java repositories. LLM test generators tend to write oracles that capture "the actual program behaviour rather than the expected one". Meaningful test and variable names improve the oracles. **[A]**
- Konstantinou, Tambon, Papadakis, *On the risk of coding before testing*, arXiv [2607.05139](https://arxiv.org/abs/2607.05139) (2026-07-06). Measured: tests generated after faulty code detect faults **14%** of the time, against **25%** for tests generated independently. The tests become "mutually consistent" with the buggy implementation. **[A]**
- Zhao, Zhou, Cohen, *Misguidance Effect of Buggy Code in LLM-Generated Unit Tests*, ISSTA 2026, arXiv [2607.22883](https://arxiv.org/abs/2607.22883). Measured: buggy code in the prompt raises the number of wrong assertions and lowers the number of bug-revealing tests. The mitigation is to give the test writer an LLM-generated specification docstring instead of the code. That cut misguided tests and raised bug-finding tests, for both buggy and correct inputs. **[A]**
- Li, Yu, Yuan, *DCAware / the "Self-Repair Trap"*, ASE 2026, arXiv [2608.05917](https://arxiv.org/abs/2608.05917). Measured with mutation testing. Repairing generated tests round after round until they execute "progressively drives models toward assertions that are easier to satisfy but less effective at detecting faults". **[A]** This is the test-side version of reward hacking: if "the test runs green" is the target, assertions get weaker.
- Ma, Eisty, arXiv [2607.10277](https://arxiv.org/abs/2607.10277) (2026-07-11). A preliminary study on 10 Defects4J bugs and 5 open models. Oracles generated from business requirements alone matched the requirement-derived oracle better than the system under test, with large variance between bugs and between models. **[A]**

## 3. Reward hacking and test tampering by coding agents

**It happens in deployed agents and frontier models, and is common when the task is impossible or ambiguous.**

- **METR**, *Recent frontier models are reward hacking*, [blog](https://metr.org/blog/2025-06-05-recent-reward-hacking/) (2025-06-05; o3, Claude 3.7 Sonnet, o1). Measured: reward hacking in **30.4% of RE-Bench runs (39/128)** against **0.7% of HCAST runs (8/1,087)**. METR's explanation is that RE-Bench shows the model the full scoring code. Observed exploits: monkey-patching the evaluator, rewriting timers, reading the reference answer off the call stack, and overloading `torch` equality. Telling the model not to reward hack had a "nearly negligible" effect (o3 still hacked 14 of 20 times). Caveat: detection had "a very high false-positive rate" and needed manual review, and some hacks may have been missed. **[A]**
- **Anthropic, Claude 4 system card** (May 2025; revised, and footnote 29 corrects earlier numbers), section 6. **[T]**
  - Definitions: hard-coding means "writing solutions that directly output expected values". Special-casing means "writing insufficiently general solutions" to pass tests. Sonnet 3.7 showed these "particularly in agentic coding settings such as Claude Code".
  - Table 6.2.A (Opus 4 / Sonnet 4 / Sonnet 3.7):
    - Hack-prone tasks, classifier: 9% / 4% / 44%
    - Same tasks, **hidden-test hack rate**: 13% / 12% / 23%
    - **Claude Code impossible tasks**, no prompt: 51% / 51% / 78%
    - Same tasks, **with anti-hack prompt**: 19% / 7% / 80%. Opus 4 is corrected from 5% to 19% in footnote 29.
  - The anti-hack prompt (quoted in section 6.3) asks for "a high quality, general purpose solution", tells the model to say if the task is infeasible or any test is incorrect, and to not hard-code test cases. It cut hacking for the Claude 4 models and did nothing for Sonnet 3.7.
- **Anthropic, [Claude Opus 5.5 system card](https://www-cdn.anthropic.com/fc1b44717c85dc068bc6ba5024219938094694bd/Claude%20Opus%205.5%20System%20Card.pdf)** (2026-09-22), sections 6.2.1–6.2.2. Training-time classifier data on environments shared with Opus 5 and Mythos 5.1. **[T]**
  - "Guesses what the answer key expects" succeeded in about 0.3% of sampled RL episodes for all three models. Transcript 6.2.1.B shows the model reasoning about what assertions the hidden tests are likely to make. The card calls this "particularly tricky to defend against".
  - Other common hacks: copying a finished solution, for example lifting an implementation out of an installed library, and using methods or access the task prohibits.
  - Once one sub-class is excluded, successful hack rates were 0.093%, 0.045% and 0.046% (Opus 5, Mythos 5.1, Opus 5.5).
  - **Impossible tasks (a file needed for the task was missing):** attempted reward hacking was **3–6× higher** than on intact tasks from the same environments, and successful hacks were 1.5–3 percentage points more common. "Knowingly incomplete work" accounts for about 80% of the attempts.
  - Caveat: one judge model (Opus 5) graded everything, and environments differ between models.
- **Zhong, Raghunathan, Carlini, *ImpossibleBench***, arXiv [2510.20270](https://arxiv.org/abs/2510.20270) (2025-10-23). Tasks from LiveCodeBench (103) and SWE-bench (349) are changed so the spec and the tests conflict, so any pass means the model cheated. **[A]**, with numbers checked against the HTML full text.
  - Cheating rate on Conflicting-SWEbench: GPT-5 54%, o3 49%, Claude Opus 4.1 50%. On One-off-SWEbench, GPT-5 reached 76%.
  - Four cheating strategies: test modification, operator overloading, recording state, and special-casing. Claude and Qwen relied mostly on **test modification (>79% of their cheats)**. OpenAI models spread across all four.
  - Prompt wording: on Conflicting-LiveCodeBench, "prompt A and B lead to a cheating rate >85%, while prompt D lowers them to 1% and 33%" (GPT-5 and o3).
  - **Test access:** "Hiding tests from agents reduces cheating success rate to near zero", but it also lowers legitimate performance. "Read-only access provides a middle ground: it restores legitimate performance while preventing test modification attempts."
  - **Abort option:** letting the agent flag the task as impossible cut cheating from 54% to 9% for GPT-5 and from 49% to 12% for o3. The effect was smaller for Claude Opus 4.1.
  - Allowing 10 submissions raised average cheating from 33% to 38%.
  - LLM monitors caught 86–89% of cheats on LiveCodeBench but only **42–65% on multi-file SWE-bench tasks**. Excuses such as "backward compatibility" got past them.
- **Gabor, Lynch, Rosenfeld, *EvilGenie***, arXiv [2511.21654](https://arxiv.org/abs/2511.21654) (2025-11-26, rev. 2026-05-17). 154 hard LiveCodeBench problems run inside real agent CLIs: Codex (GPT-5), Claude Code (Sonnet 4) and Gemini CLI (2.5 Pro). **[A]**
  - Unambiguous problems: Codex hard-coded in 0.7%. Claude Code hard-coded in 2.1% and used heuristic solutions in 20.7%. Gemini CLI **deleted test files in 3.4%**.
  - The 9 ambiguous problems, where the tests accept only one of several valid outputs, had far higher hard-coding rates: 44.4%, 33.3% and 22.2%.
  - A GPT-5 judge had 1 false positive and 0 false negatives on unambiguous problems. Held-out tests produced false positives and missed some hacks. Watching for test-file edits caught only 6 deletions.
  - The authors recommend combining detection methods and making test files read-only.
- **Zhao et al., *SpecBench***, arXiv [2605.21384](https://arxiv.org/abs/2605.21384) (2026-05-20, rev. 2026-09-09). Each task has a natural-language spec, visible tests and **held-out tests**. All frontier models saturate the visible tests, but the visible-to-held-out gap persists. It grows by about **28 percentage points for each tenfold increase in code size**, and is larger for smaller models. One example: a "2,900-line hash-table 'compiler' that memorizes test inputs". **[A]**
- **Wang et al., *BenchJack***, arXiv [2605.12673](https://arxiv.org/abs/2605.12673) (2026-05-12, UC Berkeley). An audit of 10 agent benchmarks. In 9 of the 10, near-perfect scores were reachable through exploits without solving any task. 219 flaws in 8 classes. The SWE-bench Verified exploit is a "nine-line PyTest hook" in `conftest.py`. The first flaw class, isolation failure, is when the agent and the evaluator share an environment. The authors describe this as "the trust boundary is nominal, not enforced" and recommend read-only mounts for test files, scoring scripts and evaluation binaries. **[A]**
- **Zhong et al., *Hacker-Fixer loops / Terminal Wrench***, arXiv [2606.08960](https://arxiv.org/abs/2606.08960) (2026-06-08). 323 of about 2,000 agent-benchmark tasks (16%) could be hacked by frontier models given only the task description. **[A]**
- **Wang et al., *The Verification Horizon***, arXiv [2606.26300](https://arxiv.org/abs/2606.26300) (2026-06-24). A position paper with some measurement. Its argument: "no fixed reward function can remain effective as policy capability continues to grow". It reports that stronger policies still retrieve solution artifacts and tamper with tests or the harness, even in hardened environments. **[A]/[G]**
- **MacDiarmid et al. (Anthropic)**, arXiv [2511.18397](https://arxiv.org/abs/2511.18397) (2025-11-23). In production coding RL environments, learning to reward hack generalised to alignment faking and attempted sabotage. The first mitigation listed is preventing the hacking at its source. **[A]** This matters for training more than for a harness, but it is a reason not to treat hacks as cosmetic.
- **Baker et al. (OpenAI)**, arXiv [2503.11926](https://arxiv.org/abs/2503.11926) (2025-03-14). Monitoring the chain of thought catches reward hacking in agentic coding better than watching actions alone. GPT-4o monitored the stronger o3-mini effectively. Optimising against the monitor teaches the agent to hide its intent. **[A]**

**Weak tests inflate claimed success, even in well-known benchmarks.**

- EvalPlus (Liu et al.), arXiv [2305.01210](https://arxiv.org/abs/2305.01210). With 80× more tests, pass@k on HumanEval dropped by 19.3–28.9%, and the model rankings changed. **[A]**
- PatchDiff (Wang, Pradel, Liu), arXiv [2503.15223](https://arxiv.org/abs/2503.15223), listed as ICSE 2026 on the authors' site. In SWE-bench, 29.6% of plausible patches behave differently from the gold patch. Of the divergent patches that were manually reviewed, 28.6% were certainly incorrect. Reported resolution rates are inflated by 6.2 percentage points. **[A]**
- UTBoost (Yu, Zhu, He, Kang), ACL 2025, arXiv [2506.09289](https://arxiv.org/abs/2506.09289). Found 36 instances with insufficient tests and 345 erroneous patches that had been counted as passing. 40.9% of SWE-Bench Lite and 24.4% of SWE-Bench Verified leaderboard entries changed rank. **[A]**

## 4. What measurably improves verification

| Intervention | Evidence | Strength |
|---|---|---|
| **Separate test author, no access to the implementation** | AgentCoder RQ6: test accuracy 61→88% (HumanEval). Konstantinou 2026: fault detection 14% vs 25%. Zhao ISSTA 2026: a spec docstring instead of code reduces misguided tests. | **Strong.** Three independent groups, all measured. All on function-level benchmarks or Java units, not repo-scale agents. |
| **Fresh session or context for the reviewer** | Song, *Cross-Context Review*, arXiv [2603.12123v2](https://arxiv.org/abs/2603.12123) (2026-10-01). F1: fresh session (CCR) 28.6%, same-session (SR) 27.1%, **context-inheriting subagent (SA) 23.8%**, repeated same-session (SR2) 21.7%. Only CCR beating SR2 holds across runs (p=0.004). CCR beating SR (p=0.26) and SA (p=0.057) does **not** hold. | **Weak.** One model (Claude Opus 4.6), 30 artifacts with 150 injected errors, a language confound (Korean vs English), and one run excluded. **[A]** |
| | Claude Code docs, [Best practices](https://code.claude.com/docs/en/best-practices) (fetched 2026-10-04): "A fresh context improves code review since Claude won't be biased toward code it just wrote." The adversarial reviewer "sees only the diff and the criteria you give it, not the reasoning that produced the change". It also warns that a reviewer told to find gaps "will usually report some, even when the work is sound". | **[G]** vendor guidance, no numbers. |
| **Restricting test access** (hidden or read-only tests) | ImpossibleBench: hidden tests bring cheating near zero at some cost to capability, read-only tests stop modification. EvilGenie and BenchJack both recommend read-only test files and scorers. | **Strong** for tampering. Read-only does not stop special-casing or operator overloading. |
| **Held-out tests the implementer never sees** | Claude 4 card "hidden test hack rate" (12–23%). SpecBench visible vs held-out gap. EvilGenie: useful but noisy. | **Strong** as a detector. Opus 5.5 card: capable models reason about what the hidden tests probably assert. |
| **Explicit permission to report the task infeasible** | ImpossibleBench: 54→9% (GPT-5). Claude 4 anti-hack prompt: 51→19% (Opus 4) and 51→7% (Sonnet 4). METR: "nearly negligible" for o3. | **Mixed.** Depends on the model. Helps, but cannot be the only control. |
| **Different model as verifier** | No direct measurement found of cross-vendor review on coding tasks. Indirect evidence: Goel et al., *Great Models Think Alike*, arXiv [2502.04313](https://arxiv.org/abs/2502.04313) (Feb 2025) introduce CAPA, a measure of error overlap, and find LLM judges favour models similar to themselves and that "as models become more capable, their mistakes become increasingly correlated". Self-preference (Panickssery) and label bias (Chae) also point this way. Baker: a weaker model can monitor a stronger one. | **Inferential.** Supports using a different model and hiding authorship. Does not show the size of the effect. |
| **Adversarial roles** | Hacker-Fixer (2606.08960): an attacker/patcher/validator loop cut attack success **62%→0%** on held-out exploits. A Gemini 3 Flash loop defended against Claude Opus (61%→0%). BenchJack: iterative patching cut hackability from ~100% to <10% within three cycles. PatchDiff: differential testing against a reference exposes divergent patches. | **Measured on hardening graders and benchmarks**, not on reviewing product code. The mechanism carries over. |
| **More rounds of repair or submission** | ImpossibleBench: 10 submissions raised cheating from 33% to 38%. DCAware: repeated self-repair weakens assertions. Stroebl: false positives cap resampling. | **Negative.** More loops against the same signal make things worse. |

## 5. Measuring verifier independence

- **Mutation score, not coverage.** MUTGEN (Wang, Xu, Briand, Liu), arXiv [2506.02954](https://arxiv.org/abs/2506.02954), IEEE TSE (rev. 2026-04-15). The paper points to test suites that reach "100% coverage but only 4% mutation score", and its mutation-guided LLM generation beats EvoSuite and plain LLM prompting across 204 subjects. **[A]** Google's experience, Petrović et al., *Does Mutation Testing Improve Testing Practices?*, ICSE 2021: from about 15M mutants, developers exposed to surviving mutants wrote more and better tests. **[A]**
- **LLM-generated mutants work in production.** Meta's ACH system, Foster et al., arXiv [2501.12862](https://arxiv.org/abs/2501.12862) (Jan 2025). Applied to 10,795 Kotlin classes, it produced 9,095 mutants and 571 tests, of which engineers accepted 73%. Its LLM equivalent-mutant detector had precision 0.79 and recall 0.47, rising to 0.95 and 0.96 with pre-processing. **[A]**
- **Seeded and impossible tasks.** ImpossibleBench, the Claude 4 Claude Code impossible-tasks evaluation, and the Opus 5.5 missing-file case study all build tasks where any pass proves the agent cheated. A conflict between spec and tests that only the harness knows about turns "did it cheat?" into a yes/no measurement.
- **Gap between visible and held-out tests** (SpecBench, Claude 4 hidden-test hack rate) measures overfitting to the tests the implementer saw.
- **Differential behaviour against a reference** (PatchDiff) measures plausible-but-wrong patches.
- **Misguidance metric** (Zhao, ISSTA 2026): how much seeing the buggy code changes what the generated tests assert. It works directly on seeded-bug fixtures.
- **Error correlation between models** (CAPA, Goel et al.): measures whether implementer and verifier fail together.
- **Tamper signals**: a diff of the test, config and harness files (`conftest.py`, CI config, snapshot files) between the start and end of the implementer's session. EvilGenie found file-edit detection alone catches little, because most hacks are in implementation code, but it is cheap and deterministic.

---

## Design implications for Öge

### ContextPolicy

1. **The verifier must start from a fresh context built by the harness, never a fork of the implementer's session.** The context-inheriting subagent (SA) scored lowest of the single-review conditions in Song 2026, below even same-session review. The evidence is weak, but it matches the vendor's own guidance and the self-preference results. *(CCR; Claude Code best practices [G]; Panickssery)*
2. **Do not pass the implementer's narrative to the verifier.** That means no transcript, no "what I did" summary and no PR-style description. A framing attack through PR metadata got 32 of 33 CVEs past Claude Code and CodeRabbit review, and self-reports are wrong often enough: Sonnet 3.7 claimed "all tests pass" after special-casing, and GPT-5.5 lied about an impossible task in 29% of samples. Give the verifier the planner's spec and acceptance criteria, the diff, and evidence the harness produced. *(Alexopoulos 2026; Claude 4 card [T]; GPT-5.5 card)*
3. **Hide authorship.** Do not tell a verifier that "you" or "model X" wrote the change. Labels alone shift scores. *(Chae 2026)*
4. **A test-writing verifier should see the spec, not the implementation, at least for the first pass.** This is the best-supported finding in this report: 14% vs 25% fault detection, 61% vs 88% test accuracy, and fewer misguided tests when a spec replaces the code. Öge's ContextPolicy needs a mode like "spec + public interface only, no bodies". *(AgentCoder; Konstantinou 2026; Zhao ISSTA 2026)*
5. **Spec quality is itself a hacking control.** Ambiguous problems produced 22–44% hard-coding against 0–2% on unambiguous ones. The planner's acceptance criteria, especially where they leave the expected output open, set an upper bound on how honest verification can be. *(EvilGenie)*

### Verifier write scope

6. **The implementer must not be able to change the oracle.** Test modification is >79% of Claude and Qwen cheats, Gemini CLI deleted test files, and BenchJack beat SWE-bench Verified with a 9-line `conftest.py`. The harness should run evidence from a harness-owned copy of the tests, test configuration and runner (`conftest.py`, `pytest.ini`, `jest.config`, `go test` flags, fixtures), not from files in the implementer's worktree. Telling the agent not to edit the tests is not enough: METR found it "nearly negligible" for o3. *(ImpossibleBench; EvilGenie; BenchJack; METR)*
7. **Make tests read-only for the implementer, and keep a held-out set it never sees.** Read-only tests keep capability and stop modification. Hidden tests catch special-casing and operator overloading, which read-only tests cannot. Expect capable models to guess hidden assertions (Opus 5.5 card), so held-out tests are a detector, not a guarantee. *(ImpossibleBench; Claude 4 card; SpecBench; Opus 5.5 card [T])*
8. **The verifier may write new tests and probes in its own area but cannot change implementation code.** If a verifier could patch the implementation it would be grading its own work. Verifier-written tests become evidence only after the harness runs them, and a verifier that writes and then reruns its tests until they go green is in DCAware's "self-repair trap". *(DCAware; Stroebl)*

### Evidence model

9. **Deterministic evidence comes first. LLM verdicts are secondary signals.** LLM monitors caught only 42–65% of cheats on multi-file SWE-bench tasks, and judge verdicts move with prompt wording. For every claim, the evidence record should say which tests ran, from which hash of which file, with what exit code. *(ImpossibleBench; Zhao/Esmaeili/Fard 2026)*
10. **Record oracle integrity as evidence.** Store a hash of the test, config and harness files at the start and end of each role's session, and an explicit "oracle files touched: yes/no". It is cheap and deterministic, and it catches deletion and modification, even though most hacks are elsewhere. *(EvilGenie; BenchJack)*
11. **Keep "visible tests pass" and "held-out tests pass" apart** in the evidence model. The gap between them is the measured definition of overfitting to tests. *(SpecBench; Claude 4 card)*
12. **Make "cannot complete / spec conflicts with tests" a first-class result, not a failure the agent hides.** Offering this option cut GPT-5 cheating from 54% to 9%. Impossible tasks raise attempted hacks 3–6×, and about 80% of those attempts are knowingly incomplete work. Öge's outcome types should include an infeasibility verdict with evidence, and the planner should treat it as useful information, not penalise it. *(ImpossibleBench; Opus 5.5 card [T]; GPT-5 card [T])*
13. **Do not reward retries against the same signal.** More submissions raised cheating, and resampling against weak tests hits a ceiling. Retry budgets should be small and visible in the evidence record. *(ImpossibleBench; Stroebl)*

### Challenge role

14. **Two shapes are supported by measurements.**
    - (a) **Exploit the oracle.** A challenger tries to make the checks pass without meeting the spec, for example with a stub or special-casing, and the harness records whether it worked. This is the Hacker-Fixer and BenchJack loop, which took attack success from 62% to 0%.
    - (b) **Differential and mutation probing.** Run the change against a reference or seeded mutants, and report divergences and surviving mutants.
    A weaker model in the loop could defend against a stronger attacker, so the challenger does not have to be the most expensive model. *(Hacker-Fixer 2606.08960; BenchJack; PatchDiff; MUTGEN)*
15. **Limit the challenger's tendency to find something.** The vendor's own guidance warns that adversarial reviewers report gaps "even when the work is sound". Findings should be filed as claims that need deterministic reproduction, such as a failing test or a surviving mutant, before they block anything. *(Claude Code best practices [G])*

### Measuring independence

16. **Use the fixture repos with seeded bugs and spec/test conflicts that only the harness knows about.** That gives a direct yes/no measure of whether the verifier caught the bug and whether the implementer cheated. Track: seeded-bug detection rate, mutation score of verifier-written tests compared with implementer-written tests, misguidance (how verifier tests change when the verifier sees the code compared with only the spec), and the visible vs held-out gap. *(ImpossibleBench; Zhao ISSTA 2026; Meta ACH; SpecBench)*
17. **When choosing a different model for verification, measure error overlap on the fixtures**, for example with CAPA, instead of assuming a different vendor means independence. *(Goel et al. 2025)*

---

### Differentiation (what upstream already ships)

The Claude Code [best-practices page](https://code.claude.com/docs/en/best-practices) (fetched 2026-10-04) already offers much of this pattern natively: a "verification subagent" that "has a fresh model try to refute the result", a `/goal` condition re-checked by "a separate evaluator", a Stop hook as "a deterministic gate", `/code-review` in a fresh subagent, and the advice to "show evidence rather than asserting success". **[G]** So "a fresh reviewer" alone does not set Öge apart. The structural difference these findings support is who owns the loop. In Claude Code, the implementing session spawns the reviewer and "receives the gaps directly and can fix them and re-review", and the tests it runs live in the implementer's own worktree. In Öge, the harness owns the verifier's context, a copy of the oracle the implementer cannot touch, held-out tests, and the evidence record. Sections 3–4 above are the evidence for why that ownership matters: tampering, test modification, and weak LLM monitors on multi-file tasks.

## Open questions

- **How much does a different model actually help?** I found no measurement of a cross-vendor verifier (for example Codex reviewing Claude Code's diff, or the reverse) against a same-model fresh-session verifier on repository-scale coding tasks. The case is indirect (CAPA, self-preference, label bias). Öge's fixture repos could measure it.
- **The fresh-session evidence is thin.** One study, one model, 30 artifacts, and its advantage over same-session review does not hold across runs. Treat fresh context as a cheap, low-risk default, not a proven large win.
- **How far can a spec-only test writer go at repository scale?** The independent-test results come from function-level benchmarks and Java units. A real repository needs the public interface, fixtures and conventions. Exactly how much code the verifier may see is still a design call. Candidates: signatures only, signatures plus docstrings, or the existing test suite but not the diff.
- **Do held-out tests stay useful against models that guess them?** The Opus 5.5 card shows models inferring hidden assertions. How quickly that wears down a fixture's held-out suite is unknown.
- **The upstream agents' own "write tests" behaviour.** Codex and Claude Code both run and edit tests by default. Whether their native sandbox or permission settings can make particular paths read-only, or whether Öge must enforce it with its own worktree or copy, belongs to the context-isolation and workspace-isolation tickets.
- **Chain-of-thought monitoring.** Baker et al. show CoT monitoring catches hacks, but Öge sees only what each CLI exposes, and optimising against a monitor causes the model to hide its intent. Whether Öge should look at agent reasoning at all is open.
