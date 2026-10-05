# Öge

**Öge makes delegation actually feel like delegation.**

Öge is the supervision layer for coding agents. AI made coding fast, and supervision became the bottleneck. Give Öge the task: it runs your coding agent, checks the result itself, and interrupts you only for decisions that actually need you.

> **Status: pre-alpha.** Nothing works yet except `oge --version`. Follow [milestone M1](https://github.com/Erengun/oge/milestone/1) for the first usable version.

## The idea

```sh
oge "fix the payment retry bug"
```

Your coding agent (Claude Code first) makes the change. A fresh agent session, which never sees the first agent's reasoning or claims, writes tests from your task. Öge then runs your checks and those tests itself. The result is accepted only on the evidence Öge collected, never on the agent's word that "all tests pass."

On a terminal, a run shows its stages live, with the agent's recent steps under the one that's running. This is a finished run in Fast mode today, on the scripted test agent:

```text
fix Add
Run 20261005T090000-a1b2c3 · Fast mode · HEAD 84ca1bd (main) + 1 untracked

✓ preflight  ok · 0.4s
✓ implement  fake · Exit done · Candidate 6d1231d · 1 file changed · 5.7s
✓ check      go test -json ./... · 1 ran · 0 failed · pass · 1.2s

ÖGE RECEIPT   Run 20261005T090000-a1b2c3 · Fast mode
Result        ✓ Accepted
Task          fix Add
Candidate     6d1231d · 1 file changed · Oracle v0
Checks        Check #1 passed · 1 of 1 Oracle tests attested passing
Protected     no test changes kept
Scope         no out-of-scope changes
Handled       0 operations approved automatically · Human interruptions: 0
Time          7.4s · preflight 400ms · implement 5.6s · Check 1.2s · your attention 0s
Not covered   No independent tests (fast mode): no QA and no held-out tests
              Checks ran unsandboxed; network not blocked; no isolation against deliberately hostile Candidate code running with your privileges
Ledger        head 3406de47c802 · identifies this Receipt; not tamper-proof
Nothing was written to your repository.
```

In CI, or with `--plain`, the same run prints plain lines.

At the end of every run you get a Receipt: what was checked, what passed, and what was not covered. `oge receipt --md` prints it again as Markdown for a PR, and `oge receipt --json` as JSON. You take the result with `oge apply`.

Öge uses the coding-agent CLIs you already have installed and logged in. It never asks for, reads or stores your provider credentials.

## Build from source

Requires Go 1.27.

```sh
CGO_ENABLED=0 go build -o oge ./cmd/oge
./oge --version
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Design decisions are recorded in [`docs/adr/`](docs/adr/).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
