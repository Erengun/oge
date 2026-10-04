---
status: accepted
---

# Third-party code is reference-only by default

Öge is a clean Go repo (`Erengun/oge`, module `github.com/erengun/oge`). We did not fork `coder/agentapi`: it is archived, its core scrapes terminal screens, and about 0 of its ~5k LOC would survive ([#2](https://github.com/Erengun/oge/issues/2), [#16](https://github.com/Erengun/oge/issues/16)). Öge still learns from other people's code: the MIT Claude Agent SDK (Python) for the stream-json control protocol, the Apache-2.0 Codex app-server schema, the ACP spec, and `coder/acp-go-sdk`. We chose the following policy over two alternatives. We rejected "translate freely with attribution", because it would make provenance the normal case. We rejected "never translate", because it forbids the rare case where a translation is the right call.

- **Default: reference only.** Protocol code is written by us from documented behaviour, published schemas and black-box observation of the real binaries. Reading someone else's code to learn a protocol's shape creates no obligation. Close translation is never the normal protocol-development strategy.
- **Exception: close translation, case by case.** It is allowed only when there is a concrete implementation reason. The translated section must have isolated, documented provenance:
  - preserve the upstream copyright and licence notice;
  - name the source file and revision;
  - record it in `THIRD_PARTY_NOTICES`;
  - keep the translated section clearly attributable, not mixed into our own code.
- **Copied artefacts keep their licence.** A schema snapshot copied into the repo, such as the Codex app-server schema used for the CI drift check, carries its upstream licence and gets a `THIRD_PARTY_NOTICES` entry.
- **Never a source:** the Claude Agent SDK for TypeScript (all rights reserved), or anything else without an OSI licence.
- **Ownership:** the copyright line is "The Öge Authors". Contributions use DCO sign-off (`Signed-off-by`). There is no CLA.

## Consequences

- Patterns taken from AgentAPI are ideas, not code: fake scripted agents for quota-free e2e tests, and the never-auto-allow-permissions lesson. Its shutdown order (SIGTERM, close pipes, SIGKILL after a grace period) is a reference pattern, not a protocol invariant.
- The agent tooling copied in from `mattpocock/skills` (MIT) needs its notice in `THIRD_PARTY_NOTICES` like any other third-party code.

## What would reverse this

1. Anthropic publishes a formal stream-json/control-protocol spec. The main reason to translate SDK code then goes away, and the exception could tighten to "never".
2. Öge must relicense or move to a foundation that requires 100%-owned code. Then remove all translated sections.
3. Reference-only reimplementation of a protocol turns out to be repeatedly wrong where a translated upstream implementation would have been right.
