#!/usr/bin/env bash
# Wrapper for hack/demo.sh — provisions a fresh Kind cluster, helm-installs
# the operator, then records the demo as an asciicast at docs/assets/demo.cast.
# If `agg` is installed, also produces docs/assets/demo.gif.
#
# Re-run safe: reuses the Kind cluster if one with the expected name already
# exists; the demo script itself wipes prior staging/production state.

set -euo pipefail

CLUSTER=projection-demo
NODE_IMAGE=kindest/node:v1.32.0
IMAGE_TAG=projection:demo
CAST_OUT=docs/assets/demo.cast
GIF_OUT=docs/assets/demo.gif
CLEANUP=false

usage() {
    cat <<EOF
Usage: $(basename "$0") [--cleanup]

Provisions Kind cluster '${CLUSTER}' with ${NODE_IMAGE}, builds ${IMAGE_TAG},
helm-installs the operator into projection-system, then records hack/demo.sh
as ${CAST_OUT}. If 'agg' is installed, additionally writes ${GIF_OUT}.

Options:
  --cleanup            Delete the Kind cluster after recording.
  -h, --help           Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --cleanup) CLEANUP=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
done

cd "$(git rev-parse --show-toplevel)"

# Prefer the repo-pinned kind binary over PATH so the recording matches CI.
KIND="${KIND:-./bin/kind}"
[[ -x "$KIND" ]] || KIND=kind

for tool in docker helm kubectl asciinema "$KIND"; do
    if ! command -v "$tool" >/dev/null 2>&1 && [[ ! -x "$tool" ]]; then
        echo "error: required tool not found: $tool" >&2
        echo "  install asciinema via: brew install asciinema" >&2
        exit 2
    fi
done

if "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
    echo "==> reusing existing Kind cluster: $CLUSTER"
else
    echo "==> creating Kind cluster: $CLUSTER ($NODE_IMAGE)"
    "$KIND" create cluster --name "$CLUSTER" --image "$NODE_IMAGE"
fi

# `kind create cluster` sets the kubectl context, but reusing an existing
# cluster does not — and other tools may have left current-context unset
# or pointed elsewhere. Make the context explicit so helm and kubectl below
# always hit the demo cluster.
kubectl config use-context "kind-$CLUSTER" >/dev/null

echo "==> building $IMAGE_TAG"
make docker-build IMG="$IMAGE_TAG" >/dev/null

echo "==> loading $IMAGE_TAG into Kind"
"$KIND" load docker-image "$IMAGE_TAG" --name "$CLUSTER"

echo "==> helm install/upgrade projection"
helm upgrade --install projection charts/projection \
    --namespace projection-system --create-namespace \
    --set image.repository=projection \
    --set image.tag=demo \
    --set image.pullPolicy=Never \
    --wait --timeout=180s >/dev/null

kubectl -n projection-system rollout status deploy/projection --timeout=60s

# Wipe any leftover demo state from a prior run so the recording starts
# crisp — without this the in-script cleanup loop in hack/demo.sh adds 10s+
# of leading idle time to the cast while namespaces terminate.
echo "==> wiping prior demo namespaces (if any)"
kubectl delete --ignore-not-found ns staging production --wait=false >/dev/null 2>&1 || true
while kubectl get ns staging >/dev/null 2>&1 || kubectl get ns production >/dev/null 2>&1; do
    sleep 0.5
done

mkdir -p "$(dirname "$CAST_OUT")"
echo "==> recording asciicast to $CAST_OUT"
asciinema rec "$CAST_OUT" \
    --command "bash hack/demo.sh" \
    --overwrite \
    --title "projection — namespace-tier and cluster-tier mirroring"

echo "==> cast saved: $CAST_OUT"

if command -v agg >/dev/null 2>&1; then
    echo "==> agg available: converting to $GIF_OUT"
    agg "$CAST_OUT" "$GIF_OUT"
    echo "==> gif saved: $GIF_OUT"
else
    echo "==> agg not installed; skipping GIF (install via: brew install agg)"
fi

if "$CLEANUP"; then
    echo "==> deleting Kind cluster: $CLUSTER"
    "$KIND" delete cluster --name "$CLUSTER"
fi
