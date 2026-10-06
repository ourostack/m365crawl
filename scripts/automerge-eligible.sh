#!/usr/bin/env bash
#
# Decides whether a Dependabot pull request may auto-merge. Run by dependabot-automerge.yml.
# Only Go module updates that are all semantic-version patch or minor qualify. GitHub Actions
# updates never do: a moved upstream action tag could otherwise reach the job that holds the
# Apple signing secrets without a human reading it. Majors stay open pull requests.
#
#   HEAD_REF   the pull request's branch (dependabot/go_modules/... for Go modules)
#   UPDATED    dependabot/fetch-metadata's updated-dependencies-json output
#
# Exit 0 and print "eligible" when it may auto-merge; exit 1 and print the reason when not.
#
#   scripts/automerge-eligible.sh --selftest
set -euo pipefail

decide() {
  [[ "${HEAD_REF:-}" == dependabot/go_modules/* ]] || { echo "not eligible: only Go module updates auto-merge (branch ${HEAD_REF:-unset})"; return 1; }
  [[ -n "${UPDATED:-}" ]] || { echo "not eligible: no dependency metadata"; return 1; }
  if jq -e 'type == "array" and length > 0 and all(.[]; .updateType == "version-update:semver-patch" or .updateType == "version-update:semver-minor")' <<<"$UPDATED" > /dev/null 2>&1; then
    echo "eligible"
    return 0
  fi
  echo "not eligible: every update must be a semver patch or minor"
  return 1
}

selftest() {
  local patch minor major
  patch='{"dependencyName":"a","updateType":"version-update:semver-patch"}'
  minor='{"dependencyName":"b","updateType":"version-update:semver-minor"}'
  major='{"dependencyName":"c","updateType":"version-update:semver-major"}'
  check() { # check EXPECTED-STATUS HEAD_REF UPDATED
    local status=0
    HEAD_REF="$2" UPDATED="$3" decide > /dev/null || status=$?
    [[ "$status" -eq "$1" ]] || { echo "error: selftest: HEAD_REF=$2 UPDATED=$3 gave $status, expected $1" >&2; exit 1; }
  }
  check 0 dependabot/go_modules/go-modules-abc "[$patch]"
  check 0 dependabot/go_modules/go-modules-abc "[$patch,$minor]"
  check 1 dependabot/go_modules/go-modules-abc "[$patch,$major]"
  check 1 dependabot/go_modules/go-modules-abc "[$major]"
  check 1 dependabot/go_modules/go-modules-abc "[]"
  check 1 dependabot/go_modules/go-modules-abc "not json"
  check 1 dependabot/go_modules/go-modules-abc ""
  check 1 dependabot/github_actions/actions-abc "[$patch]"
  check 1 "" "[$patch]"
  echo "automerge-eligible selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
decide
