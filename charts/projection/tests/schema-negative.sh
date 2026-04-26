#!/usr/bin/env bash
# Asserts values.schema.json rejects supportedKinds entries missing the `resources` field.
# Runs in the chart-test CI job and is safe to invoke locally.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

bad_values=$(mktemp)
trap 'rm -f "$bad_values"' EXIT

cat >"$bad_values" <<'YAML'
supportedKinds:
  - apiGroup: apps
YAML

# Capture both stdout and stderr; helm prints schema errors to stderr.
output=$(helm template projection "$CHART_DIR" -f "$bad_values" 2>&1 || true)
status=$?

# helm template returns 0 on success. With the schema in place, supplying a
# supportedKinds entry that omits `resources` must cause a non-zero exit.
if [[ $status -eq 0 ]]; then
  echo "FAIL: helm template succeeded; schema did not reject the malformed override." >&2
  echo "Output was:" >&2
  echo "$output" >&2
  exit 1
fi

# The error message must mention the missing field so users can fix it.
if ! grep -qiE 'resources|required' <<<"$output"; then
  echo "FAIL: helm template failed but the error did not mention the missing 'resources' field." >&2
  echo "Output was:" >&2
  echo "$output" >&2
  exit 1
fi

echo "OK: schema rejected the malformed override."
