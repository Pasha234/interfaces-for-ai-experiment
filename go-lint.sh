#!/usr/bin/env bash
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNS_DIR="$ROOT_DIR/runs"
REPORTS_DIR="$ROOT_DIR/lint-results"
LINTERS="govet,staticcheck,revive,errcheck,gocyclo,gocognit,maintidx,dupl,gosec,godoclint"

usage() {
  printf 'Usage: %s <text-run-1|contract-run-1|--all> [phase1|phase2]\n' "$0"
  printf '       %s runs/text-run-1 [phase1|phase2]\n' "$0"
}

if (( $# < 1 || $# > 2 )); then
  usage >&2
  exit 2
fi

for tool in golangci-lint lizard python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'Error: required executable not found: %s\n' "$tool" >&2
    exit 2
  fi
done

suffix=""
if (( $# == 2 )); then
  if [[ "$2" != "phase1" && "$2" != "phase2" ]]; then
    echo 'Error: phase must be phase1 or phase2' >&2
    exit 2
  fi
  suffix="-$2"
fi

mkdir -p "$REPORTS_DIR"

analyze_run() {
  local name="$1"
  local project="$RUNS_DIR/$name"
  local lint_report="$REPORTS_DIR/${name}${suffix}.json"
  local lizard_report="$REPORTS_DIR/${name}${suffix}-lizard.csv"
  local summary_report="$REPORTS_DIR/${name}${suffix}-lizard-summary.json"
  local lint_status=0 lizard_status=0

  if [[ ! -f "$project/go.mod" ]]; then
    echo "Error: $project/go.mod not found" >&2
    return 2
  fi

  echo "=== Analyze: ${name}${suffix} ==="
  rm -f "$lint_report" "$lizard_report" "$summary_report"

  (
    cd "$project" || exit 2
    golangci-lint run \
      --enable-only="$LINTERS" \
      --tests=false \
      --max-issues-per-linter=0 \
      --max-same-issues=0 \
      --uniq-by-line=false \
      --output.text.path=stderr \
      --output.json.path="$lint_report" \
      ./...
  ) || lint_status=$?

  if [[ -f "$lint_report" ]]; then
    echo "golangci-lint JSON: $lint_report"
    python3 -c '
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as file:
        issues = json.load(file).get("Issues") or []
    print("Linter issues:", len(issues))
except (OSError, ValueError) as exc:
    print("Could not read lint JSON:", exc, file=sys.stderr)
    sys.exit(2)
' "$lint_report" || lint_status=2
  else
    echo "Warning: golangci-lint produced no JSON report (exit $lint_status)" >&2
    lint_status=2
  fi

  # Collect only Go application source files, excluding tests and vendor code.
  local -a go_files=()
  mapfile -d '' -t go_files < <(
    cd "$project" || exit 2
    find . -type d \( -name .git -o -name vendor \) -prune -o \
      -type f -name '*.go' ! -name '*_test.go' -print0
  )

  if (( ${#go_files[@]} == 0 )); then
    echo "Warning: no Go application source files in $project" >&2
    lizard_status=2
  else
    (
      cd "$project" || exit 2
      lizard -l go -V --csv "${go_files[@]}" > "$lizard_report"
    ) || lizard_status=$?
  fi

  if [[ "$lizard_status" -eq 0 && -f "$lizard_report" ]]; then
    python3 -c '
import csv, json, statistics, sys
try:
    with open(sys.argv[1], newline="", encoding="utf-8-sig") as file:
        rows = list(csv.DictReader(file))
    def values(column):
        return [float(row[column]) for row in rows]
    ccns = values("CCN")
    nlocs = values("NLOC")
    params = values("PARAM")
    result = {
        "function_count": len(rows),
        "max_ccn": max(ccns, default=0),
        "mean_ccn": round(statistics.mean(ccns), 2) if ccns else 0,
        "functions_ccn_gt_10": sum(x > 10 for x in ccns),
        "max_function_nloc": max(nlocs, default=0),
        "mean_function_nloc": round(statistics.mean(nlocs), 2) if nlocs else 0,
        "max_parameters": max(params, default=0),
    }
    with open(sys.argv[2], "w", encoding="utf-8") as file:
        json.dump(result, file, ensure_ascii=False, indent=2)
        file.write("\n")
    print("Lizard:", result["function_count"], "functions; max CCN:", result["max_ccn"], "; mean CCN:", result["mean_ccn"])
except (OSError, ValueError, KeyError) as exc:
    print("Could not summarize Lizard CSV:", exc, file=sys.stderr)
    sys.exit(2)
' "$lizard_report" "$summary_report" || lizard_status=2
    echo "Lizard per-function CSV: $lizard_report"
    if [[ "$lizard_status" -eq 0 ]]; then
      echo "Lizard summary JSON: $summary_report"
    fi
  else
    echo "Warning: Lizard failed to produce a CSV report (exit $lizard_status)" >&2
    lizard_status=2
  fi

  echo "Exit status: golangci-lint=$lint_status; lizard=$lizard_status"
  echo
  # Lint findings are expected in a quality benchmark; failure to run is not.
  if [[ "$lint_status" -gt 1 || "$lizard_status" -ne 0 ]]; then
    return 2
  fi
  return 0
}

if [[ "$1" == "--all" ]]; then
  found=0
  failed=0
  for project in "$RUNS_DIR"/*; do
    [[ -d "$project" && -f "$project/go.mod" ]] || continue
    found=1
    analyze_run "$(basename "$project")" || failed=1
  done
  if (( found == 0 )); then
    echo "Error: no Go modules found in $RUNS_DIR" >&2
    exit 2
  fi
  exit "$failed"
fi

name="${1#runs/}"
if [[ -z "$name" || "$name" == */* || "$name" == '.' || "$name" == '..' ]]; then
  echo 'Error: pass a run name like text-run-1' >&2
  exit 2
fi

analyze_run "$name"