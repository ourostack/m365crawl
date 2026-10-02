#!/usr/bin/env bash
#
# Verifies the Homebrew install of a stable release. Run by the verify-homebrew job
# in release.yml on macos-latest, after verify-release.
#
#   TAG                  release tag, e.g. v0.1.0
#   APPLE_TEAM_ID        expected signing team
#   POLL_TIMEOUT_MINUTES how long to wait for the tap (default 75)
#
# The tap's update-casks workflow polls hourly and cannot be triggered from this
# repository, so the script polls `brew info` until the tap serves this version,
# then installs the cask and runs the same gates as verify-release on the
# installed binary.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fail() {
  echo "error: $*" >&2
  exit 1
}
for name in TAG APPLE_TEAM_ID; do
  [[ -n "${!name:-}" ]] || fail "$name is required"
done
[[ "$TAG" != *-* ]] || fail "$TAG is a prerelease; prereleases never reach the tap"
version="${TAG#v}"
cask="ourostack/tap/teamscrawl"

brew tap ourostack/tap
deadline=$((SECONDS + ${POLL_TIMEOUT_MINUTES:-75} * 60))
served=""
while :; do
  brew update --quiet || true
  served="$(brew info --cask --json=v2 "$cask" | python3 -c 'import json,sys; print(json.load(sys.stdin)["casks"][0]["version"])')"
  [[ "$served" == "$version" ]] && break
  [[ $SECONDS -lt $deadline ]] || fail "the tap still serves '$served' after ${POLL_TIMEOUT_MINUTES:-75} minutes; expected '$version'"
  echo "tap serves '$served', waiting for '$version'"
  sleep 120
done

brew install --cask "$cask"
bin="$(realpath "$(brew --prefix)/bin/teamscrawl")"
got="$("$bin" --json version | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')"
[[ "$got" == "$version" ]] || fail "installed version is '$got', expected '$version'"
"$here/sign-notarize.sh" --verify "$bin"
echo "verify-homebrew: $cask $version installed and verified"
