#!/usr/bin/env bash
# Report drift between the vendored upstream contract and upstream main.
# Prints a Markdown report on drift, nothing when in sync. Non-zero on error.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
. ./upstream.lock
: "${REPO:?upstream.lock must define REPO}"

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
git clone -q --depth 1 "$REPO" "$tmp"   # exits non-zero if the clone fails
rev=$(git -C "$tmp" rev-parse HEAD)

report=""
add() { report="${report}$1"$'\n'; }

cmp -s openapi.yaml "$tmp/openapi.yaml" || add "- changed: \`openapi.yaml\`"
for f in test_cases/*.json; do
  n=${f#test_cases/}
  if [ ! -f "$tmp/$f" ]; then
    add "- removed upstream: \`test_cases/$n\`"
  elif ! cmp -s "$f" "$tmp/$f"; then
    add "- changed: \`test_cases/$n\`"
  fi
done
for f in "$tmp"/test_cases/*.json; do
  n=${f#"$tmp"/test_cases/}
  [ -f "test_cases/$n" ] || add "- added upstream: \`test_cases/$n\`"
done

[ -z "$report" ] && exit 0

printf 'Upstream main has contract changes since the vendored revision.\n\n'
printf 'Upstream HEAD: `%s`\n\n' "$rev"
printf '%s' "$report"
printf '\nAbsorb the changes with:\n\n```sh\nmake vendor-upstream REV=%s\n```\n' "$rev"
