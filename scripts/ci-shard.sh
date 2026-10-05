#!/bin/sh
# Prints the go test flag that selects shard INDEX (1-based) of TOTAL of
# the tests in package directory DIR.
#
# Top-level test names are read from the _test.go sources (listing them
# with the test binary would run TestMain) and dealt round-robin in sorted
# order. Shards 1..TOTAL-1 print -run=^(their names)$. The last shard
# prints -skip=^(every other shard's names)$, so it also runs anything the
# source scan missed: together the shards always run every test. A scanned
# name that is no test (say, inside a fixture's source string) only
# matches nothing.
#
# Usage: go test "$(scripts/ci-shard.sh internal/cli 2 4)" ./internal/cli
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: ci-shard.sh DIR INDEX TOTAL" >&2
	exit 2
fi
dir=$1 index=$2 total=$3
if [ "$index" -lt 1 ] || [ "$index" -gt "$total" ]; then
	echo "ci-shard: shard $index is outside 1..$total" >&2
	exit 2
fi

all=$(cat "$dir"/*_test.go |
	sed -n 's/^func \(Test[A-Za-z0-9_]*\)([A-Za-z0-9_]* \*testing\.T).*/\1/p' |
	LC_ALL=C sort -u)

pattern() {
	printf '^(%s)$' "$(printf '%s\n' "$1" | paste -sd '|' -)"
}

if [ "$index" -lt "$total" ]; then
	names=$(printf '%s\n' "$all" | awk -v i="$index" -v n="$total" 'NF && (NR - 1) % n == i - 1')
	if [ -z "$names" ]; then
		echo "ci-shard: shard $index/$total of $dir is empty" >&2
		exit 1
	fi
	printf '%s\n' "-run=$(pattern "$names")"
	exit 0
fi

others=$(printf '%s\n' "$all" | awk -v n="$total" 'NF && (NR - 1) % n != n - 1')
if [ -z "$others" ]; then
	echo "-run=."
else
	printf '%s\n' "-skip=$(pattern "$others")"
fi
