# Go codegen vs the Codex app-server schema

> Moved from branch `research/go-codegen` at commit [`013035c593cb`](https://github.com/Erengun/oge/blob/013035c593cbd503b4bb63e45beb8c1232e520a0/research/go-codegen.md). Changes: in-repo link updated.

Ticket: #33 (parent map #1, feeds #15). Follows up row 8 of [`lang-ecosystem.md`](lang-ecosystem.md) (#11), which gave Rust a "material" protocol-layer edge because `typify` compiled the Codex bundle and Go's main generator documents no `oneOf`/`anyOf`/`allOf` support.

**Answer:** No Go generator produces usable types for the Codex schema. go-jsonschema can't generate the bundle until all 54 `oneOf` defs are erased. Per message file, it generates and compiles only with `--only-models` (293/310), and every union comes out as `interface{}`. quicktype compiles but flattens each union into one struct of optional fields. Hand-written Go for the MVP subset is small and bounded, though: about 100 schema definitions in the closure, of which Öge needs to inspect perhaps 6–8 unions. The Rust edge survives, but it shrinks from "material" to a one-time cost of about 1–2 days plus a small diff per Codex release.

## Setup and versions

All runs were local in a throwaway scratch dir. No model calls, no Codex sessions.

| Thing | Version |
|---|---|
| Go | go1.27.1 darwin/arm64 (Homebrew) |
| Codex CLI | codex-cli 0.155.1. `codex app-server generate-json-schema --out schema` writes 39 top-level files, `v1/` (InitializeParams/Response), 273 `v2/` files, and two bundles: `codex_app_server_protocol.schemas.json` (697 KB, 83 defs + nested `v2`) and `codex_app_server_protocol.v2.schemas.json` (597 KB, 638 defs). Neither bundle has a root type, only `definitions`. |
| go-jsonschema | `github.com/atombender/go-jsonschema` **v0.24.1** (`go install …@latest`; this is the module path of the omissis-maintained repo, so "omissis" and "atombender" are the same tool) |
| quicktype | **26.0.0** via `npx -y quicktype` |
| ACP schema | `agentclientprotocol/agent-client-protocol` @ `f0a08fbdb2` (2026-10-04), `schema/v1/schema.json` (247 KB, draft 2020-12, 170 `$defs`, 186 `allOf`, 71 `anyOf`, 15 `oneOf`, 5 `discriminator`) |

## Generator 1: go-jsonschema v0.24.1

### Whole v2 bundle (what typify was given): fails

```
go-jsonschema: Warning: Multiple types map to the name "TurnError"; declaring duplicate as "TurnError_1" instead
go-jsonschema: Failed: cannot add struct field: could not generate type with scope 'ReviewStartResponseTurn':
  cannot add struct field: could not generate type with scope 'TurnError': cannot add struct field:
  could not generate type with scope 'TurnErrorCodexErrorInfo': could not merge anyOf types: types list is empty
```

The failure shows up at `Turn.error`. That field is `anyOf: [{$ref: TurnError}, {type: null}]`, and `TurnError.codexErrorInfo` is itself `anyOf: [{$ref: CodexErrorInfo}, {type: null}]`, where `CodexErrorInfo` is a serde externally-tagged enum (`oneOf` of a string enum plus `{"httpConnectionFailed": {...}}`-style objects). It takes the nesting to fail: `ErrorNotification` references `TurnError` directly (not nullable), and that file generates and compiles, with `CodexErrorInfo interface{}`. `--only-models` fails the same way.

How much has to be erased before the bundle generates:

| Patch | Result |
|---|---|
| `CodexErrorInfo := {}` | same error (anyOf of `{}` + null is still "empty") |
| `CodexErrorInfo := {type: object}` | next failure: `ThreadForkParamsApprovalPolicy` (`AskForApproval`, same shape) |
| all 16 `oneOf` defs with a non-object variant → `{type: object}` | next failure: `TurnStartParamsSandboxPolicy`. `SandboxPolicy` is a clean `type`-tagged object union, and it fails too once wrapped in nullable `anyOf` |
| **all 54 `oneOf` defs → `{type: object}`** | generates. `--only-models`: 10,351 lines, `go build` + `go vet` clean. Full mode (with validators): 24,484 lines, **11 compile errors** (see below) |

So the only bundle output that compiles is a single package in which every union is an opaque object.

### Per-message files: mostly generates, often doesn't compile

The per-message schema files were run separately, each into its own package (they share types like `ThreadItem`, so they can't share a package).

- Full mode, 308 files (top-level + `v2/`; the 2 `v1/` files weren't in this run): **291 of 308 generate.** All 17 failures are the same `could not merge anyOf types: types list is empty`, and every one of those files contains a `Turn` (nullable `Turn.error` → nullable `CodexErrorInfo`): `ThreadStartResponse`, `ThreadResumeResponse`, `TurnStartResponse`, `TurnStartedNotification`, `TurnCompletedNotification`, `ThreadStartedNotification`, `ClientRequest`, `ServerNotification`, etc. **Almost every MVP response and turn notification is in this list.**
- Of the 291 that generate, **33 packages don't compile.** These include the MVP-critical `ThreadStartParams`, `ThreadResumeParams`, `TurnStartParams` and `ServerRequest`:

  ```
  TurnStartParams/gen.go:111:29: method ApprovalsReviewer.UnmarshalJSON already declared at TurnStartParams/gen.go:91:29
  TurnStartParams/gen.go:359:23: method Personality.UnmarshalJSON already declared at TurnStartParams/gen.go:339:23
  ServerRequest/gen.go:1025:38: method McpElicitationStringFormat.UnmarshalJSON already declared at ServerRequest/gen.go:1005:38
  ```

  Cause: a string enum reached through the nullable wrapper `anyOf: [{$ref: Enum}, {type: null}]` (serde `Option<Enum>`, the most common pattern in the schema) gets two `UnmarshalJSON` methods. The full-mode generator also imports `github.com/go-viper/mapstructure/v2` even with `--tags json`.
- `--only-models` (no validators), 310 files including `v1/`: **293 of 310 generate (same 17 failures), and all 293 build and vet clean.**

### What the output looks like

- **Every union is `interface{}`.** `type ThreadItem interface{}`, `type UserInput interface{}`, `type PatchChangeKind interface{}`, `type SandboxPolicy interface{}`, and 58 distinct `interface{}` union types across the outputs. `FileUpdateChange.Kind` is `PatchChangeKind interface{}`.
- **Every nullable `$ref` field is `interface{}` too.** In `TurnStartParams`, `Personality`, `ApprovalPolicy`, `SandboxPolicy` and `Effort` are all `interface{}`, even though `Personality` is a plain three-value string enum that was generated as a named type.
- **Root `oneOf` message unions produce nothing.** `ServerRequest.json` (10 methods) generates the param types but no `ServerRequest` type and no method dispatch.

### Strict vs tolerant (measured, full mode)

| Input | Result |
|---|---|
| enum `NonSteerableTurnKind`, value `"review"` | ok |
| same enum, value `"somethingNew"` | `invalid value (expected one of []interface {}{"review", "compact"}): "somethingNew"` |
| `ErrorNotification` with an unknown extra field | ok (generated code never calls `DisallowUnknownFields`; from a grep, `additionalProperties:false` objects look equally lax, but no decode of one was tested) |
| `ErrorNotification` missing required `willRetry` | `field willRetry in ErrorNotificationJson: required` |

Unknown fields are tolerated, but **a new enum value fails decoding of the whole message.** That is the kind of drift the Codex research saw. `--only-models` has no `UnmarshalJSON`, so it tolerates everything.

## Generator 2: quicktype 26.0.0

`--src-lang schema --lang go --top-level <Name>` on 7 MVP per-message files (`ServerNotification`, `ServerRequest`, `ClientRequest`, `ItemCompletedNotification`, `TurnStartParams`, `ThreadStartParams`, `FileChangeRequestApprovalParams`): **all generate in about 1–2 s and all `go build`/`go vet` clean.** quicktype turns unions into **one flattened struct holding every variant's fields as optional**:

- `ThreadItem` becomes one struct with **54 fields** plus `Type ThreadItemType`. Fields that collide across variants are merged loosely (`Status *string` covers four different status enums; `Arguments interface{}`). Nothing ties a field to its variant.
- In `ServerNotification`, `Params` is one `Notification` struct with **93 fields**, and quicktype gives up on the nested item: `Item interface{} 'json:"item"'` ("quicktype cannot infer this type because there is no data about it in the input"). Per-notification files keep `Item ThreadItem`.
- `PatchChangeKind` comes out as `{Type, MovePath}`, which is usable.
- Externally-tagged and primitive-or-object unions get small "union structs" (`Result{MCPToolCallResult *…; String *string}`) with a custom `UnmarshalJSON`.

Measured tolerance: an `item/completed` with `type:"somethingNew"`, `status:"weird"` and unknown fields decodes with `err=nil` (enums are plain string types with no validation). **quicktype is tolerant but weakly typed.** It is fine as a source of plain structs to copy from, but no better than hand-written code for variant safety.

## Generator 3 (cheap): ACP v1 schema

- go-jsonschema: `Failed: … scope 'AgentRequestParams': could not merge allOf types: types list is empty`, with a warning that `RequestPermissionRequest` failed on `anyOf`. `--only-models` fails the same way.
- quicktype `--top-level AcpMessage`: builds, 238 lines, but `Params interface{}` / `Result interface{}`, so it is useless as a protocol layer.
- Precedent: the community `coder/acp-go-sdk` (v0.13.5) did not use a stock generator. It ships its own **custom generator, `cmd/generate`** (load/merge/IR/emit with jennifer, ~113 KB of Go source), which emits a 9,494-line `types_gen.go`. That is the real-world Go answer to this exact problem.

## MVP subset: size of the hand-written layer

Transitive `$ref` closure (script over the full bundle) of: `initialize`, `thread/start|resume`, `turn/start|interrupt|steer` (params + responses), the notifications `error`, `thread/started`, `thread/status/changed`, `turn/started|completed|diff/updated`, `item/started|completed`, `item/agentMessage/delta`, `item/commandExecution/outputDelta`, `item/fileChange/outputDelta|patchUpdated`, and the approval requests `item/commandExecution/requestApproval`, `item/fileChange/requestApproval` with their responses. (The approval types and `InitializeParams` are in the non-v2 bundle / `v1/` only.)

| | count |
|---|---|
| definitions in closure | **101** |
| plain objects | 52 |
| string enums | 23 |
| `type`-tagged object unions (`oneOf`, tag `type`) | 10: `ThreadItem`(19 variants), `UserInput`(7), `CommandAction`(4), `SandboxPolicy`(4), `ThreadStatus`(4), `WebSearchAction`(4), `FunctionCallOutputContentItem`(4), `PatchChangeKind`(3), `DynamicToolCallOutputContentItem`(3), `ImageGenerationFailure`(1) |
| externally-tagged / string-or-object unions (serde default, no shared tag) | 9: `CommandExecutionApprovalDecision`(6), `FileChangeApprovalDecision`(4), `AskForApproval`(2), `CodexErrorInfo`(6), `SessionSource`(3), `SubAgentSource`(3), `TurnItemsView`(3), `MessagePhase`(2), `ReasoningSummary`(2) |
| other `anyOf` union | 1: `FunctionCallOutputBody` |
| nullable `anyOf[$ref,null]` fields | 45 |
| `additionalProperties:false` objects | 13 |

Öge doesn't need full fidelity on most of this. It acts on a few item variants (agentMessage, commandExecution, fileChange, maybe reasoning/plan), statuses, and diffs. Everything else can be `json.RawMessage` that is logged or passed through.

**Effort estimate:**

- **Hybrid (recommended for Go):** take plain structs and enums from `go-jsonschema --only-models` (compiles today) or quicktype as a starting point, hand-write tagged decoding for the ~6–8 unions Öge inspects (`ThreadItem`, `PatchChangeKind`, `ThreadStatus`, `UserInput` for sending, the two approval decisions for sending, and probably `SandboxPolicy`/`AskForApproval` for sending), keep enums as plain `string`, and add an `Unknown{Type, Raw}` arm everywhere. **About 1 day** including tests against recorded traffic, plus **under an hour per Codex release** to diff the schema for those unions (CI can run `generate-json-schema` and diff).
- **Full-fidelity hand-written MVP closure** (all 101 defs, all 20 unions typed): **about 2–3 days**, with more surface to keep in step with Codex.
- **Custom generator** in the coder/acp-go-sdk style (~150–300 lines for this schema's two union shapes: `type`-tagged and serde externally-tagged): **about 1–2 days**. Only worth it if Öge ends up needing much more of the protocol.

## Sketch: tagged-union decoding in Go (compiled and run)

The full file (111 lines, `go vet` clean) also has `AgentMessage`/`CommandExecution` and a `MarshalJSON` for the externally-tagged approval decision. Core pattern:

```go
type ThreadItem interface{ itemType() string }

type FileChange struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // plain string: new statuses must not break decoding
	Changes []struct {
		Path string `json:"path"`
		Diff string `json:"diff"`
		Kind struct {
			Type     string  `json:"type"` // add | delete | update
			MovePath *string `json:"move_path"`
		} `json:"kind"`
	} `json:"changes"`
}
type Unknown struct {
	Type string
	Raw  json.RawMessage // passed through untouched
}

func decodeThreadItem(raw json.RawMessage) (ThreadItem, error) {
	var tag struct{ Type string `json:"type"` }
	if err := json.Unmarshal(raw, &tag); err != nil {
		return nil, err
	}
	switch tag.Type {
	case "agentMessage":
		var v AgentMessage
		err := json.Unmarshal(raw, &v)
		return v, err
	case "commandExecution":
		var v CommandExecution
		err := json.Unmarshal(raw, &v)
		return v, err
	case "fileChange":
		var v FileChange
		err := json.Unmarshal(raw, &v)
		return v, err
	default:
		return Unknown{Type: tag.Type, Raw: raw}, nil
	}
}

// Sending side: serde externally tagged, "accept" | {"acceptWithExecpolicyAmendment":{...}}
type ApprovalDecision struct {
	Simple string
	Tagged map[string]any
}

func (d ApprovalDecision) MarshalJSON() ([]byte, error) {
	if d.Simple != "" {
		return json.Marshal(d.Simple)
	}
	return json.Marshal(d.Tagged)
}
```

Run output on a fileChange with an extra field, a commandExecution with an unknown status, and an unknown item type:

```
fileChange         err=<nil> {ID:i1 Status:inProgress Changes:[{Path:a.go Diff:@@ -1 +1 @@ Kind:{Type:update MovePath:<nil>}}]}
commandExecution   err=<nil> {ID:i2 Command:ls Cwd:/r Status:someFutureStatus ExitCode:<nil>}
somethingNew       err=<nil> {Type:somethingNew Raw:{"type":"somethingNew","id":"i3","extra":{"a":1}}}
"accept" {"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}}
```

Each extra variant costs about 6 lines. The pattern is tolerant by construction (no `DisallowUnknownFields`, string enums, `Unknown` arm), which is the behaviour Öge wants given the wire runs ahead of the schema. The compiler won't check that a `switch` over `ThreadItem` is exhaustive. `go vet` won't either; a linter such as `exhaustive` only helps with sealed-interface conventions.

## Verdict: does Rust's protocol-layer edge survive?

**Yes, but smaller.** Downgrade row 8 of the ecosystem table from "Rust (material)" to **"Rust (moderate)"**.

- For Rust: `typify` compiled the **unmodified** bundle first try (#11), and Rust enums model both union shapes (internally `type`-tagged and serde externally-tagged) with exhaustive `match`. No Go tool gets close. go-jsonschema fails on the bundle and on 17 per-message files including nearly every MVP turn/thread response, can't compile `TurnStartParams`/`ThreadStartParams`, and erases all unions. quicktype compiles but flattens unions into 54- and 93-field bags.
- For Go: the MVP layer is about 100 defs, of which only ~6–8 unions need real decoding. That is about 1 day of hand-written code, following a well-worn pattern (`coder/acp-go-sdk` did the same with a custom generator). Hand-written Go is also **tolerant by default**. Rust's `typify` output maps `additionalProperties:false` (13 objects in the MVP closure) to strict structs and enums to closed sets, so on the Rust side, tolerance of wire drift is extra work: settings, `#[serde(other)]`, or patching the schema. #11 already lists that as unverified.
- Net: the protocol layer alone no longer decides Go vs Rust. It is now a bounded cost (~1–2 days + per-release diffs) rather than a capability gap. The deciding factors move to #11's other rows (ACP SDK, Windows process-tree control, SQLite/build simplicity).

## Reproduce

Scripts are not in the repo. Steps: `codex app-server generate-json-schema --out schema`; `go install github.com/atombender/go-jsonschema@latest`; `go-jsonschema [--only-models] -p pkg --tags json -o gen.go <file>` per file and on the v2 bundle; `npx -y quicktype --src-lang schema --lang go --package pkg --top-level <Name> -o gen.go <file>`; for the bundle patch, replace every `definitions.*` that has `oneOf` with `{"type":"object"}`; then `go build ./... && go vet ./...`.
