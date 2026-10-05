#!/bin/sh
# Fails if this CI job could reach a live coding agent or provider (ADR-0017).
# Only checks whether variables are set; never prints their values.
set -eu

fail=0
for var in OGE_LIVE_AGENTS ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN OPENAI_API_KEY CODEX_API_KEY GEMINI_API_KEY GOOGLE_API_KEY; do
	if eval "[ -n \"\${$var+x}\" ]"; then
		echo "ci-no-live-agents: $var is set; CI must not use live agents or provider credentials" >&2
		fail=1
	fi
done
exit "$fail"
