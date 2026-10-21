#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
gate="$(awk '
  /^  verify:/ { job = 1; next }
  job && /^  [A-Za-z0-9_-]+:/ { exit }
  job && /^        run: \|/ { body = 1; next }
  body && /^          / { sub(/^          /, ""); print; next }
  body { exit }
' .github/workflows/release.yml)"
if [[ -z "$gate" ]]; then
  echo "error: release verification has no aggregate gate to exercise" >&2
  exit 1
fi

if ! RESULTS='["success","success","success"]' bash -e -o pipefail -c "$gate" >/dev/null 2>&1; then
  echo "error: successful release verification was refused" >&2
  exit 1
fi
while IFS= read -r results; do
  if RESULTS="$results" bash -e -o pipefail -c "$gate" >/dev/null 2>&1; then
    echo "error: incomplete release verification passed: $results" >&2
    exit 1
  fi
done <<'JSON'
[]
null
"success"
["success","success"]
["success","success","success","success"]
["failure","success","success"]
["success","failure","success"]
["success","success","failure"]
["cancelled","success","success"]
["success","cancelled","success"]
["success","success","cancelled"]
["skipped","success","success"]
["success","skipped","success"]
["success","success","skipped"]
["success","success","unknown"]
["success","success",null]
not json
JSON
echo "release verification gate regression tests: ok"
