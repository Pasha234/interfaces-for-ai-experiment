#!/usr/bin/env bash

set -o pipefail

if [ $# -ne 1 ]; then
    echo "Usage: $0 <test_file.go>"
    exit 1
fi

test_file="$1"
if [ ! -f "$test_file" ]; then
    echo "Error: test file not found: $test_file" >&2
    exit 1
fi

test_dir=$(dirname "$test_file")
tests=$(grep -oE '^func Test[A-Za-z0-9_]+\(' "$test_file" | sed -E 's/^func (Test[A-Za-z0-9_]+)\(/\1/' | paste -sd'|' -)

if [ -z "$tests" ]; then
    echo "Error: no Test* functions found in $test_file" >&2
    exit 1
fi

result=$(mktemp)
trap 'rm -f "$result"' EXIT

(cd "$test_dir" && go test -count=1 -json -run "^(${tests})$" .) | tee "$result" | jq -r '
    select(.Action == "output") | .Output | rtrimstr("\n")
'

exit_code=${PIPESTATUS[0]}

echo
echo "========== TEST SUMMARY =========="
jq -rs '
    [.[] | select(.Test != null)] |
    {
        passed: [.[] | select(.Action == "pass")] | length,
        failed: [.[] | select(.Action == "fail")] | length,
        skipped: [.[] | select(.Action == "skip")] | length
    } |
    "Passed: \(.passed) | Failed: \(.failed) | Skipped: \(.skipped)"
' "$result"

exit "$exit_code"
