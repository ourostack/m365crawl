#!/usr/bin/env bash
#
# Reports a failed workflow run as a GitHub issue in this repository: opens one, or
# comments on the open issue with the same title. Run by the report-failure job of
# release.yml and credential-health.yml.
#
#   WORKFLOW   workflow name, e.g. Release
#   VERSION    version being released, or empty when there is none
#   RUN_URL    link to the failed run
#   REPO       owner/name (GITHUB_REPOSITORY)
#   GH_TOKEN   token with issues: write
#
#   scripts/report-failure.sh --selftest   runs both paths with a stand-in gh
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stubs are quoted heredocs.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

report() {
  local name
  for name in WORKFLOW RUN_URL REPO GH_TOKEN; do
    [[ -n "${!name:-}" ]] || fail "$name is required"
  done
  local title="Workflow failed: $WORKFLOW" version="${VERSION:-}" body number
  [[ -n "$version" ]] || version="none (no version was derived)"
  body="$(printf 'Workflow: %s\nVersion: %s\nRun: %s\n' "$WORKFLOW" "$version" "$RUN_URL")"
  number="$(gh issue list -R "$REPO" --state open --search "\"$title\" in:title" --json number,title \
    --jq ".[] | select(.title == \"$title\") | .number" | head -n 1)"
  if [[ -n "$number" ]]; then
    gh issue comment "$number" -R "$REPO" --body "$body"
    echo "report-failure: commented on issue #$number"
  else
    gh issue create -R "$REPO" --title "$title" --body "$body"
    echo "report-failure: opened a new issue"
  fi
}

selftest() {
  local self stub log out status
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/report-failure-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  stub="$tmp/bin"
  log="$tmp/calls"
  mkdir -p "$stub"
  cat > "$stub/gh" <<'STUB'
#!/usr/bin/env bash
# Stand-in for gh issue list/create/comment. STUB_OPEN is the number of an open issue with the title, if any.
printf '%s\n' "$*" >> "$STUB_LOG"
if [[ "$1 $2" == "issue list" ]]; then
  [[ -z "${STUB_OPEN:-}" ]] || echo "$STUB_OPEN"
fi
STUB
  chmod +x "$stub/gh"
  run() {
    : > "$log"
    status=0
    out="$(env PATH="$stub:$PATH" HOME="$tmp" STUB_LOG="$log" WORKFLOW=Release VERSION=v0.2.0 RUN_URL=https://example.test/run/1 REPO=o/r GH_TOKEN=t "$@" "$self" 2>&1)" || status=$?
  }
  run
  [[ "$status" -eq 0 ]] || fail "selftest: new issue failed: $out"
  grep -Fq 'issue create -R o/r --title Workflow failed: Release' "$log" || fail "selftest: no issue created: $(cat "$log")"
  grep -Fq 'Version: v0.2.0' "$log" && grep -Fq 'Run: https://example.test/run/1' "$log" || fail "selftest: body lacks version or run link"
  run STUB_OPEN=7
  [[ "$status" -eq 0 ]] || fail "selftest: comment failed: $out"
  grep -Fq 'issue comment 7 -R o/r' "$log" || fail "selftest: no comment on the open issue: $(cat "$log")"
  ! grep -Fq 'issue create' "$log" || fail "selftest: opened a duplicate issue"
  run VERSION=
  grep -Fq 'none (no version was derived)' "$log" || fail "selftest: an empty version should be stated"
  run RUN_URL=
  [[ "$status" -ne 0 ]] && grep -Fq 'RUN_URL is required' <<<"$out" || fail "selftest: a missing input should be named: $out"
  echo "report-failure selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
report
