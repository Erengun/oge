---
status: accepted
---

# Check caches are private copies of a Run-level immutable warm seed

[#71](https://github.com/Erengun/oge/issues/71). The walking skeleton (#40) showed that a private cold Go cache per Check (ADR-0010) costs 5–20 s on a one-function package, and minutes on a real project. Standard mode runs several Checks per Run. "Trust must earn its overhead" (#66) applies.

A reader might expect a shared, writable per-Run or per-project cache, or the user's own `GOCACHE`. Both are rejected, because Check runs execute Candidate code, which could poison a cache that a later Check or the user then trusts.

## Decisions

```text
Snapshot → Öge setup → Run cache seed → private copy per Check → Check runs → Check-local cache discarded
```

- **Seed.** Öge's setup command and Öge's own offline warm step populate the seed, on the trusted Snapshot: Run-level seeds for the build cache and module cache.
  - The warm step compiles the Snapshot's packages and tests without linking or running them, with the module proxy off. It also vets them with go test's own analyzer set, so a Check doesn't vet the standard library again; a toolexec wrapper refuses every link, so no test binary exists to run ([#112](https://github.com/Erengun/oge/issues/112)). It runs niced, in the background, overlapping the implementer's Attempt. A Check waits for it only up to a bound, then stops it and starts from the partial seed.
  - Candidate code never runs with a seed directory as a writable cache. Once warm, the seed is read-only, so a stray write from an uncontained Check fails.
  - The seed lives in Öge's private state, outside Candidate access. A later Run sweeps the seeds and Check directories that a Run stopped without cleanup left behind.
- **Per-Check copy.** Before each Check, Öge materialises a private, Check-local warm cache from the seed.
  - It prefers a filesystem clone (reflink, copy-on-write) where supported, then a regular copy.
  - If neither is practical, it uses a cold Check cache.
  - **There is never a fallback to a writable cache shared across Checks, or to the user's own cache.**
- **Unchanged:** Check network stays off, and the Check-local cache is disposable, so it can't poison later Checks.
- **Evidence.** It records the strategy per Check, e.g. `cache: seeded-private-copy` or `cache: cold`.
- **Measurement.** Öge measures cold Check time, seeded Check time and materialisation overhead, and reports them in the evaluation (ADR-0019 metrics).
  - Report three numbers per fixture: cold Check, seeded Check, and seed creation plus materialisation. Report each saving as absolute seconds saved and as a percentage reduction.

## Consequences

- This amends ADR-0010's private-cache rule: private still holds, but the private cache may start warm from the seed.
- New ticket: the Run-level warm cache seed. It blocks `verify = "after"`.

## What would reverse this

- The seed doesn't materially improve real-project latency. Then simplify back to cold caches rather than carry complexity for theoretical speed.
