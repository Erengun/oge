#!/usr/bin/env bash
# Screenshots of the bare-oge home on fakeagy, for working on the TUI:
# TestTUIShots writes each scenario's screen as .ansi and .txt (and each
# session's output stream as .raw), and freeze draws each .ansi as a PNG.
# Usage: scripts/tui-shots.sh [dir]   (default .tui-shots)
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p "${1:-.tui-shots}"
dir=$(cd "${1:-.tui-shots}" && pwd)
rm -f "${dir:?}"/*.ansi "${dir:?}"/*.txt "${dir:?}"/*.raw "${dir:?}"/*.png
OGE_TUI_SHOTS=$dir go test -timeout 10m -count=1 -run '^TestTUIShots$' ./internal/cli/
# freeze reads a piped stdin in place of the file: give it none.
for f in "$dir"/*.ansi; do
	go run github.com/charmbracelet/freeze@v0.2.2 -l ansi --window --no-font.ligatures \
		-o "${f%.ansi}.png" "$f" </dev/null >/dev/null
done
ls "$dir"/*.png
