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

- **Seed.** Setup runs on the trusted Snapshot and populates Run-level seeds for the build cache and module cache.
  - Candidate code never runs with a seed directory as a writable cache.
  - The seed lives in Öge's private state, outside Candidate access.
- **Per-Check copy.** Before each Check, Öge materialises a private, Check-local warm cache from the seed.
  - It prefers a filesystem clone (reflink, copy-on-write) where supported, then a regular copy.
  - If neither is practical, it uses a cold Check cache.
  - **There is never a fallback to a writable cache shared across Checks, or to the user's own cache.**
- **Unchanged:** Check network stays off, and the Check-local cache is disposable, so it can't poison later Checks.
- **Evidence.** It records the strategy per Check, e.g. `cache: seeded-private-copy` or `cache: cold`.
- **Measurement.** Öge measures cold Check time, seeded Check time and materialisation overhead, and reports them in the evaluation (ADR-0019 metrics).

## Consequences

- This amends ADR-0010's private-cache rule: private still holds, but the private cache may start warm from the seed.
- New ticket: the Run-level warm cache seed. It blocks `verify = "after"`.

## What would reverse this

- The seed doesn't materially improve real-project latency. Then simplify back to cold caches rather than carry complexity for theoretical speed.
