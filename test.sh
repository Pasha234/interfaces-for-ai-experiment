#!/usr/bin/env bash

set -o pipefail

if [ $# -ne 1 ]; then
    echo "Usage: $0 <test_file.go>"
    exit 1
fi

result=$(mktemp)
trap 'rm -f "$result"' EXIT

go test -json "$1" | tee "$result" | jq -r '
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