# Öge

**Your coding agent says it's done. Öge checks.**

Öge independently checks coding-agent work instead of letting the same agent grade its own homework.

> **Status: pre-alpha.** Nothing works yet except `oge --version`. Follow [milestone M1](https://github.com/Erengun/oge/milestone/1) for the first usable version.

## The idea

```sh
oge "fix the payment retry bug"
```

Your coding agent (Claude Code first) makes the change. A fresh agent session, which never sees the first agent's reasoning or claims, writes tests from your task. Öge then runs your checks and those tests itself. The result is accepted only on the evidence Öge collected, never on the agent's word that "all tests pass."

At the end of every run you get a Receipt: what was checked, what passed, and what was not covered. You take the result with `oge apply`.

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
