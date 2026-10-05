### Öge Receipt: ✓ Accepted

Run 20261005T090000-a1b2c3 · Standard mode

| | |
|---|---|
| **Task** | fix Add |
| **Agent claimed** | _"Done. All tests pass." (Claim, implement#1)_ |
| **Öge found** | Check #1 failed on its Candidate 6d1231d: 1 held-out test failing (AC-2) |
| | → sent back to the implementer; the Candidate it then made was Accepted |
| **Candidate** | 5a1bd80 · 1 file changed · Oracle v1 |
| **Checks** | Check #2 passed · 2 of 2 Oracle tests attested passing · 1 held out from the implementer · 2 Checks in all |
| **QA** | +1 held-out · 2 QA passes |
| **Protected** | no test changes kept |
| **Scope** | no out-of-scope changes |
| **Handled** | 18 operations approved automatically · 1 sent back to the implementer · Human interruptions: 0 |
| **Time** | 28.1s · preflight 400ms · implement 10.4s · QA 8.8s · Check 7.7s · your attention 0s |
| **Ledger** | head f1fc93266303 · identifies this Receipt; not tamper-proof |

**Not covered**

- Checks ran unsandboxed; network not blocked; no isolation against deliberately hostile Candidate code running with your privileges

<details><summary>Full evidence</summary>

- Check #1 on `6d1231d` · Oracle v1 · fail · cache seeded-clone
  - command · go test -json ./... · fail (exit 1) · 3.8s
  - not attested passing: fx.TestAddNegatives (held-out) (AC-2)
- Check #2 on `5a1bd80` · Oracle v1 · pass · cache seeded-clone
  - command · go test -json ./... · pass · 3.9s
- Files changed: add.go
- Ledger head `f1fc932663037836fbfe25fccae70e400e2ba6f34350f7e362cb04a9f06f01b4` (identifies this Receipt; not tamper-proof)

</details>
