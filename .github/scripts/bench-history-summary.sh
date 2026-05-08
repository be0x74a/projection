#!/usr/bin/env bash
# bench-history-summary.sh — emits the markdown body of the release-bench
# step summary (see `.github/workflows/bench.yml`). One big table per event
# shape (source-update, self-heal, ns-flip), keyed by profile, with one row
# per profile whether or not it actually exercised that shape (em-dash for
# missing).
#
# Args:
#   $1: path to bench.json — a JSON array of per-profile reports (from
#       `jq -s` over the harness's stdout).
#   $2: label string used in the run header and the bench-history link.
#
# Output: markdown to stdout.
#
# Notes:
#   - The script reads bench.json once into a single jq pass per metric. This
#     avoids the O(profiles * metrics) re-reads that would happen with
#     per-row jq invocations.
#   - LC_ALL=C pins printf's number formatting so "1.5ms" doesn't render as
#     "1,5ms" on a comma-decimal locale (lesson from bench-comment.sh).
#   - Em-dash represents missing/zero values uniformly: a profile that
#     doesn't exercise a shape, or a shape that recorded zero samples.

set -euo pipefail
export LC_ALL=C

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <bench.json> <label>" >&2
  exit 2
fi

BENCH_JSON="$1"
LABEL="$2"

if [ ! -s "$BENCH_JSON" ]; then
  echo "bench-history-summary: missing or empty $BENCH_JSON" >&2
  exit 1
fi
if ! jq -e 'type == "array"' "$BENCH_JSON" >/dev/null 2>&1; then
  echo "bench-history-summary: $BENCH_JSON is not a JSON array" >&2
  exit 1
fi

# ms <ns-value> → "<X>ms" (one decimal) or "—" for zero/empty/null.
ms() {
  local ns="${1:-}"
  if [ -z "$ns" ] || [ "$ns" = "null" ] || [ "$ns" = "0" ]; then
    echo "—"
  else
    awk -v ns="$ns" 'BEGIN { printf "%.1fms\n", ns / 1000000 }'
  fi
}

# Cache profile names + total wall-clock once.
mapfile -t PROFILES < <(jq -r '.[].profile.Name' "$BENCH_JSON")
WALL_TOTAL=$(jq -r '[.[].duration_seconds] | add // 0 | floor' "$BENCH_JSON")
NUM_PROFILES=${#PROFILES[@]}

# field <profile-name> <jq-path> emits the raw value (or empty string if the
# profile doesn't exist or the field is missing).
field() {
  local name="$1" path="$2"
  jq -r --arg n "$name" --arg p "$path" '
    .[] | select(.profile.Name == $n)
    | (getpath($p | split(".")) // "") | tostring
  ' "$BENCH_JSON"
}

printf '# Bench: %s\n\n' "$LABEL"
printf 'Wall total: %ss • Profiles: %s\n\n' "$WALL_TOTAL" "$NUM_PROFILES"

# ── Source-update p99 ─────────────────────────────────────────────────────
printf '## Source-update p99 latency by profile and path\n\n'
printf '| Profile | NP | CP-sel slowest | CP-list slowest |\n'
printf '|---|---|---|---|\n'
for p in "${PROFILES[@]}"; do
  np=$(ms "$(field "$p" measurements.e2e_np_source_update_p99_ns)")
  sel=$(ms "$(field "$p" measurements.e2e_cp_sel_source_update_slowest_p99_ns)")
  lst=$(ms "$(field "$p" measurements.e2e_cp_list_source_update_slowest_p99_ns)")
  printf '| %s | %s | %s | %s |\n' "$p" "$np" "$sel" "$lst"
done
printf '\n'

# ── Self-heal p99 ─────────────────────────────────────────────────────────
printf '## Self-heal p99 latency by profile and path\n\n'
printf '| Profile | NP | CP-sel | CP-list |\n'
printf '|---|---|---|---|\n'
for p in "${PROFILES[@]}"; do
  np=$(ms "$(field "$p" measurements.e2e_np_self_heal_p99_ns)")
  sel=$(ms "$(field "$p" measurements.e2e_cp_sel_self_heal_p99_ns)")
  lst=$(ms "$(field "$p" measurements.e2e_cp_list_self_heal_p99_ns)")
  printf '| %s | %s | %s | %s |\n' "$p" "$np" "$sel" "$lst"
done
printf '\n'

# ── ns-flip p99 (CP-selector only) ────────────────────────────────────────
printf '## ns-flip p99 latency (CP-selector only)\n\n'
printf '| Profile | cleanup | add |\n'
printf '|---|---|---|\n'
for p in "${PROFILES[@]}"; do
  cleanup=$(ms "$(field "$p" measurements.e2e_cp_sel_ns_flip_cleanup_p99_ns)")
  add=$(ms "$(field "$p" measurements.e2e_cp_sel_ns_flip_add_p99_ns)")
  printf '| %s | %s | %s |\n' "$p" "$cleanup" "$add"
done
printf '\n'

# Footer link points at the file the workflow's push step will create. Use
# GITHUB_REPOSITORY when available (CI) and fall back to the project repo
# for local dry-runs.
REPO="${GITHUB_REPOSITORY:-projection-operator/projection}"
printf 'Full distributions in the [bench-history JSON](https://github.com/%s/blob/bench-history/bench-history/%s.json) and the workflow artifact.\n' \
  "$REPO" "$LABEL"
