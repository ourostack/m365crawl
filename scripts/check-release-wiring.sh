#!/usr/bin/env bash
#
# Checks that every job in a workflow that depends, directly or through other jobs, on a job that
# can be skipped on purpose states its own `if:` with `!cancelled()` or `always()`. GitHub skips a
# job that has no `if:` whenever any job upstream of it was skipped, so a publish-only resume
# (which skips `release`) would otherwise silently skip settle and the tap publish (issue 78).
#
#   scripts/check-release-wiring.sh [WORKFLOW] [SKIPPABLE_JOB]   (defaults: .github/workflows/release.yml release)
#   scripts/check-release-wiring.sh --selftest
set -euo pipefail

# jobs FILE prints "job|needs (space separated)|if" for each job, reading the plain layout this repo
# uses: jobs at two spaces of indent, `needs:` as a name or a one-line [list], `if:` on one line.
jobs() {
  awk '
    /^jobs:/ { injobs = 1; next }
    injobs && /^[^ #]/ { injobs = 0 }
    !injobs { next }
    /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { if (job != "") print job "|" needs "|" cond; job = $1; sub(/:$/, "", job); needs = ""; cond = ""; next }
    /^    needs:/ { v = $0; sub(/^    needs:[[:space:]]*/, "", v); gsub(/[][,]/, " ", v); gsub(/[[:space:]]+/, " ", v); sub(/^ /, "", v); sub(/ $/, "", v); needs = v; next }
    /^    if:/ { v = $0; sub(/^    if:[[:space:]]*/, "", v); cond = v; next }
    END { if (job != "") print job "|" needs "|" cond }
  ' "$1"
}

check() {
  local file="$1" skippable="$2" table changed=1 line job needs cond n bad=0
  table="$(jobs "$file")"
  [[ -n "$table" ]] || { echo "error: no jobs found in $file" >&2; return 1; }
  grep -q "^$skippable|" <<<"$table" || { echo "error: no job $skippable in $file" >&2; return 1; }
  # downstream: the skippable job plus everything that needs it, to a fixed point.
  local downstream=" $skippable "
  while [[ "$changed" == 1 ]]; do
    changed=0
    while IFS='|' read -r job needs cond; do
      [[ "$downstream" == *" $job "* ]] && continue
      for n in $needs; do
        if [[ "$downstream" == *" $n "* ]]; then downstream+="$job "; changed=1; break; fi
      done
    done <<<"$table"
  done
  while IFS='|' read -r job needs cond; do
    [[ "$job" == "$skippable" || "$downstream" != *" $job "* ]] && continue
    if [[ "$cond" != *'!cancelled()'* && "$cond" != *'always()'* ]]; then
      echo "error: job $job runs after $skippable but its if: does not start from !cancelled() or always(), so it is skipped whenever $skippable is" >&2
      bad=1
    fi
  done <<<"$table"
  return "$bad"
}

selftest() {
  local self
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/wiring-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  cat > "$tmp/good.yml" <<'YML'
on: push
jobs:
  decide:
    runs-on: ubuntu-latest
  release:
    needs: decide
    if: ${{ false }}
  verify:
    needs: [decide, release]
    if: ${{ !cancelled() && needs.release.result != 'failure' }}
  publish:
    needs: [decide, verify]
    if: ${{ !cancelled() && needs.verify.result == 'success' }}
  report:
    if: ${{ always() }}
    needs: [publish]
  unrelated:
    needs: decide
YML
  "$self" "$tmp/good.yml" release > /dev/null 2>&1 || { echo "error: selftest: a well-wired workflow should pass" >&2; exit 1; }
  sed 's/^    if: \${{ !cancelled() && needs.verify.result == .success. }}$//' "$tmp/good.yml" > "$tmp/bad.yml"
  if "$self" "$tmp/bad.yml" release > /dev/null 2>&1; then echo "error: selftest: a transitive job with no if: should fail" >&2; exit 1; fi
  sed "s/!cancelled() \&\& needs.verify.result == 'success'/needs.verify.result == 'success'/" "$tmp/good.yml" > "$tmp/bad2.yml"
  if "$self" "$tmp/bad2.yml" release > /dev/null 2>&1; then echo "error: selftest: an if: without !cancelled() should fail" >&2; exit 1; fi
  if "$self" "$tmp/good.yml" nosuchjob > /dev/null 2>&1; then echo "error: selftest: an unknown job should fail" >&2; exit 1; fi
  "$self" "$(dirname "$self")/../.github/workflows/release.yml" release > /dev/null || { echo "error: selftest: the real release workflow is miswired" >&2; exit 1; }
  echo "check-release-wiring selftest: ok"
}

case "${1:-}" in
  --selftest) selftest ;;
  *) check "${1:-.github/workflows/release.yml}" "${2:-release}" ;;
esac
