#!/usr/bin/env bash
#
# Checks that the tap deploy key still authenticates: git ls-remote against the tap
# over SSH with the key, verifying the server against the host keys pinned in
# scripts/github_known_hosts. Run by credential-health.yml. It reads access only; it never
# pushes. The key is written to a private temporary file and never printed.
#
#   HOMEBREW_TAP_DEPLOY_KEY  private half of the tap's deploy key
#   TAP_URL                  git URL of the tap (default git@github.com:ourostack/homebrew-tap.git)
#
#   scripts/check-tap-key.sh --selftest   runs against a local bare repository
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stubs are quoted heredocs.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

check() {
  [[ -n "${HOMEBREW_TAP_DEPLOY_KEY:-}" ]] || fail "HOMEBREW_TAP_DEPLOY_KEY is required"
  local tap="${TAP_URL:-git@github.com:ourostack/homebrew-tap.git}" here
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  work="$(mktemp -d)"
  trap 'rm -r "${work:?}"' EXIT
  (umask 077 && printf '%s\n' "$HOMEBREW_TAP_DEPLOY_KEY" > "$work/key")
  export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=$here/github_known_hosts"
  git ls-remote --exit-code "$tap" HEAD > /dev/null || fail "the tap deploy key no longer authenticates to $tap (was it removed from the tap, or rotated without updating HOMEBREW_TAP_DEPLOY_KEY?)"
  echo "check-tap-key: the deploy key authenticates to $tap"
}

selftest() {
  local self out status
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/check-tap-key-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  git init --quiet --bare --initial-branch=main "$tmp/tap.git"
  git init --quiet --initial-branch=main "$tmp/seed"
  git -C "$tmp/seed" -c user.name=t -c user.email=t@example.com commit --quiet --allow-empty -m seed
  git -C "$tmp/seed" push --quiet "file://$tmp/tap.git" main
  run() { # run TAP_URL [KEY]
    status=0
    out="$(env PATH="$PATH" HOME="$tmp" TAP_URL="$1" HOMEBREW_TAP_DEPLOY_KEY="${2-SECRET-KEY-VALUE}" "$self" 2>&1)" || status=$?
  }
  run "file://$tmp/tap.git"
  [[ "$status" -eq 0 ]] || fail "selftest: a reachable tap should pass: $out"
  run "file://$tmp/missing.git"
  [[ "$status" -ne 0 ]] && grep -Fq "no longer authenticates" <<<"$out" || fail "selftest: an unreachable tap should fail: $out"
  ! grep -Fq "SECRET-KEY-VALUE" <<<"$out" || fail "selftest: the key was printed"
  run "file://$tmp/tap.git" ""
  [[ "$status" -ne 0 ]] && grep -Fq "HOMEBREW_TAP_DEPLOY_KEY is required" <<<"$out" || fail "selftest: a missing key should be named: $out"
  echo "check-tap-key selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
check
