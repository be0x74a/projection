#!/usr/bin/env bash
# Scripted demo for asciinema recording.
#
# Run via hack/record-demo.sh, which provisions a Kind cluster + helm-installs
# the operator first. Standalone, this script assumes:
#   - kubectl context points at a cluster with the projection operator
#     running in the projection-system namespace
#   - the staging/production namespaces don't already exist (or are empty)

set -euo pipefail

MANIFEST="hack/demo-manifest.yaml"

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

# Idempotency: clear prior demo state before recording so re-runs look clean.
kubectl delete --ignore-not-found ns staging production >/dev/null 2>&1 || true
while kubectl get ns staging >/dev/null 2>&1 || kubectl get ns production >/dev/null 2>&1; do
    sleep 0.5
done

clear
printf '\033[1;36m# projection — declarative resource mirroring across namespaces\033[0m\n'
sleep 2.5

step "cat ${MANIFEST}" 5.0

step "kubectl apply -f ${MANIFEST}" 2.0

step "kubectl wait --for=condition=Ready --timeout=30s -n staging projection/app-config-mirror" 2.0

step "kubectl get cm -n production app-config -o jsonpath='{.data}' && echo" 3.5

step "kubectl patch cm -n staging app-config --type=merge -p '{\"data\":{\"version\":\"v2\"}}'" 2.5

step "kubectl get cm -n production app-config -o jsonpath='{.data}' && echo" 4.5

printf '\n\033[1;36m# Source edit propagates to destination in ~100ms.\033[0m\n'
sleep 3.5
