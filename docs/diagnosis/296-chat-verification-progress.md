# #296: Chat verification progress and the README wait

## Confirmed from the code

`Chat.Cut` waits on `c.prepared` before calling `Start`. Chat preparation
creates the cache seed and waits for the Snapshot control to finish. The
control runs the frozen Check commands on the Snapshot. `RunStarted` is
written by `Start`, so a cut waiting for preparation has no Run Ledger yet.

For a repository whose Check is `go test -json ./...`, preparation can
therefore include an entire repository test run. Previously the composer
showed only `verifying…`, indistinguishable from a stalled Check.

The composer now names preparation, QA and Checks, with elapsed time.
The composer stays usable while verification runs; cancellation and the
final result replace progress. Stage events contain no held-out sources
or QA narrative. They are transient UI events, not Verdicts.

## What the recorded session does not establish

The issue describes Chat `20261014T144946-3ccdc7`, a saved README change,
a baseline directory, and no linked `RunStarted`. That is consistent with
the preparation wait above. It does **not** establish which preparation
sub-phase consumed the time or prove the absence of a deadlock.

The maintainer's saved session is not available in this checkout. No new
paid agent runs were performed to manufacture a diagnosis. The specific
cause and duration of the recorded wait remain unproven. Inspecting its
existing baseline output blobs and timestamps would narrow that uncertainty.

Moving `RunStarted` ahead of baseline completion would change preparation
and control semantics; this visibility repair deliberately does not do so.

## Regression coverage

- Run API: preparation and Check stage events; QA precedes the Check.
- PTY: delayed baseline is named and can be cancelled; a delayed Candidate
  Check is named and ends in visible acceptance without requiring apply.
- PTY: another turn can complete while a Check holds; superseded progress
  cannot replace the newest cut's state.
- UI model: stale sequence events are ignored and step names exclude QA
  output and held-out file names.
