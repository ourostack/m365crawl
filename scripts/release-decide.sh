#!/usr/bin/env bash
#
# Decides whether a push to main is a release, and which one. Run by the decide job
# in release.yml. Merging a release-notes file docs/releases/vX.Y.Z.md (or
# vX.Y.Z-rc.N.md) to main is the release: the version comes from the file name that
# the push added.
#
#   BEFORE, AFTER   the commits the push moved main between (github.event.before, github.sha)
#   REPO            owner/name (GITHUB_REPOSITORY)
#   GH_TOKEN        token that can read this repository's tags and releases
#   GITHUB_OUTPUT   where the outputs go (set by Actions)
#
# Outputs: release (true or false), tag, version, sha, rehearsal (true for a tag with a hyphen).
# A push that adds no notes file (for example an edit to an existing one) is not a
# release: release=false and exit 0. Anything that looks like a release but is wrong
# fails with the reason: more than one notes file added, a tag or GitHub release that
# already exists, a version that is not valid semantic versioning, or (stable only) a
# CHANGELOG.md with no section for the version.
#
#   scripts/release-decide.sh --selftest   runs every decision against a throwaway repository
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stubs are quoted heredocs.
set -euo pipefail

fail() {
  if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
    echo "::error title=Release refused::$*"
  fi
  echo "error: release refused: $*" >&2
  exit 1
}

# https://semver.org, with the optional leading v of a tag. Build metadata is not allowed in a tag.
semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?$'

# gh exits non-zero for both "absent" and "could not ask"; only "not found" means absent.
exists() { # exists KIND NAME -- KIND is tag or release
  local out
  if [[ "$1" == tag ]]; then
    out="$(gh api "repos/$REPO/git/ref/tags/$2" 2>&1)" && return 0
  else
    out="$(gh release view "$2" -R "$REPO" --json tagName 2>&1)" && return 0
  fi
  grep -Eqi 'not found|404' <<<"$out" && return 1
  fail "could not check whether the $1 $2 exists: $out"
}

emit() {
  echo "$1=$2" >> "$GITHUB_OUTPUT"
}

decide() {
  local name
  for name in AFTER REPO GITHUB_OUTPUT; do
    [[ -n "${!name:-}" ]] || fail "$name is required"
  done
  local before="${BEFORE:-}" added files count file base version tag

  if [[ -n "$before" && ! "$before" =~ ^0+$ ]] && git cat-file -e "${before}^{commit}" 2>/dev/null; then
    added="$(git diff --no-renames --name-only --diff-filter=A "$before" "$AFTER")"
  else
    # A new branch or a rewritten history has no usable base: look at the pushed commit alone.
    added="$(git diff-tree --root --no-commit-id --no-renames -r --name-only --diff-filter=A "$AFTER")"
  fi
  files="$(printf '%s\n' "$added" | grep -E '^docs/releases/v[^/]+\.md$' || true)"
  count="$(printf '%s' "$files" | grep -c . || true)"

  if [[ "$count" -eq 0 ]]; then
    echo "No release-notes file was added by this push: not a release."
    emit release false
    return 0
  fi
  [[ "$count" -eq 1 ]] || fail "$count release-notes files were added in one push ($(printf '%s' "$files" | tr '\n' ' ')); a push releases exactly one version"

  file="$files"
  base="${file##*/}"
  tag="${base%.md}"
  version="${tag#v}"
  [[ "$version" =~ $semver_re ]] || fail "$file does not name a valid semantic version ($version); use docs/releases/vX.Y.Z.md or vX.Y.Z-rc.N.md"

  if exists tag "$tag"; then
    fail "the tag $tag already exists; a version is released once, so add notes for the next version"
  fi
  if exists release "$tag"; then
    fail "a GitHub release for $tag already exists; a version is released once, so add notes for the next version"
  fi

  if [[ "$tag" != *-* ]]; then
    [[ -f CHANGELOG.md ]] || fail "CHANGELOG.md not found; a stable release needs a section for $version"
    awk -v h="## [$version]" 'index($0, h) == 1 { found = 1 } END { exit !found }' CHANGELOG.md \
      || fail "CHANGELOG.md has no section for $version (expected a line starting '## [$version]')"
  fi

  echo "Release $tag from $AFTER (notes: $file)"
  emit release true
  emit tag "$tag"
  emit version "$version"
  emit sha "$AFTER"
  if [[ "$tag" == *-* ]]; then emit rehearsal true; else emit rehearsal false; fi
}

selftest() {
  local self repo stub out status c0 c1 c2 c3 c4 c5 c6 c7 c8 c9 c10 c11 outputs
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-decide-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  repo="$tmp/repo"
  stub="$tmp/bin"
  outputs="$tmp/output"
  mkdir -p "$stub" "$repo/docs/releases"
  cat > "$stub/gh" <<'STUB'
#!/usr/bin/env bash
# Stand-in for gh api repos/R/git/ref/tags/TAG and gh release view TAG. STUB_TAGS and
# STUB_RELEASES list the names that exist.
case "$1" in
  api) name="${2##*/}"; list=" ${STUB_TAGS:-} " ;;
  release) name="$3"; list=" ${STUB_RELEASES:-} " ;;
  *) echo "stub gh: unexpected: $*" >&2; exit 64 ;;
esac
if [[ "$list" == *" $name "* ]]; then echo '{}'; exit 0; fi
echo "gh: Not Found (HTTP 404)" >&2
exit 1
STUB
  chmod +x "$stub/gh"

  git -C "$repo" init --quiet --initial-branch=main
  git -C "$repo" config user.name t
  git -C "$repo" config user.email t@example.com
  commit() { # commit MESSAGE FILE... : writes each FILE (its path as content) and commits
    local message="$1" f
    shift
    for f in "$@"; do
      mkdir -p "$repo/$(dirname "$f")"
      printf '%s\n' "$message" >> "$repo/$f"
    done
    git -C "$repo" add -A
    git -C "$repo" commit --quiet -m "$message"
    git -C "$repo" rev-parse HEAD
  }
  printf '# Changelog\n\n## [Unreleased]\n\n## [0.2.0] - 2026-10-05\n\n## [0.10.0] - 2026-10-06\n' > "$repo/CHANGELOG.md"
  c0="$(commit base README.md)"

  decide_run() { # decide_run BEFORE AFTER [VAR=value ...]; sets out, status
    local b="$1" a="$2"
    shift 2
    : > "$outputs"
    status=0
    out="$(cd "$repo" && env PATH="$stub:$PATH" HOME="$tmp" BEFORE="$b" AFTER="$a" REPO=o/r GH_TOKEN=t GITHUB_OUTPUT="$outputs" "$@" "$self" 2>&1)" || status=$?
  }
  expect_ok() { # expect_ok DESCRIPTION OUTPUT-LINE...
    local what="$1" line
    shift
    [[ "$status" -eq 0 ]] || fail "selftest: $what should succeed: $out"
    for line in "$@"; do
      grep -Fxq -- "$line" "$outputs" || fail "selftest: $what: output '$line' missing from: $(tr '\n' ' ' < "$outputs")"
    done
  }
  expect_fail() { # expect_fail DESCRIPTION SUBSTRING
    [[ "$status" -ne 0 ]] || fail "selftest: $1 should fail"
    grep -Fq -- "$2" <<<"$out" || fail "selftest: $1: output missing '$2': $out"
    ! grep -Fxq 'release=true' "$outputs" || fail "selftest: $1 must not output release=true"
  }

  c1="$(commit n docs/releases/v0.2.0.md)"
  decide_run "$c0" "$c1"
  expect_ok "stable" release=true tag=v0.2.0 version=0.2.0 "sha=$c1" rehearsal=false

  c2="$(commit n docs/releases/v0.3.0-rc.1.md)"
  decide_run "$c1" "$c2"
  expect_ok "rehearsal needs no changelog section" release=true tag=v0.3.0-rc.1 version=0.3.0-rc.1 rehearsal=true

  decide_run "0000000000000000000000000000000000000000" "$c1"
  expect_ok "first push of a branch" release=true tag=v0.2.0

  decide_run "" "$c1"
  expect_ok "no base commit" release=true tag=v0.2.0

  c3="$(commit n docs/releases/v0.4.0-rc.1.md docs/releases/v0.4.0-rc.2.md)"
  decide_run "$c2" "$c3"
  expect_fail "two notes files" "2 release-notes files were added"

  c4="$(commit n docs/releases/v0.5.0-rc.1.md)"
  decide_run "$c3" "$c4" STUB_TAGS="v0.5.0-rc.1"
  expect_fail "existing tag" "the tag v0.5.0-rc.1 already exists"

  decide_run "$c3" "$c4" STUB_RELEASES="v0.5.0-rc.1"
  expect_fail "existing release" "a GitHub release for v0.5.0-rc.1 already exists"

  c5="$(commit n docs/releases/v1.2.md)"
  decide_run "$c4" "$c5"
  expect_fail "two-part version" "does not name a valid semantic version"
  c6="$(commit n docs/releases/v01.2.3.md)"
  decide_run "$c5" "$c6"
  expect_fail "leading zero" "does not name a valid semantic version"
  c7="$(commit n docs/releases/v1.2.3+build.md)"
  decide_run "$c6" "$c7"
  expect_fail "build metadata" "does not name a valid semantic version"

  c8="$(commit n docs/releases/v0.9.0.md)"
  decide_run "$c7" "$c8"
  expect_fail "stable without a changelog section" "CHANGELOG.md has no section for 0.9.0"

  # A version that only prefixes a changelog heading does not count.
  c9="$(commit n docs/releases/v0.1.0.md)"
  decide_run "$c8" "$c9"
  expect_fail "changelog prefix" "CHANGELOG.md has no section for 0.1.0"

  c10="$(commit edit docs/releases/v0.2.0.md)"
  decide_run "$c9" "$c10"
  expect_ok "an edit to an existing notes file is not a release" release=false
  ! grep -Fxq 'release=true' "$outputs" || fail "selftest: an edit must not release"

  c11="$(commit n docs/releases/README.md docs/other.md)"
  decide_run "$c10" "$c11"
  expect_ok "unrelated added files are not a release" release=false

  decide_run "$c0" "$c1" GH_TOKEN=t REPO=
  [[ "$status" -ne 0 ]] && grep -Fq "REPO is required" <<<"$out" || fail "selftest: a missing input should be named: $out"

  echo "release-decide selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
decide
