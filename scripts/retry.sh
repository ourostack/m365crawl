#!/usr/bin/env bash
#
# Runs a command up to ATTEMPTS times, pausing PAUSE seconds between attempts, and exits with
# the last attempt's status. Used by verify-homebrew so one transient failure of the Homebrew
# install check does not roll the tap back and demote a good release.
#
#   scripts/retry.sh ATTEMPTS PAUSE COMMAND [ARG...]
#   scripts/retry.sh --selftest
set -euo pipefail

selftest() {
  local self counter status
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/retry-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  counter="$tmp/count"
  cat > "$tmp/flaky.sh" <<'STUB'
#!/usr/bin/env bash
# Fails until it has been run $2 times, then succeeds. $1 is the counter file.
n=$(($(cat "$1" 2>/dev/null || echo 0) + 1))
echo "$n" > "$1"
[[ "$n" -ge "$2" ]]
STUB
  chmod +x "$tmp/flaky.sh"
  status=0
  "$self" 3 0 "$tmp/flaky.sh" "$counter" 1 > /dev/null 2>&1 || status=$?
  [[ "$status" -eq 0 && "$(cat "$counter")" == 1 ]] || { echo "error: selftest: a first success should not retry" >&2; exit 1; }
  rm "$counter"
  "$self" 3 0 "$tmp/flaky.sh" "$counter" 3 > /dev/null 2>&1 || status=$?
  [[ "$status" -eq 0 && "$(cat "$counter")" == 3 ]] || { echo "error: selftest: a third-attempt success should pass after 3 runs" >&2; exit 1; }
  rm "$counter"
  status=0
  "$self" 3 0 "$tmp/flaky.sh" "$counter" 4 > /dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 && "$(cat "$counter")" == 3 ]] || { echo "error: selftest: a command that always fails should fail after exactly 3 attempts" >&2; exit 1; }
  echo "retry selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
[[ $# -ge 3 ]] || { echo "usage: scripts/retry.sh ATTEMPTS PAUSE COMMAND [ARG...]" >&2; exit 64; }
attempts="$1"
pause="$2"
shift 2
for ((attempt = 1; attempt <= attempts; attempt++)); do
  status=0
  "$@" || status=$?
  [[ "$status" -ne 0 ]] || exit 0
  if ((attempt < attempts)); then
    echo "retry: attempt $attempt of $attempts failed (status $status); pausing ${pause}s" >&2
    sleep "$pause"
  fi
done
exit "$status"
