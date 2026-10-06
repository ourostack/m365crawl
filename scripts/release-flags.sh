#!/usr/bin/env bash
#
# Keeps a GitHub release's prerelease and latest flags right. Run by the verify-release and
# contain jobs in release.yml.
#
#   TAG        release tag
#   REPO       owner/name
#   GH_TOKEN   token that can edit this repository's releases
#
#   scripts/release-flags.sh demote   marks the release as a prerelease and not latest, so users
#                                     and brew do not resolve it as the newest. A release that does
#                                     not exist is left alone.
#   scripts/release-flags.sh settle   (the settle job, after every verification job passed): a rehearsal must be a prerelease
#                                     and not latest; a stable release that an earlier flake demoted is
#                                     restored (not prerelease, and latest unless a higher stable release
#                                     exists: versions only move forward, so an old version never becomes
#                                     latest), so the flag check never fails a good release.
#   scripts/release-flags.sh --selftest
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stub is a quoted heredoc.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

# prerelease_flag prints true or false, or nothing when the release does not exist.
prerelease_flag() {
  local out
  if out="$(gh release view "$TAG" -R "$REPO" --json isPrerelease --jq .isPrerelease 2>&1)"; then
    echo "$out"
    return 0
  fi
  grep -Eqi 'not found|404' <<<"$out" && return 0
  fail "could not read the release $TAG: $out"
}

# higher_stable prints a published stable release tag (vX.Y.Z, no hyphen) above $TAG, if any. It reads
# the whole list before deciding, so nothing closes the pipe early.
higher_stable() {
  local mine="${TAG#v}" list top
  list="$(gh api "repos/$REPO/releases" --paginate --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name')" \
    || fail "could not list the releases of $REPO"
  top="$(printf '%s\n' "$list" | { grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' || true; } | { grep -vxF "$TAG" || true; } | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)"
  [[ -n "$top" && "$top" != "$mine" ]] || return 0
  if [[ "$(printf '%s\n%s\n' "$top" "$mine" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)" == "$top" ]]; then
    echo "v$top"
  fi
}

demote() {
  local flag
  flag="$(prerelease_flag)"
  if [[ -z "$flag" ]]; then
    echo "release-flags: no release for $TAG; nothing to demote"
    return 0
  fi
  gh release edit "$TAG" -R "$REPO" --prerelease --latest=false
  echo "release-flags: $TAG is now a prerelease and not latest"
}

settle() {
  local flag latest higher
  flag="$(prerelease_flag)"
  [[ -n "$flag" ]] || fail "there is no release for $TAG"
  if [[ "$TAG" == *-* ]]; then
    [[ "$flag" == true ]] || fail "isPrerelease is $flag for $TAG, expected true"
    latest="$(gh api "repos/$REPO/releases/latest" --jq .tag_name 2>/dev/null || true)"
    [[ "$latest" != "$TAG" ]] || fail "prerelease $TAG is marked latest"
    echo "release-flags: $TAG is a prerelease and not latest"
    return 0
  fi
  if [[ "$flag" == true ]]; then
    higher="$(higher_stable)"
    if [[ -n "$higher" ]]; then
      echo "release-flags: $TAG is marked as a prerelease, but every check passed; restoring it, not as latest, because $higher is a higher stable release"
      gh release edit "$TAG" -R "$REPO" --prerelease=false --latest=false
    else
      echo "release-flags: $TAG is marked as a prerelease, but every check passed; restoring it as the latest release"
      gh release edit "$TAG" -R "$REPO" --prerelease=false --latest
    fi
    flag="$(prerelease_flag)"
    [[ "$flag" == false ]] || fail "could not restore $TAG: isPrerelease is still $flag"
  fi
  echo "release-flags: $TAG is a stable release"
}

selftest() {
  local self stub state out status
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-flags-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  stub="$tmp/bin"
  state="$tmp/state"
  mkdir -p "$stub" "$state"
  cat > "$stub/gh" <<'STUB'
#!/usr/bin/env bash
# Stand-in for gh release view/edit and gh api releases/latest. State lives in $STUB_STATE:
# the file "pre" holds true or false, and is absent when there is no release; "latest" holds the latest tag.
s="$STUB_STATE"
case "$1 $2" in
  "release view")
    [[ -f "$s/pre" ]] || { echo "release not found" >&2; exit 1; }
    cat "$s/pre" ;;
  "release edit")
    tag="$3"
    for arg in "$@"; do
      case "$arg" in
        --prerelease) echo true > "$s/pre" ;;
        --prerelease=false) echo false > "$s/pre" ;;
        --latest=false) [[ "$(cat "$s/latest")" != "$tag" ]] || : > "$s/latest" ;;
        --latest) echo "$tag" > "$s/latest" ;;
      esac
    done ;;
  "api repos/o/r/releases/latest") cat "$s/latest" ;;
  "api repos/o/r/releases") cat "$s/stable" 2>/dev/null || true ;;
  *) echo "stub gh: unexpected: $*" >&2; exit 64 ;;
esac
STUB
  chmod +x "$stub/gh"
  set_state() { # set_state PRERELEASE LATEST [STABLE-TAGS...] ; "" PRERELEASE means no release
    rm -f "$state/pre"
    printf '%s\n' "${@:3}" > "$state/stable"
    [[ -z "$1" ]] || echo "$1" > "$state/pre"
    echo "${2-}" > "$state/latest"
  }
  run() { # run MODE TAG
    status=0
    out="$(env PATH="$stub:$PATH" HOME="$tmp" STUB_STATE="$state" TAG="$2" REPO=o/r GH_TOKEN=t "$self" "$1" 2>&1)" || status=$?
  }

  set_state false v0.2.0
  run demote v0.2.0
  [[ "$status" -eq 0 && "$(cat "$state/pre")" == true && ! -s "$state/latest" ]] || fail "selftest: demote should mark a prerelease and clear latest: $out"
  set_state ""
  run demote v0.2.0
  [[ "$status" -eq 0 ]] && grep -Fq "nothing to demote" <<<"$out" || fail "selftest: demoting a missing release is harmless: $out"

  set_state false v0.2.0
  run settle v0.2.0
  [[ "$status" -eq 0 && "$(cat "$state/latest")" == v0.2.0 ]] || fail "selftest: a good stable release stays as it is: $out"
  set_state true ""
  run settle v0.2.0
  [[ "$status" -eq 0 && "$(cat "$state/pre")" == false && "$(cat "$state/latest")" == v0.2.0 ]] || fail "selftest: a demoted stable release is restored: $out"
  grep -Fq "restoring" <<<"$out" || fail "selftest: the restore should be said: $out"
  # An old version never becomes latest while a higher stable release exists.
  set_state true v0.2.0 v0.2.0 v0.1.0
  run settle v0.1.5
  [[ "$status" -eq 0 && "$(cat "$state/pre")" == false && "$(cat "$state/latest")" == v0.2.0 ]] || fail "selftest: v0.1.5 must be restored without taking latest from v0.2.0: $out"
  grep -Fq "not as latest" <<<"$out" || fail "selftest: the reason should be said: $out"
  set_state true "" v0.1.0
  run settle v0.1.5
  [[ "$status" -eq 0 && "$(cat "$state/latest")" == v0.1.5 ]] || fail "selftest: with no higher stable release the restore makes it latest: $out"
  set_state true "" v0.10.0
  run settle v0.9.0
  [[ "$status" -eq 0 && -z "$(cat "$state/latest")" ]] || fail "selftest: v0.10.0 is higher than v0.9.0: $out"
  # A long list is read in full: no early close of the pipe, and the highest one decides.
  local many i
  many=""
  for i in $(seq 1 400); do many="$many v0.$i.0"; done
  # shellcheck disable=SC2086 # the list is words on purpose
  set_state true "" $many v1.2.4
  run settle v0.5.0
  [[ "$status" -eq 0 && "$(cat "$state/pre")" == false && -z "$(cat "$state/latest")" ]] || fail "selftest: with 401 releases, one higher stable release must stop it taking latest: $out"
  grep -Fq "v1.2.4 is a higher stable release" <<<"$out" || fail "selftest: the highest release should be named: $out"
  # shellcheck disable=SC2086
  set_state true "" $many
  run settle v9.0.0
  [[ "$status" -eq 0 && "$(cat "$state/latest")" == v9.0.0 ]] || fail "selftest: with 400 lower releases it becomes latest: $out"
  set_state ""
  run settle v0.2.0
  [[ "$status" -ne 0 ]] && grep -Fq "no release for v0.2.0" <<<"$out" || fail "selftest: settling a missing release fails: $out"

  set_state true v0.1.0
  run settle v0.3.0-rc.1
  [[ "$status" -eq 0 ]] || fail "selftest: a good rehearsal passes: $out"
  set_state false v0.1.0
  run settle v0.3.0-rc.1
  [[ "$status" -ne 0 && "$(cat "$state/pre")" == false ]] && grep -Fq "expected true" <<<"$out" || fail "selftest: a rehearsal that is not a prerelease fails and is not restored: $out"
  set_state true v0.3.0-rc.1
  run settle v0.3.0-rc.1
  [[ "$status" -ne 0 ]] && grep -Fq "marked latest" <<<"$out" || fail "selftest: a rehearsal marked latest fails: $out"
  echo "release-flags selftest: ok"
}

case "${1:-}" in
  --selftest) selftest ;;
  demote | settle)
    for name in TAG REPO; do
      [[ -n "${!name:-}" ]] || fail "$name is required"
    done
    "$1"
    ;;
  *) fail "usage: scripts/release-flags.sh demote | settle | --selftest" ;;
esac
