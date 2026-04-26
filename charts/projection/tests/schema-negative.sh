#!/usr/bin/env bash
# Asserts values.schema.json rejects supportedKinds entries missing the `resources` field.
# Runs in the chart-test CI job and is safe to invoke locally.
#
# Uses `helm lint -f bad.yaml` (not `helm template`) because lint runs schema
# validation but does NOT render templates. This isolates the schema rejection
# from the existing template-side `fail` guard in clusterrole.yaml, which would
# otherwise mask a missing schema by failing for its own reason.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

bad_values=$(mktemp)
trap 'rm -f "$bad_values"' EXIT

cat >"$bad_values" <<'YAML'
supportedKinds:
  - apiGroup: apps
YAML

# Run helm lint and capture its exit status separately. Plain `output=$(cmd || true)`
# would swallow the exit code into the assignment's status (always 0).
status=0
output=$(helm lint "$CHART_DIR" -f "$bad_values" 2>&1) || status=$?

if [[ $status -eq 0 ]]; then
  echo "FAIL: helm lint accepted the malformed override; schema is missing or too permissive." >&2
  echo "Output was:" >&2
  echo "$output" >&2
  exit 1
fi

if ! grep -qiE 'resources|required' <<<"$output"; then
  echo "FAIL: helm lint failed but the error did not mention the missing 'resources' field." >&2
  echo "Output was:" >&2
  echo "$output" >&2
  exit 1
fi

echo "OK: schema rejected the malformed override."
