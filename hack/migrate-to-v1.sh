#!/usr/bin/env bash
# migrate-to-v1.sh — prepare a v0.1.0-alpha cluster for a v0.2.0 upgrade.
#
# Scans every Projection in the cluster, collects unique source
# (apiVersion, kind, namespace, name) tuples, and annotates each source with
# projection.be0x74a.io/projectable="true". Required before upgrading to v0.2,
# whose default --source-mode=allowlist ignores sources lacking this
# annotation.
#
# Dry-run is the default: a table of planned actions is printed and nothing is
# mutated. Pass --apply to execute the annotations.
#
# Usage:
#   ./hack/migrate-to-v1.sh                    # dry-run, default kubeconfig
#   ./hack/migrate-to-v1.sh --apply            # mutate
#   ./hack/migrate-to-v1.sh --context my-ctx   # dry-run, explicit context
#
# See docs/upgrade.md for the broader migration guide.

set -euo pipefail

readonly ANNOTATION='projection.be0x74a.io/projectable'
APPLY=false
CONTEXT=""

usage() {
    cat <<EOF
Usage: $(basename "$0") [--apply] [--context NAME] [-h|--help]

Prepares a projection v0.1 cluster for upgrade to v0.2 by annotating all
Projection sources with ${ANNOTATION}="true". Dry-run by default.

Options:
  --apply              Execute the annotations. Without this flag, the script
                       prints the plan and exits 0 without mutating anything.
  --context NAME       kubectl context to use. Defaults to the current context.
  -h, --help           Show this help and exit.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --apply) APPLY=true; shift ;;
        --context) CONTEXT="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# Validate dependencies. Fail fast with a clear message rather than a cryptic
# "command not found" halfway through the run.
for tool in kubectl jq; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "error: $tool not found on PATH" >&2
        exit 2
    fi
done

KCTL=(kubectl)
if [[ -n "$CONTEXT" ]]; then
    KCTL+=(--context "$CONTEXT")
fi

if ! "${KCTL[@]}" cluster-info >/dev/null 2>&1; then
    echo "error: kubectl cannot reach the cluster" >&2
    exit 2
fi

# Enumerate Projections and extract unique source tuples. The jq pipeline
# produces tab-separated (apiVersion, kind, namespace, name) lines; `sort -u`
# dedupes the case where multiple Projections share the same source.
if ! projections_json=$("${KCTL[@]}" get projections.projection.be0x74a.io -A -o json 2>&1); then
    echo "error: failed to list Projections — is the CRD installed?" >&2
    echo "$projections_json" >&2
    exit 1
fi

sources=$(printf '%s' "$projections_json" | jq -r '.items[]
    | [.spec.source.apiVersion, .spec.source.kind,
       .spec.source.namespace, .spec.source.name]
    | @tsv' | sort -u)

if [[ -z "$sources" ]]; then
    echo "no Projections found; nothing to migrate."
    exit 0
fi

printf '%-12s %-14s %-14s %-30s %s\n' NAMESPACE APIVERSION KIND NAME ACTION

count_annotate=0
count_skip_exists=0
count_skip_opted_out=0
count_skip_not_found=0
exit_code=0

while IFS=$'\t' read -r api kind ns name; do
    # For apiVersion="v1" (core group) kubectl expects just the kind; for
    # apiVersion="apps/v1" it accepts "kind.group".
    if [[ "$api" == */* ]]; then
        resource="$kind.${api%%/*}"
    else
        resource="$kind"
    fi

    # kubectl exits non-zero with a NotFound error for missing objects; stderr is
    # suppressed and the trailing `|| true` clears the exit code, leaving
    # `existing` empty so the branch below treats it as "source not found".
    existing=$("${KCTL[@]}" -n "$ns" get "$resource" "$name" -o json 2>/dev/null || true)
    if [[ -z "$existing" ]]; then
        printf '%-12s %-14s %-14s %-30s %s\n' "$ns" "$api" "$kind" "$name" "skip (source not found)"
        count_skip_not_found=$((count_skip_not_found + 1))
        continue
    fi

    current=$(printf '%s' "$existing" | jq -r --arg a "$ANNOTATION" '.metadata.annotations[$a] // ""')
    case "$current" in
        true)
            printf '%-12s %-14s %-14s %-30s %s\n' "$ns" "$api" "$kind" "$name" "skip (already annotated)"
            count_skip_exists=$((count_skip_exists + 1))
            continue
            ;;
        false)
            printf '%-12s %-14s %-14s %-30s %s\n' "$ns" "$api" "$kind" "$name" "skip (owner opted out)"
            count_skip_opted_out=$((count_skip_opted_out + 1))
            continue
            ;;
    esac

    printf '%-12s %-14s %-14s %-30s %s\n' "$ns" "$api" "$kind" "$name" "annotate (projectable=true)"
    if $APPLY; then
        # Capture stderr so we can distinguish a benign concurrent-annotate
        # race from a real error. The earlier read-check filtered the case
        # where the annotation existed at start; if `kubectl annotate
        # --overwrite=false` now reports "already has a value", the source
        # owner annotated between our check and this apply. Either value
        # they wrote (true/false) is their choice; we respect it and count
        # the source as a skip rather than bumping exit_code.
        if ! annotate_err=$("${KCTL[@]}" -n "$ns" annotate "$resource" "$name" \
                "${ANNOTATION}=true" --overwrite=false 2>&1); then
            if [[ "$annotate_err" == *"already has a value"* ]]; then
                count_skip_exists=$((count_skip_exists + 1))
                continue
            fi
            echo "  -> error annotating $ns/$name: $annotate_err" >&2
            exit_code=1
            continue
        fi
    fi
    count_annotate=$((count_annotate + 1))
done <<<"$sources"

echo
if $APPLY; then
    echo "Applied: ${count_annotate} annotated, ${count_skip_exists} already annotated, ${count_skip_opted_out} opted out, ${count_skip_not_found} not found."
else
    echo "Plan: ${count_annotate} to annotate, ${count_skip_exists} already annotated, ${count_skip_opted_out} opted out, ${count_skip_not_found} not found."
    echo "Re-run with --apply to execute."
fi

exit "$exit_code"
