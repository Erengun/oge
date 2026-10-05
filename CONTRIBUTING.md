# Contributing to Öge

Thanks for helping. A few rules keep the project's licensing and trust model intact.

## Sign off your commits (DCO)

Öge uses the [Developer Certificate of Origin](https://developercertificate.org/). There is no CLA. Every commit in a pull request must carry a `Signed-off-by` line matching its author:

```sh
git commit -s
```

CI checks this on every pull request.

## Licensing

- Contributions are licensed under Apache-2.0. The copyright line is "The Öge Authors".
- Third-party code is used for reference only and is not copied in. See [ADR-0002](docs/adr/0002-reference-only-third-party-code.md).

## Build invariants

- **The product binary is cgo-free.** `CGO_ENABLED=0 go build ./...` must keep working. The only cgo use is the `-race` test job.
- **CI spends zero model quota.** Tests use the fake agent or recorded sessions. Live-agent tests only run when enabled locally with `OGE_LIVE_AGENTS=1`, and CI fails if that variable is set. See [ADR-0017](docs/adr/0017-testing-posture-fakes-first-zero-quota-ci-windows-refused.md).

## Before you push

```sh
gofmt -l .
go vet ./...
go test ./...
CGO_ENABLED=0 go build ./...
```

## Where decisions live

- Product rule: [AGENTS.md](AGENTS.md). Trust must earn its overhead.
- Architecture decisions: [`docs/adr/`](docs/adr/).
- Vocabulary: [`GLOSSARY.md`](GLOSSARY.md).
- Wording: [`docs/positioning.md`](docs/positioning.md).
