---
status: accepted
---

# Run state is an append-only ledger, content-addressed blobs and the Run repository, in a private per-user tree; no SQLite in the MVP

[#26](https://github.com/Erengun/oge/issues/26) settles what Öge persists, where it puts it and how that changes over time. It builds on ADR-0005 to ADR-0013. A reader might expect any of five things this design rejects:

- a SQLite database as the store;
- Run state in the repository's `.oge/` folder;
- Oracle versions kept in a second git repository;
- migrating old Runs to new formats in place;
- automatic cleanup of old Runs.

## Decisions

- **The store is plain files: no SQLite in the MVP.** Each Run has:
  - an append-only, hash-chained JSONL **Ledger** of control records, which is the source of truth;
  - a content-addressed blob store holding logs, reports, patches, redacted frames, Briefing manifests, the frozen config and compiled graph, and Oracle content;
  - the Run repository (ADR-0010), holding the Snapshot, Candidates and Checkpoints as commits.

  Readers rebuild Run state by replaying the Ledger. SQLite was rejected for three reasons:
  - The integrity manifest around every uncontained Check (ADR-0011) has to hash critical Run state. Append-only files hash cheaply; a live database file does not.
  - A hash chain naturally lives in an append-only log.
  - One writer per Run means no transactions across Runs are needed.

  SQLite may later become an index or cache rebuilt from the files, never the source of truth.
- **One per-user state root with two separate trees.**
  - The root is `$XDG_STATE_HOME/oge` (`~/Library/Application Support/oge` on macOS).
  - `private/` holds the Ledger, blobs, Run repository, Oracle manifests, locks and Check directories. Every Launch profile denies reads of it. Its mode is 0700 where the platform supports it, and it lies outside every Workspace an agent can see.
  - `work/` holds Workspaces and agent caches. They are disposable.

  The two trees stay separate even when both sit under a single configured root. The root can be overridden by environment or config. Öge canonicalises it and refuses any location that resolves:
  - inside the user's repository;
  - inside an Öge Workspace;
  - under the system temp directory;
  - through a symlink into any of these.

  `.oge/` in the user's repository holds project config only (ADR-0013). Öge writes nothing there.
- **Oracle versions are content-addressed manifests, not a git repository.** Each immutable version manifest references:
  - its command definitions;
  - its test and probe blobs;
  - its criterion mappings;
  - its parent version;
  - its additions;
  - explicit human removals, each with its Gate decision.

  A removal creates a new manifest that leaves the item out, and earlier versions stay intact. Diffs between versions come straight from the manifests. A Check copies the blobs of the selected version into its fresh Check directory. There are no refs, index or worktree, and Candidate history can't be confused with Oracle history.
- **How a Run is found.** The Run records the canonical path of its source repository and the Snapshot identity. A Run id works from anywhere, and commands run inside a repository show only the Runs whose source matches it. No pointer file is written into the repository.
- **Durability order.**
  - A control record is appended and fsynced before its effect counts as durable.
  - When a record refers to a blob, object or file, Öge first writes that item, fsyncs it, renames it into place atomically where that applies, and fsyncs the directory where needed. Only after that does it append and fsync the record.
  - High-volume normalised event streams and redacted frames are written per Attempt with buffering. They are flushed at Attempt and Checkpoint boundaries, and their hash and length go into the record that closes them.
  - When a Ledger is opened, a torn final record is truncated back to the last valid boundary, and that recovery is recorded. Corruption or a broken chain before the tail is an Infrastructure stop. Historical records are never repaired silently.
  - The chain detects corruption and keeps audits consistent. It does not protect against hostile code running with the same OS privileges (ADR-0011).
- **Formats are versioned and never migrated in place.**
  - Every record carries a format version.
  - A binary refuses formats newer than it supports, and reads older ones through a versioned decoder.
  - Historical Ledgers are never rewritten.
  - Resume compatibility may be narrower than compatibility for reading, reporting and delivery. A Run that can be read but no longer resumed still supports status, diff and delivery wherever its stored artifacts allow.
  - From the first public release, Öge aims to keep readers for every released format. Pre-release development formats are not promised forever.
- **Retention is manual.**
  - Workspaces and Check directories are removed at the end of their Attempt or Check. Nothing else is deleted automatically.
  - `oge gc` lists what it will remove first and deletes whole Runs only.
  - By default it selects finished Runs older than 30 days.
  - It never touches an unfinished Run or one whose lock is held.
  - It keeps an undelivered Accepted or Overridden Run unless given `--include-undelivered`, so every **Delivery** gets a durable Ledger record.
  - It also removes orphaned `work/` folders.
- **Never stored:**
  - credential values or credential-store settings;
  - account identity (email, organisation);
  - vendor transcripts or native session files;
  - unredacted output;
  - environment variable values (names only);
  - the binary content of reverted files.

  The subscription tier the native agent reports is non-secret metadata and may be stored, as ADR-0006 already allows. Vendor Session ids are stored because resume needs them. They are not credentials, but they stay private metadata: kept only in private state, left out of ordinary reports, and shown only where diagnostics need them. `answered_by` and `escalated_by` record a role or actor category, such as human or decider, and never a personal identity. Absolute local paths are acceptable for the local, single-user MVP.

## Consequences

- Provisional implementation defaults, which can change without an ADR:
  - per-Run blobs under the Run's private directory, so `gc` is a directory removal;
  - SHA-256 hashes over uncompressed content, with compression off;
  - a time-sortable Run id;
  - the Run lock at `private/runs/<id>/run.lock`;
  - the cross-Run Codex start-up lock (ADR-0012) under `private/locks/`;
  - the integrity manifest covering everything in the Run's private directory except its lock and the Check directory in use;
  - a Run header holding the format version, Öge version, Run id, creation time, source repository path, Snapshot identity, and references to the Task and frozen config/graph blobs;
  - a derived summary file for fast listing;
  - read-only commands reading the Ledger up to the last valid record without taking the lock.
- `oge apply` and `oge branch` append a Delivery record. `oge diff` is inspection only.
- #28 renders `oge gc` (dry-run first, `--include-undelivered`), state-root refusals, read-only Runs that can't be resumed, and Session ids only in diagnostics. #32 builds on these boundaries. #31 reads its counts by replaying Ledgers.

## What would reverse this

- Listing or #31 aggregates over many Runs become slow. Then add a rebuildable SQLite index, still never authoritative.
- A vendor sandbox can't deny reads of `private/` while allowing `work/` under one root. Then use separate roots.
- High-volume streams make disk use painful. Then prune streams per Run, but never the Ledger, Candidates or Oracle.
- Crash diagnosis needs event streams from the middle of an Attempt. Then fsync them periodically.
- Long-parked Runs routinely need to survive upgrades. Then promise resume across formats for released versions.
