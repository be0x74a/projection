#!/usr/bin/env bash
# Scripted demo for asciinema recording.
#
# Run via hack/record-demo.sh, which provisions a Kind cluster + helm-installs
# the operator first. Standalone, this script assumes:
#   - kubectl context points at a cluster with the projection operator
#     running in the projection-system namespace
#   - the demo namespaces (staging, production, platform, tenant-a/b/c) don't
#     already exist (or are empty)

set -euo pipefail

MANIFEST="hack/demo-manifest.yaml"
DEMO_NS=(staging production platform tenant-a tenant-b tenant-c)

# Visual prompt + run-and-pace helper. The second arg is the post-command
# read-time in seconds (default 2.0): tune it per step so dense outputs get
# enough dwell time and one-liners get just enough to register. eval lets
# the cmd carry shell quoting (notably the patch JSON below) without
# escaping the whole call site.
step() {
    local cmd="$1"
    local read_time="${2:-2.0}"
    printf '\n\033[1;32m$\033[0m %s\n' "$cmd"
    sleep 0.6
    eval "$cmd"
    sleep "$read_time"
}

act() {
    local title="$1"
    printf '\n\033[1;36m# %s\033[0m\n' "$title"
    sleep 2.0
}

# Idempotency: clear prior demo state before recording so re-runs look clean.
kubectl delete --ignore-not-found ns "${DEMO_NS[@]}" >/dev/null 2>&1 || true
for ns in "${DEMO_NS[@]}"; do
    while kubectl get ns "$ns" >/dev/null 2>&1; do sleep 0.5; done
done
kubectl delete --ignore-not-found clusterprojection ca-bundle-fanout >/dev/null 2>&1 || true

clear
printf '\033[1;36m# projection — namespace-tier and cluster-tier mirroring\033[0m\n'
sleep 2.5

step "kubectl apply -f ${MANIFEST}" 2.0

# --- Act 1 ---
act "Act 1 — namespace-tier: a Projection mirrors one source into one namespace"

step "kubectl wait --for=condition=Ready --timeout=30s -n production projection/app-config-mirror" 2.0

step "kubectl get cm -n production app-config -o jsonpath='{.data}' && echo" 3.5

step "kubectl patch cm -n staging app-config --type=merge -p '{\"data\":{\"version\":\"v2\"}}'" 2.5

step "kubectl get cm -n production app-config -o jsonpath='{.data}' && echo" 3.5

# --- Act 2 ---
act "Act 2 — cluster-tier: a ClusterProjection fans one source out to many namespaces"

step "kubectl wait --for=condition=Ready --timeout=30s clusterprojection/ca-bundle-fanout" 2.0

step "kubectl get cm -A | grep -E 'NAMESPACE|ca-bundle'" 4.5

# --- Act 3 ---
act "Act 3 — self-heal: kubectl-delete a destination, watch it come back"

step "kubectl delete cm ca-bundle -n tenant-a" 1.0

# Time the recovery loop. The big number is the demo's punch line —
# ensureDestWatch typically fires inside ~100 ms.
RECOVERY_START=$(python3 -c 'import time; print(time.time())')
until kubectl get cm ca-bundle -n tenant-a >/dev/null 2>&1; do
    sleep 0.02
    if [ $(python3 -c "import time; print(int((time.time() - $RECOVERY_START)))") -gt 30 ]; then
        echo "  TIMEOUT" >&2
        break
    fi
done
RECOVERY_MS=$(python3 -c "import time; print(int((time.time() - $RECOVERY_START) * 1000))")
printf '\n\033[1;33m  ensureDestWatch fired — recovered in %d ms\033[0m\n' "$RECOVERY_MS"
sleep 2.5

step "kubectl get cm ca-bundle -n tenant-a" 4.0

printf '\n\033[1;36m# Two CRDs, one operator. Self-healing watches keep destinations honest.\033[0m\n'
sleep 3.5
