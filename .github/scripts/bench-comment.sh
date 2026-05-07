#!/usr/bin/env bash
# bench-comment.sh — emits the sticky-comment markdown body for the bench
# smoke workflow. Two branches: success (table from bench.json) and failure
# (workflow-link callout). The marker line is required at the top of both —
# the workflow uses it to find an existing comment to update.
#
# The smoke comment intentionally only surfaces source-update numbers — the
# headline event for a per-PR check. Self-heal and ns-flip distributions are
# tracked in bench.json but kept out of this comment to avoid noise.
#
# Args:
#   $1: path to bench.json (may be missing or empty on failure)
#   $2: short commit SHA
#   $3: workflow run URL
#
# Output: markdown to stdout. Exits non-zero only on missing args.

set -euo pipefail
# Numeric formatting in awk's printf honors LC_NUMERIC — pin C so "1.5ms"
# doesn't become "1,5ms" on a comma-decimal runner locale.
export LC_ALL=C

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

# Profile-shape sentence: read counts from JSON so non-mixed profiles
# (np-typical only, etc.) describe themselves correctly. Skip shapes
# whose count is 0 to avoid "0 CP-selector destinations" awkwardness.
NP_COUNT=$(jq -r '.profile.NamespacedProjections // 0' "$BENCH_JSON")
CPSEL_COUNT=$(jq -r '.profile.SelectorNamespaces // 0' "$BENCH_JSON")
CPLIST_COUNT=$(jq -r '.profile.ListNamespaces // 0' "$BENCH_JSON")
PROFILE_DESC=""
if [ "$NP_COUNT" -gt 0 ]; then
  PROFILE_DESC="$NP_COUNT namespaced Projections"
fi
if [ "$CPSEL_COUNT" -gt 0 ]; then
  [ -n "$PROFILE_DESC" ] && PROFILE_DESC+=" + "
  PROFILE_DESC+="$CPSEL_COUNT CP-selector destinations"
fi
if [ "$CPLIST_COUNT" -gt 0 ]; then
  [ -n "$PROFILE_DESC" ] && PROFILE_DESC+=" + "
  PROFILE_DESC+="$CPLIST_COUNT CP-list destinations"
fi
[ -z "$PROFILE_DESC" ] && PROFILE_DESC="(no shapes set — empty profile?)"

NP_SAMPLES=$(n measurements.e2e_np_source_update_samples)
NP_P50=$(ms measurements.e2e_np_source_update_p50_ns)
NP_P95=$(ms measurements.e2e_np_source_update_p95_ns)
NP_P99=$(ms measurements.e2e_np_source_update_p99_ns)

CPSEL_SAMPLES=$(n measurements.e2e_cp_sel_source_update_samples)
CPSEL_E_P50=$(ms measurements.e2e_cp_sel_source_update_earliest_p50_ns)
CPSEL_E_P95=$(ms measurements.e2e_cp_sel_source_update_earliest_p95_ns)
CPSEL_E_P99=$(ms measurements.e2e_cp_sel_source_update_earliest_p99_ns)
CPSEL_S_P50=$(ms measurements.e2e_cp_sel_source_update_slowest_p50_ns)
CPSEL_S_P95=$(ms measurements.e2e_cp_sel_source_update_slowest_p95_ns)
CPSEL_S_P99=$(ms measurements.e2e_cp_sel_source_update_slowest_p99_ns)

CPLIST_SAMPLES=$(n measurements.e2e_cp_list_source_update_samples)
CPLIST_E_P50=$(ms measurements.e2e_cp_list_source_update_earliest_p50_ns)
CPLIST_E_P95=$(ms measurements.e2e_cp_list_source_update_earliest_p95_ns)
CPLIST_E_P99=$(ms measurements.e2e_cp_list_source_update_earliest_p99_ns)
CPLIST_S_P50=$(ms measurements.e2e_cp_list_source_update_slowest_p50_ns)
CPLIST_S_P95=$(ms measurements.e2e_cp_list_source_update_slowest_p95_ns)
CPLIST_S_P99=$(ms measurements.e2e_cp_list_source_update_slowest_p99_ns)

cat <<EOF
<!-- bench-smoke -->
## Bench smoke — \`$PROFILE_NAME\`

End-to-end source-update latency from a 2-vCPU GHA runner. Treat absolute numbers as a sanity check, not a perf claim — runner noise is high. The point of this check is to catch shape-break regressions on \`api/v1\` / controller / bench changes before merge. (Self-heal and ns-flip distributions are recorded in \`bench.json\` but omitted here for signal-to-noise.)

### Profile

$PROFILE_DESC, layered in one bootstrap.

### Results — source-update latency

| Path | Samples | p50 | p95 | p99 |
|---|---|---|---|---|
| NP single-target | $NP_SAMPLES | $NP_P50 | $NP_P95 | $NP_P99 |
| CP-selector earliest | $CPSEL_SAMPLES | $CPSEL_E_P50 | $CPSEL_E_P95 | $CPSEL_E_P99 |
| CP-selector slowest | $CPSEL_SAMPLES | $CPSEL_S_P50 | $CPSEL_S_P95 | $CPSEL_S_P99 |
| CP-list earliest | $CPLIST_SAMPLES | $CPLIST_E_P50 | $CPLIST_E_P95 | $CPLIST_E_P99 |
| CP-list slowest | $CPLIST_SAMPLES | $CPLIST_S_P50 | $CPLIST_S_P95 | $CPLIST_S_P99 |

Total wall: ${DURATION}s • Commit: \`$SHORT_SHA\` • [Workflow run]($RUN_URL)
EOF
