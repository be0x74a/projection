#!/usr/bin/env bash
# bench-comment.sh — emits the sticky-comment markdown body for the bench
# smoke workflow. Two branches: success (table from bench.json) and failure
# (workflow-link callout). The marker line is required at the top of both —
# the workflow uses it to find an existing comment to update.
#
# Args:
#   $1: path to bench.json (may be missing or empty on failure)
#   $2: short commit SHA
#   $3: workflow run URL
#
# Output: markdown to stdout. Exits non-zero only on missing args.

set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <bench.json|''> <short-sha> <run-url>" >&2
  exit 2
fi

BENCH_JSON="${1:-}"
SHORT_SHA="${2:-}"
RUN_URL="${3:-}"

# Detect failure: empty path, missing file, empty file, or unparseable JSON.
is_success=false
if [ -n "$BENCH_JSON" ] && [ -s "$BENCH_JSON" ]; then
  if jq -e . "$BENCH_JSON" >/dev/null 2>&1; then
    is_success=true
  fi
fi

if [ "$is_success" != true ]; then
  cat <<EOF
<!-- bench-smoke -->
## Bench smoke — \`mixed-typical\` ❌

Smoke check failed. The bench harness either crashed during bootstrap, exceeded its measurement timeout, or hit a schema-validation error against the deployed operator. This usually means a change in \`api/v1\`, \`internal/controller\`, or \`test/bench\` introduced an incompatibility.

[Workflow run]($RUN_URL) for details.

Commit: \`$SHORT_SHA\`
EOF
  exit 0
fi

# Success branch — pull metrics out of bench.json and format the table.
# fmt_ms takes a JSON path and emits "<X>ms" with one decimal, or "—" when
# the field is missing/zero (selector/list disabled in profile, etc).
ms() {
  local ns
  ns=$(jq -r --arg path "$1" '(getpath($path | split(".")) // 0) | tostring' "$BENCH_JSON")
  if [ "$ns" = "0" ] || [ -z "$ns" ]; then
    echo "—"
  else
    awk -v ns="$ns" 'BEGIN { printf "%.1fms\n", ns / 1000000 }'
  fi
}

n() {
  jq -r --arg path "$1" '
    (getpath($path | split(".")) // 0) as $v
    | if $v == 0 then "—" else ($v | tostring) end
  ' "$BENCH_JSON"
}

PROFILE_NAME=$(jq -r '.profile.Name // "mixed-typical"' "$BENCH_JSON")
DURATION=$(jq -r '.duration_seconds // 0 | floor | tostring' "$BENCH_JSON")

NP_SAMPLES=$(n measurements.e2e_np_samples)
NP_P50=$(ms measurements.e2e_np_p50_ns)
NP_P95=$(ms measurements.e2e_np_p95_ns)
NP_P99=$(ms measurements.e2e_np_p99_ns)

CPSEL_SAMPLES=$(n measurements.e2e_cp_sel_samples)
CPSEL_E_P50=$(ms measurements.e2e_cp_sel_earliest_p50_ns)
CPSEL_E_P95=$(ms measurements.e2e_cp_sel_earliest_p95_ns)
CPSEL_E_P99=$(ms measurements.e2e_cp_sel_earliest_p99_ns)
CPSEL_S_P50=$(ms measurements.e2e_cp_sel_slowest_p50_ns)
CPSEL_S_P95=$(ms measurements.e2e_cp_sel_slowest_p95_ns)
CPSEL_S_P99=$(ms measurements.e2e_cp_sel_slowest_p99_ns)

CPLIST_SAMPLES=$(n measurements.e2e_cp_list_samples)
CPLIST_E_P50=$(ms measurements.e2e_cp_list_earliest_p50_ns)
CPLIST_E_P95=$(ms measurements.e2e_cp_list_earliest_p95_ns)
CPLIST_E_P99=$(ms measurements.e2e_cp_list_earliest_p99_ns)
CPLIST_S_P50=$(ms measurements.e2e_cp_list_slowest_p50_ns)
CPLIST_S_P95=$(ms measurements.e2e_cp_list_slowest_p95_ns)
CPLIST_S_P99=$(ms measurements.e2e_cp_list_slowest_p99_ns)

cat <<EOF
<!-- bench-smoke -->
## Bench smoke — \`$PROFILE_NAME\`

End-to-end latency from a 2-vCPU GHA runner. Treat absolute numbers as a sanity check, not a perf claim — runner noise is high. The point of this check is to catch shape-break regressions on \`api/v1\` / controller / bench changes before merge.

### Profile

100 namespaced Projections + 50 CP-selector destinations + 10 CP-list destinations, layered in one bootstrap.

### Results

| Path | Samples | p50 | p95 | p99 |
|---|---|---|---|---|
| NP single-target | $NP_SAMPLES | $NP_P50 | $NP_P95 | $NP_P99 |
| CP-selector earliest | $CPSEL_SAMPLES | $CPSEL_E_P50 | $CPSEL_E_P95 | $CPSEL_E_P99 |
| CP-selector slowest | $CPSEL_SAMPLES | $CPSEL_S_P50 | $CPSEL_S_P95 | $CPSEL_S_P99 |
| CP-list earliest | $CPLIST_SAMPLES | $CPLIST_E_P50 | $CPLIST_E_P95 | $CPLIST_E_P99 |
| CP-list slowest | $CPLIST_SAMPLES | $CPLIST_S_P50 | $CPLIST_S_P95 | $CPLIST_S_P99 |

Total wall: ${DURATION}s • Commit: \`$SHORT_SHA\` • [Workflow run]($RUN_URL)
EOF
