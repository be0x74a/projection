#!/usr/bin/env bash
# observe.sh — snapshot of operator + projections state, designed to be grepped
# by tooling. Usage: ./hack/observe.sh [projection-name] [projection-namespace]
# When a name/namespace is supplied, the destination object is also dumped.

set -u
CTX="${KUBECTL_CONTEXT:-kind-projection-dev}"
KCTL=(kubectl --context "$CTX")
NS_SYS="projection-system"
PROJ_NAME="${1:-}"
PROJ_NS="${2:-default}"

section() { printf '\n=== %s ===\n' "$1"; }

section "CLUSTER"
"${KCTL[@]}" cluster-info 2>&1 | head -2 || true
"${KCTL[@]}" get nodes 2>&1

section "OPERATOR POD"
"${KCTL[@]}" -n "$NS_SYS" get pods -o wide 2>&1

section "OPERATOR LOGS (last 80 lines)"
"${KCTL[@]}" -n "$NS_SYS" logs -l control-plane=controller-manager --tail=80 --all-containers 2>&1 || true

section "PROJECTIONS (all namespaces)"
"${KCTL[@]}" get projections -A 2>&1

section "PROJECTION STATUS CONDITIONS"
"${KCTL[@]}" get projections -A -o json 2>/dev/null \
  | jq -r '.items[] | "\(.metadata.namespace)/\(.metadata.name): \((.status.conditions // []) | map("\(.type)=\(.status) reason=\(.reason) msg=\(.message // "")") | join("; "))"' 2>&1

section "RECENT EVENTS (operator namespace, last 20)"
"${KCTL[@]}" -n "$NS_SYS" get events --sort-by=.lastTimestamp 2>&1 | tail -20

if [[ -n "$PROJ_NAME" ]]; then
  section "PROJECTION $PROJ_NS/$PROJ_NAME (full)"
  "${KCTL[@]}" -n "$PROJ_NS" get projection "$PROJ_NAME" -o yaml 2>&1

  SRC=$("${KCTL[@]}" -n "$PROJ_NS" get projection "$PROJ_NAME" -o json 2>/dev/null \
        | jq -r '[.spec.source.apiVersion, .spec.source.kind, .spec.source.namespace, .spec.source.name] | @tsv')
  if [[ -n "$SRC" ]]; then
    IFS=$'\t' read -r SRC_API SRC_KIND SRC_NS SRC_NAME <<<"$SRC"
    section "SOURCE $SRC_API $SRC_KIND $SRC_NS/$SRC_NAME"
    "${KCTL[@]}" -n "$SRC_NS" get "$SRC_KIND" "$SRC_NAME" -o yaml 2>&1 | head -40
  fi

  DST=$("${KCTL[@]}" -n "$PROJ_NS" get projection "$PROJ_NAME" -o json 2>/dev/null \
        | jq -r '[
            (.spec.destination.namespace // .metadata.namespace),
            (.spec.destination.name // .spec.source.name)
          ] | @tsv')
  if [[ -n "$DST" && -n "${SRC_KIND:-}" ]]; then
    IFS=$'\t' read -r DST_NS DST_NAME <<<"$DST"
    section "DESTINATION $SRC_KIND $DST_NS/$DST_NAME"
    "${KCTL[@]}" -n "$DST_NS" get "$SRC_KIND" "$DST_NAME" -o yaml 2>&1 | head -60
  fi
fi

section "DONE"
