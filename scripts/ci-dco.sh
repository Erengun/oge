#!/bin/sh
# Checks that every commit in base..head carries a Signed-off-by line for its
# author (Developer Certificate of Origin; see CONTRIBUTING.md).
set -eu

base=$1
head=$2
fail=0
for sha in $(git rev-list --no-merges "$base..$head"); do
	author=$(git log -1 --format='%an <%ae>' "$sha")
	if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha" | grep -qxF "$author"; then
		echo "ci-dco: $(git log -1 --format='%h %s' "$sha") has no 'Signed-off-by: $author'" >&2
		fail=1
	fi
done
if [ "$fail" -ne 0 ]; then
	echo "ci-dco: sign off with 'git commit -s' (amend or rebase existing commits)" >&2
fi
exit "$fail"
