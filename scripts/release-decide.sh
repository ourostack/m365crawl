#!/usr/bin/env bash
#
# Decides what to release, from the state of main. Run by the decide job in release.yml.
# Merging a release-notes file docs/releases/vX.Y.Z.md (or vX.Y.Z-rc.N.md) to main is the
# release: the version comes from the file name.
#
#   AFTER           the commit of main being evaluated (github.sha)
#   REPO            owner/name (GITHUB_REPOSITORY)
#   REF             the ref the run started on (github.ref); must be refs/heads/main when set
#   GH_TOKEN        token that can read this repository's tags and releases
#   GITHUB_OUTPUT   where the outputs go (set by Actions)
#
# The candidates are every docs/releases/v*.md in AFTER's tree whose release is not done:
# its tag does not exist, or the tag exists at a commit of main that holds the notes file but
# no GitHub release exists (a release that stopped half way: it resumes at the tag's commit).
# No candidate: release=false and exit 0. Otherwise the lowest version in semantic-version
# order is released, and remaining=true says more candidates wait (the workflow starts itself
# again for them). Because the decision reads state, not the push, a run that was cancelled or
# refused is retried by any later run: a fix-up merge, a changelog fix or a manual start.
#
# Outputs: release (true or false), tag, version, sha (the commit to release), rehearsal (true
# for a version with a hyphen), resume (true when the tag already exists), remaining.
#
# Versions only move forward: a candidate whose version is not greater than the highest stable
# version already released is refused (a late notes file for an old version, or a rehearsal for
# a version at or below the latest stable), because it would become "latest" and move the
# Homebrew cask backwards. Tags may be lightweight or annotated; an annotated tag is read through
# to its commit, and a notes file whose tag and release both exist is skipped without looking at the tag.
#
# It refuses, with the reason, when: a notes file name is not valid semantic versioning or not
# a valid tag name; a GitHub release exists with no tag; a tag with no release exists at a
# commit that is not on main or lacks the notes file; or (stable only) CHANGELOG.md has no
# section for the version.
#
#   scripts/release-decide.sh --selftest   runs every decision against a throwaway repository
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stubs are quoted heredocs.
set -euo pipefail
export LC_ALL=C

fail() {
  if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
    echo "::error title=Release refused::$*"
  fi
  echo "error: release refused: $*" >&2
  exit 1
}

# https://semver.org, without the leading v of a tag. Build metadata is not allowed in a tag.
semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-((0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?$'

# semver_cmp A B prints -1, 0 or 1 by semantic-version precedence (valid versions only).
semver_cmp() {
  local a="$1" b="$2" ac bc ap="" bp="" i x y
  local -a ai bi
  ac="${a%%-*}"
  bc="${b%%-*}"
  [[ "$a" != *-* ]] || ap="${a#*-}"
  [[ "$b" != *-* ]] || bp="${b#*-}"
  IFS=. read -r -a ai <<<"$ac"
  IFS=. read -r -a bi <<<"$bc"
  for i in 0 1 2; do
    if ((ai[i] < bi[i])); then echo -1; return; fi
    if ((ai[i] > bi[i])); then echo 1; return; fi
  done
  if [[ -z "$ap" && -z "$bp" ]]; then echo 0; return; fi
  if [[ -z "$ap" ]]; then echo 1; return; fi
  if [[ -z "$bp" ]]; then echo -1; return; fi
  IFS=. read -r -a ai <<<"$ap"
  IFS=. read -r -a bi <<<"$bp"
  i=0
  while :; do
    if ((i >= ${#ai[@]} && i >= ${#bi[@]})); then echo 0; return; fi
    if ((i >= ${#ai[@]})); then echo -1; return; fi
    if ((i >= ${#bi[@]})); then echo 1; return; fi
    x="${ai[i]}"
    y="${bi[i]}"
    if [[ "$x" =~ ^[0-9]+$ && "$y" =~ ^[0-9]+$ ]]; then
      if ((x < y)); then echo -1; return; fi
      if ((x > y)); then echo 1; return; fi
    elif [[ "$x" =~ ^[0-9]+$ ]]; then
      echo -1; return
    elif [[ "$y" =~ ^[0-9]+$ ]]; then
      echo 1; return
    elif [[ "$x" < "$y" ]]; then
      echo -1; return
    elif [[ "$x" > "$y" ]]; then
      echo 1; return
    fi
    i=$((i + 1))
  done
}

# tag_info TAG prints "TYPE OBJECT" (commit or tag, and its object id), or nothing when the
# tag does not exist. gh exits non-zero for both "absent" and "could not ask"; only "not found"
# means absent.
tag_info() {
  local out
  if out="$(gh api "repos/$REPO/git/ref/tags/$1" --jq '.object.type + " " + .object.sha' 2>&1)"; then
    echo "$out"
    return 0
  fi
  grep -Eqi 'not found|404' <<<"$out" && return 0
  fail "could not check whether the tag $1 exists: $out"
}

release_exists() {
  local out
  out="$(gh release view "$1" -R "$REPO" --json tagName 2>&1)" && return 0
  grep -Eqi 'not found|404' <<<"$out" && return 1
  fail "could not check whether the release $1 exists: $out"
}

emit() {
  echo "$1=$2" >> "$GITHUB_OUTPUT"
}

decide() {
  local name
  for name in AFTER REPO GITHUB_OUTPUT; do
    [[ -n "${!name:-}" ]] || fail "$name is required"
  done
  [[ -z "${REF:-}" || "$REF" == refs/heads/main ]] || fail "releases run from main only; this run started on $REF"
  git cat-file -e "${AFTER}^{commit}" 2>/dev/null || fail "the commit $AFTER is not in the checkout (fetch full history)"

  local path base tag version tsha info highest="" candidates="" line best="" bestline="" count=0 file sha resume
  # -z: names come back raw, so a quoted or non-ASCII name is judged like any other.
  while IFS= read -r -d '' path; do
    [[ "$path" =~ ^docs/releases/v[^/]+\.md$ ]] || continue
    base="${path##*/}"
    tag="${base%.md}"
    version="${tag#v}"
    [[ "$version" =~ $semver_re ]] || fail "$path does not name a valid semantic version ($version); use docs/releases/vX.Y.Z.md or vX.Y.Z-rc.N.md"
    git check-ref-format "refs/tags/$tag" || fail "$path does not name a valid git tag ($tag)"
    if release_exists "$tag"; then
      [[ -n "$(tag_info "$tag")" ]] || fail "a GitHub release for $tag exists but the tag does not; delete the release and run the Release workflow again"
      # Released: skipped without looking at what kind of tag it is.
      if [[ "$tag" != *-* && ( -z "$highest" || "$(semver_cmp "$version" "$highest")" == 1 ) ]]; then highest="$version"; fi
      continue
    fi
    info="$(tag_info "$tag")"
    if [[ -n "$info" ]]; then
      # The tag exists with no release: resume at the tag's commit if it is a commit of main that holds the notes.
      tsha="$(git rev-parse --verify --quiet "${info#* }^{commit}" 2>/dev/null || true)"
      if [[ -n "$tsha" ]] \
        && git merge-base --is-ancestor "$tsha" "$AFTER" \
        && git cat-file -e "${tsha}:${path}" 2>/dev/null; then
        candidates+="$version|$tag|$path|$tsha|true"$'\n'
      else
        fail "the tag $tag exists at ${info#* }, which is not a commit of main that holds $path, and no GitHub release exists for it; delete the tag (git push origin :refs/tags/$tag) and run the Release workflow again, or release a newer version"
      fi
    else
      candidates+="$version|$tag|$path|$AFTER|false"$'\n'
    fi
  done < <(git ls-tree -r -z --name-only "$AFTER" -- docs/releases)

  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    if [[ -n "$highest" && "$(semver_cmp "${line%%|*}" "$highest")" != 1 ]]; then
      fail "${line%%|*} (docs/releases/v${line%%|*}.md) is not greater than $highest, the highest stable version already released; versions only move forward. Delete that notes file, or release a version above $highest"
    fi
    count=$((count + 1))
    if [[ -z "$best" || "$(semver_cmp "${line%%|*}" "$best")" == -1 ]]; then
      best="${line%%|*}"
      bestline="$line"
    fi
  done <<<"$candidates"

  if [[ "$count" -eq 0 ]]; then
    echo "Every release-notes file on main already has its release: nothing to release."
    emit release false
    return 0
  fi

  IFS='|' read -r version tag file sha resume <<<"$bestline"
  if [[ "$tag" != *-* && "$resume" == false ]]; then
    git show "${sha}:CHANGELOG.md" 2>/dev/null | awk -v h="## [$version]" 'index($0, h) == 1 { found = 1 } END { exit !found }' \
      || fail "CHANGELOG.md has no section for $version (expected a line starting '## [$version]'); add it and merge again"
  fi

  echo "Release $tag from $sha (notes: $file; resume: $resume; $((count - 1)) more waiting)"
  emit release true
  emit tag "$tag"
  emit version "$version"
  emit sha "$sha"
  emit resume "$resume"
  if [[ "$tag" == *-* ]]; then emit rehearsal true; else emit rehearsal false; fi
  if [[ "$count" -gt 1 ]]; then emit remaining true; else emit remaining false; fi
}

selftest() {
  local self repo stub out status c0 c1 c2 c3 c4 outputs sha_c1 annotated
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-decide-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  repo="$tmp/repo"
  stub="$tmp/bin"
  outputs="$tmp/output"
  mkdir -p "$stub" "$repo/docs/releases"
  cat > "$stub/gh" <<'STUB'
#!/usr/bin/env bash
# Stand-in for gh api repos/R/git/ref/tags/TAG --jq ... and gh release view TAG. STUB_TAGS lists
# name:object[:type] triples (type commit by default) and STUB_RELEASES lists names that exist.
case "$1" in
  api)
    name="${2##*/}"
    for pair in ${STUB_TAGS:-}; do
      if [[ "${pair%%:*}" == "$name" ]]; then
        rest="${pair#*:}"
        type=commit
        [[ "$rest" != *:* ]] || type="${rest#*:}"
        echo "$type ${rest%%:*}"
        exit 0
      fi
    done
    ;;
  release)
    for name in ${STUB_RELEASES:-}; do
      if [[ "$name" == "$3" ]]; then echo '{}'; exit 0; fi
    done
    ;;
  *) echo "stub gh: unexpected: $*" >&2; exit 64 ;;
esac
echo "gh: Not Found (HTTP 404)" >&2
exit 1
STUB
  chmod +x "$stub/gh"

  git -C "$repo" init --quiet --initial-branch=main
  git -C "$repo" config user.name t
  git -C "$repo" config user.email t@example.com
  git -C "$repo" config core.quotePath true
  commit() { # commit MESSAGE FILE... : appends to each FILE and commits; prints the commit
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

  decide_run() { # decide_run AFTER [VAR=value ...]; sets out, status
    local a="$1"
    shift
    : > "$outputs"
    status=0
    out="$(cd "$repo" && env PATH="$stub:$PATH" HOME="$tmp" AFTER="$a" REPO=o/r GH_TOKEN=t GITHUB_OUTPUT="$outputs" "$@" "$self" 2>&1)" || status=$?
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

  # One stable notes file, no tag yet.
  c1="$(commit n docs/releases/v0.2.0.md)"
  decide_run "$c1"
  expect_ok "stable" release=true tag=v0.2.0 version=0.2.0 "sha=$c1" rehearsal=false resume=false remaining=false

  # Tag and release both exist: nothing to do.
  decide_run "$c1" STUB_TAGS="v0.2.0:$c1" STUB_RELEASES="v0.2.0"
  expect_ok "already released" release=false

  # Add plus edit in one push, and two untagged notes: the lowest version goes first and the rest wait.
  c2="$(commit n docs/releases/v0.3.0-rc.1.md docs/releases/v0.2.0.md)"
  decide_run "$c2"
  expect_ok "several untagged" release=true tag=v0.2.0 remaining=true
  decide_run "$c2" STUB_TAGS="v0.2.0:$c1" STUB_RELEASES="v0.2.0"
  expect_ok "rehearsal needs no changelog section" release=true tag=v0.3.0-rc.1 version=0.3.0-rc.1 rehearsal=true remaining=false

  # A tag with no release resumes at the tag's commit.
  decide_run "$c2" STUB_TAGS="v0.2.0:$c1"
  expect_ok "resume" release=true tag=v0.2.0 "sha=$c1" resume=true remaining=true

  # A tag at a commit without the notes, or at an unknown commit, is a clear refusal.
  decide_run "$c2" STUB_TAGS="v0.2.0:$c0"
  expect_fail "tag at a commit without the notes" "delete the tag (git push origin :refs/tags/v0.2.0)"
  decide_run "$c2" STUB_TAGS="v0.2.0:deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
  expect_fail "tag at an unknown commit" "is not a commit of main that holds docs/releases/v0.2.0.md"
  # An annotated tag is read through to its commit: skipped when released, resumed when not.
  git -C "$repo" tag -a v0.2.0 -m annotated "$c1"
  annotated="$(git -C "$repo" rev-parse refs/tags/v0.2.0)"
  decide_run "$c2" STUB_TAGS="v0.2.0:$annotated:tag" STUB_RELEASES="v0.2.0"
  expect_ok "annotated tag with a release" release=true tag=v0.3.0-rc.1 remaining=false
  decide_run "$c2" STUB_TAGS="v0.2.0:$annotated:tag"
  expect_ok "annotated tag without a release" release=true tag=v0.2.0 "sha=$c1" resume=true remaining=true
  decide_run "$c2" STUB_TAGS="v0.2.0:deadbeefdeadbeefdeadbeefdeadbeefdeadbeef:tag"
  expect_fail "annotated tag that cannot be read" "is not a commit of main that holds docs/releases/v0.2.0.md"

  # A release with no tag is refused.
  decide_run "$c2" STUB_RELEASES="v0.2.0"
  expect_fail "release without a tag" "a GitHub release for v0.2.0 exists but the tag does not"

  # A rename leaves one untagged notes file.
  git -C "$repo" mv docs/releases/v0.3.0-rc.1.md docs/releases/v0.3.0-rc.2.md
  git -C "$repo" commit --quiet -m rename
  c3="$(git -C "$repo" rev-parse HEAD)"
  decide_run "$c3" STUB_TAGS="v0.2.0:$c1" STUB_RELEASES="v0.2.0"
  expect_ok "rename" release=true tag=v0.3.0-rc.2 remaining=false

  # A prerelease sorts before its release, and a stable version without a changelog section is refused.
  c4="$(commit n docs/releases/v0.3.0.md)"
  decide_run "$c4" STUB_TAGS="v0.2.0:$c1" STUB_RELEASES="v0.2.0"
  expect_ok "prerelease first" release=true tag=v0.3.0-rc.2 remaining=true
  decide_run "$c4" STUB_TAGS="v0.2.0:$c1 v0.3.0-rc.2:$c1" STUB_RELEASES="v0.2.0 v0.3.0-rc.2"
  expect_fail "stable without a changelog section" "CHANGELOG.md has no section for 0.3.0"

  # Only main releases.
  decide_run "$c4" REF=refs/heads/other
  expect_fail "other branch" "main only"
  decide_run "$c4" REF=refs/heads/main STUB_TAGS="v0.2.0:$c1 v0.3.0-rc.2:$c1" STUB_RELEASES="v0.2.0 v0.3.0-rc.2" 
  expect_fail "main with a bad changelog" "CHANGELOG.md has no section for 0.3.0"

  # Odd file names are errors, never a silent "not a release".
  git -C "$repo" rm --quiet docs/releases/v0.3.0.md
  git -C "$repo" commit --quiet -m drop
  odd() { # odd FILENAME EXPECTED-SUBSTRING
    local sha
    printf 'n\n' > "$repo/docs/releases/$1"
    git -C "$repo" add -A
    git -C "$repo" commit --quiet -m "odd $1"
    sha="$(git -C "$repo" rev-parse HEAD)"
    decide_run "$sha" STUB_TAGS="v0.2.0:$c1 v0.3.0-rc.2:$c1" STUB_RELEASES="v0.2.0 v0.3.0-rc.2"
    expect_fail "file $1" "$2"
    git -C "$repo" rm --quiet -f "docs/releases/$1"
    git -C "$repo" commit --quiet -m "drop odd"
  }
  odd 'v1.2.md' "does not name a valid semantic version"
  odd 'v01.2.3.md' "does not name a valid semantic version"
  odd 'v1.2.3+build.md' "does not name a valid semantic version"
  odd 'v1.2.3-rc.1.lock.md' "does not name a valid git tag"
  odd 'v1.2.3"x.md' "does not name a valid semantic version"
  odd 'v1.2.3é.md' "does not name a valid semantic version"

  # Unrelated files under docs/releases are ignored.
  printf 'n\n' > "$repo/docs/releases/README.md"
  git -C "$repo" add -A
  git -C "$repo" commit --quiet -m readme
  sha_c1="$(git -C "$repo" rev-parse HEAD)"
  decide_run "$sha_c1" STUB_TAGS="v0.2.0:$c1 v0.3.0-rc.2:$c1" STUB_RELEASES="v0.2.0 v0.3.0-rc.2"
  expect_ok "unrelated files" release=false

  # The present state of main: notes for v0.1.0, v0.1.0-alpha.1 and v0.2.0; annotated tags v0.1.0,
  # v0.1.0-alpha.1, v0.1.0-rc.2, v0.1.0-rc.3 and v0.2.0; releases v0.2.0, v0.1.0 and v0.1.0-alpha.1
  # (the two rc tags have no notes file and no release). Nothing to release, no error.
  repo="$tmp/repo2"
  mkdir -p "$repo/docs/releases"
  git -C "$repo" init --quiet --initial-branch=main
  git -C "$repo" config user.name t
  git -C "$repo" config user.email t@example.com
  printf '# Changelog\n\n## [Unreleased]\n\n## [0.3.0] - 2026-10-07\n\n## [0.2.0] - 2026-10-06\n\n## [0.1.5] - 2026-10-05\n\n## [0.1.0] - 2026-10-01\n' > "$repo/CHANGELOG.md"
  commit "state" docs/releases/v0.1.0.md docs/releases/v0.1.0-alpha.1.md docs/releases/v0.2.0.md > /dev/null
  local n tags="" present released="v0.2.0 v0.1.0 v0.1.0-alpha.1"
  for n in v0.1.0 v0.1.0-alpha.1 v0.1.0-rc.2 v0.1.0-rc.3 v0.2.0; do
    git -C "$repo" tag -a "$n" -m "$n" HEAD
    tags="$tags $n:$(git -C "$repo" rev-parse "refs/tags/$n"):tag"
  done
  present="$(git -C "$repo" rev-parse HEAD)"
  decide_run "$present" REF=refs/heads/main STUB_TAGS="$tags" STUB_RELEASES="$released"
  expect_ok "the present state of main" release=false
  grep -Fq "nothing to release" <<<"$out" || fail "selftest: the present state should say there is nothing to release: $out"
  [[ "$(wc -l < "$outputs" | tr -d ' ')" == 1 ]] || fail "selftest: the present state should output only release=false: $(tr '\n' ' ' < "$outputs")"

  # Versions only move forward.
  local late
  late="$(commit "late" docs/releases/v0.1.5.md)"
  decide_run "$late" STUB_TAGS="$tags" STUB_RELEASES="$released"
  expect_fail "a late notes file for an old version" "v0.1.5.md) is not greater than 0.2.0"
  git -C "$repo" rm --quiet docs/releases/v0.1.5.md
  late="$(commit "late rc" docs/releases/v0.2.0-rc.1.md)"
  decide_run "$late" STUB_TAGS="$tags" STUB_RELEASES="$released"
  expect_fail "a rehearsal for an already released version" "0.2.0-rc.1 (docs/releases/v0.2.0-rc.1.md) is not greater than 0.2.0"
  git -C "$repo" rm --quiet docs/releases/v0.2.0-rc.1.md
  late="$(commit "next rc" docs/releases/v0.3.0-rc.1.md)"
  decide_run "$late" STUB_TAGS="$tags" STUB_RELEASES="$released"
  expect_ok "a rehearsal above the latest stable" release=true tag=v0.3.0-rc.1 rehearsal=true
  repo="$tmp/repo"

  # Semantic-version order.
  [[ "$(semver_cmp 0.3.0-rc.1 0.3.0)" == -1 && "$(semver_cmp 0.3.0 0.3.0-rc.1)" == 1 ]] || fail "selftest: a prerelease sorts before its release"
  [[ "$(semver_cmp 1.0.0-rc.2 1.0.0-rc.10)" == -1 && "$(semver_cmp 1.0.0-alpha 1.0.0-alpha.1)" == -1 ]] || fail "selftest: prerelease identifiers compare numerically and by length"
  [[ "$(semver_cmp 1.0.0-1 1.0.0-a)" == -1 && "$(semver_cmp 0.10.0 0.9.0)" == 1 && "$(semver_cmp 1.2.3 1.2.3)" == 0 ]] || fail "selftest: semver order"

  decide_run "$c1" REPO=
  [[ "$status" -ne 0 ]] && grep -Fq "REPO is required" <<<"$out" || fail "selftest: a missing input should be named: $out"

  echo "release-decide selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi
decide
