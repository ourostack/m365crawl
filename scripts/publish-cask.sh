#!/usr/bin/env bash
#
# Publishes the Homebrew cask of a release to the tap. Run by the publish-homebrew
# job in release.yml, after verify-release, so a release whose artifacts fail
# verification never reaches the tap.
#
#   TAG                      release tag, e.g. v0.2.0 or v0.2.0-rc.1
#   GH_TOKEN                 token that can read this repository's releases
#   HOMEBREW_TAP_DEPLOY_KEY  private half of the tap's deploy key (write access to
#                            ourostack/homebrew-tap only)
#   TAP_URL                  git URL of the tap (default git@github.com:ourostack/homebrew-tap.git)
#   TAP_BRANCH               branch of the tap to push (default main). A stable tag
#                            must go to main. A prerelease (a rehearsal) must go to
#                            another branch, so the real push is exercised without
#                            changing what users install.
#
# It downloads teamscrawl.rb from the release, commits it to the tap as
# "teamscrawl <version>" and pushes. Running it again for the same tag changes
# nothing. The key is written to a private temporary file and never printed.
#
#   scripts/publish-cask.sh --restore-previous
#                            containment for a stable release whose install failed to verify
#                            (the verify-homebrew job failed): puts the tap's main back to the
#                            cask it served before "teamscrawl <version>", as a new commit (never
#                            a force push). It needs TAG and HOMEBREW_TAP_DEPLOY_KEY only. When the
#                            tap does not serve that version, it changes nothing.
#   scripts/publish-cask.sh --selftest   runs every path against a local bare repository
#
# SSH verifies the server against the host keys pinned in scripts/github_known_hosts.
# shellcheck disable=SC2015,SC2016,SC2153 # selftest checks are deliberate A && B || fail; the gh stubs are quoted heredocs.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

selftest() {
  local self bare seed stub out status before after
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/publish-cask-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  bare="$tmp/tap.git"
  seed="$tmp/seed"
  stub="$tmp/bin"
  mkdir -p "$stub"
  git init --quiet --bare --initial-branch=main "$bare"
  git init --quiet --initial-branch=main "$seed"
  git -C "$seed" -c user.name=t -c user.email=t@example.com commit --quiet --allow-empty -m seed
  git -C "$seed" push --quiet "file://$bare" main
  cat > "$stub/gh" <<'STUB'
#!/usr/bin/env bash
# Stand-in for: gh release download TAG -R REPO -p teamscrawl.rb -D DIR
[[ "$1 $2" == "release download" ]] || { echo "stub gh: unexpected: $*" >&2; exit 64; }
dir=""
while [[ $# -gt 0 ]]; do
  [[ "$1" != -D ]] || dir="$2"
  shift
done
mkdir -p "$dir"
cp "$STUB_CASK" "$dir/teamscrawl.rb"
STUB
  chmod +x "$stub/gh"

  run() { # run TAG CASKVERSION [VAR=value ...]; sets out and status
    local tag="$1" caskversion="$2" arg mode=""
    shift 2
    for arg in "$@"; do
      [[ "$arg" != RESTORE=1 ]] || mode=--restore-previous
    done
    printf 'cask "teamscrawl" do\n  version "%s"\nend\n' "$caskversion" > "$tmp/cask.rb"
    status=0
    out="$(env -i PATH="$stub:$PATH" HOME="$tmp" STUB_CASK="$tmp/cask.rb" TAG="$tag" GH_TOKEN=t HOMEBREW_TAP_DEPLOY_KEY=k \
      TAP_URL="file://$bare" "$@" "$self" ${mode:+"$mode"} 2>&1)" || status=$?
  }
  commits() { git --git-dir="$bare" rev-list --count "$1"; }
  expect_fail() { # expect_fail DESCRIPTION SUBSTRING
    [[ "$status" -ne 0 ]] || fail "selftest: $1 should fail"
    grep -Fq -- "$2" <<<"$out" || fail "selftest: $1: output missing '$2': $out"
  }

  # First publish pushes exactly one commit.
  before="$(commits main)"
  run v0.2.0 0.2.0
  [[ "$status" -eq 0 ]] || fail "selftest: first publish failed: $out"
  [[ "$(commits main)" -eq $((before + 1)) ]] || fail "selftest: first publish should add one commit"
  git --git-dir="$bare" show main:Casks/teamscrawl.rb | grep -Fq 'version "0.2.0"' || fail "selftest: tap cask is not 0.2.0"

  # A repeat changes nothing.
  after="$(git --git-dir="$bare" rev-parse main)"
  run v0.2.0 0.2.0
  [[ "$status" -eq 0 ]] || fail "selftest: repeat failed: $out"
  [[ "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: repeat changed the tap"
  grep -Fq "already serves" <<<"$out" || fail "selftest: repeat should say the tap is current"

  # A cask for another version is refused and the tap is untouched.
  run v0.3.0 0.2.0
  expect_fail "cask for another version" "is not for version 0.3.0"
  [[ "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: wrong-version cask changed the tap"

  # A prerelease is refused on main.
  run v0.3.0-rc.1 0.3.0-rc.1
  expect_fail "prerelease to main" "prerelease"
  run v0.3.0-rc.1 0.3.0-rc.1 TAP_BRANCH=main
  expect_fail "prerelease to explicit main" "prerelease"
  [[ "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: prerelease changed main"

  # A stable release never goes to another branch.
  run v0.3.0 0.3.0 TAP_BRANCH=rehearsal
  expect_fail "stable to rehearsal" "main"

  # A rehearsal pushes the real cask to its own branch and never touches main.
  run v0.3.0-rc.1 0.3.0-rc.1 TAP_BRANCH=rehearsal
  [[ "$status" -eq 0 ]] || fail "selftest: rehearsal failed: $out"
  git --git-dir="$bare" show rehearsal:Casks/teamscrawl.rb | grep -Fq 'version "0.3.0-rc.1"' || fail "selftest: rehearsal branch cask is wrong"
  [[ "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: rehearsal changed main"
  after="$(git --git-dir="$bare" rev-parse rehearsal)"
  run v0.3.0-rc.1 0.3.0-rc.1 TAP_BRANCH=rehearsal
  [[ "$status" -eq 0 && "$(git --git-dir="$bare" rev-parse rehearsal)" == "$after" ]] || fail "selftest: rehearsal repeat changed the branch: $out"
  run v0.3.0-rc.2 0.3.0-rc.2 TAP_BRANCH=rehearsal
  [[ "$status" -eq 0 && "$(commits rehearsal)" -eq $(($(commits main) + 2)) ]] || fail "selftest: second rehearsal should add a commit to the branch: $out"

  # Containment: restore the cask the tap served before a version, with a new commit and no force push.
  run v0.4.0 0.4.0
  [[ "$status" -eq 0 ]] || fail "selftest: publish of 0.4.0 failed: $out"
  before="$(git --git-dir="$bare" rev-parse main)"
  git --git-dir="$bare" show main:Casks/teamscrawl.rb | grep -Fq 'version "0.4.0"' || fail "selftest: tap is not 0.4.0"
  run v0.4.0 0.4.0 RESTORE=1
  [[ "$status" -eq 0 ]] || fail "selftest: restore failed: $out"
  git --git-dir="$bare" show main:Casks/teamscrawl.rb | grep -Fq 'version "0.2.0"' || fail "selftest: restore did not put 0.2.0 back"
  git --git-dir="$bare" merge-base --is-ancestor "$before" main || fail "selftest: restore rewrote history"
  [[ "$(commits main)" -eq $(($(commits "$before") + 1)) ]] || fail "selftest: restore should add exactly one commit"
  after="$(git --git-dir="$bare" rev-parse main)"
  run v0.4.0 0.4.0 RESTORE=1
  [[ "$status" -eq 0 && "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: a repeated restore changed the tap: $out"
  grep -Fq "does not serve" <<<"$out" || fail "selftest: a restore with nothing to do should say so"
  run v0.2.0 0.2.0 RESTORE=1
  expect_fail "restore with no previous cask" "no cask before"
  run v0.5.0-rc.1 0.5.0-rc.1 RESTORE=1
  expect_fail "restore of a prerelease" "stable"
  [[ "$(git --git-dir="$bare" rev-parse main)" == "$after" ]] || fail "selftest: a refused restore changed the tap"

  # A missing input is named.
  for name in TAG GH_TOKEN HOMEBREW_TAP_DEPLOY_KEY; do
    status=0
    out="$(env PATH="$stub:$PATH" HOME="$tmp" STUB_CASK="$tmp/cask.rb" TAG=v0.2.0 GH_TOKEN=t HOMEBREW_TAP_DEPLOY_KEY=k \
      TAP_URL="file://$bare" bash -c 'unset "$1"; exec "$2"' _ "$name" "$self" 2>&1)" || status=$?
    expect_fail "missing $name" "$name is required"
  done
  echo "publish-cask selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi

mode=publish
[[ "${1:-}" != "--restore-previous" ]] || mode=restore
required="TAG HOMEBREW_TAP_DEPLOY_KEY"
[[ "$mode" == restore ]] || required="TAG GH_TOKEN HOMEBREW_TAP_DEPLOY_KEY"
for name in $required; do
  [[ -n "${!name:-}" ]] || fail "$name is required"
done
branch="${TAP_BRANCH:-main}"
if [[ "$mode" == restore ]]; then
  [[ "$TAG" != *-* ]] || fail "$TAG is a prerelease; only a stable release is ever on the tap's main, so there is nothing to restore"
  [[ "$branch" == main ]] || fail "restore works on the tap's main only, not $branch"
elif [[ "$TAG" == *-* ]]; then
  [[ "$branch" != main ]] || fail "$TAG is a prerelease; a prerelease must not reach the tap's main (set TAP_BRANCH to a rehearsal branch)"
else
  [[ "$branch" == main ]] || fail "$TAG is a stable release; it publishes to the tap's main only, not $branch"
fi
version="${TAG#v}"
tap="${TAP_URL:-git@github.com:ourostack/homebrew-tap.git}"
repo="${GITHUB_REPOSITORY:-ourostack/teamscrawl}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

work="$(mktemp -d)"
trap 'rm -r "${work:?}"' EXIT
(umask 077 && printf '%s\n' "$HOMEBREW_TAP_DEPLOY_KEY" > "$work/key")
export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=$here/github_known_hosts"
tap_author=(-c user.name="Ari Mendelow" -c user.email="16390116+arimendelow@users.noreply.github.com")

if [[ "$mode" == restore ]]; then
  for attempt in 1 2 3; do
    [[ ! -d "$work/tap" ]] || rm -r "$work/tap"
    git clone --quiet "$tap" "$work/tap"
    if ! grep -q "version \"$version\"" "$work/tap/Casks/teamscrawl.rb" 2> /dev/null; then
      echo "publish-cask: the tap does not serve teamscrawl $version; nothing to restore"
      exit 0
    fi
    published="$(git -C "$work/tap" log -n 1 --format=%H --extended-regexp --grep="^teamscrawl ${version//./\\.}\$" -- Casks/teamscrawl.rb)"
    [[ -n "$published" ]] || fail "the tap has no commit 'teamscrawl $version' to restore from"
    git -C "$work/tap" cat-file -e "${published}^:Casks/teamscrawl.rb" 2> /dev/null || fail "no cask before teamscrawl $version on the tap: nothing to restore to"
    git -C "$work/tap" show "${published}^:Casks/teamscrawl.rb" > "$work/previous.rb"
    cp "$work/previous.rb" "$work/tap/Casks/teamscrawl.rb"
    git -C "$work/tap" add Casks/teamscrawl.rb
    git -C "$work/tap" "${tap_author[@]}" commit --quiet -m "Restore the cask before teamscrawl $version"
    if git -C "$work/tap" push --quiet origin HEAD:refs/heads/main; then
      echo "publish-cask: restored the cask before teamscrawl $version on $tap"
      exit 0
    fi
    echo "push attempt $attempt failed, retrying"
    sleep $((attempt * 5))
  done
  fail "could not restore the cask before teamscrawl $version on $tap"
fi

gh release download "$TAG" -R "$repo" -p teamscrawl.rb -D "$work/asset"
grep -q "version \"$version\"" "$work/asset/teamscrawl.rb" || fail "the cask attached to $TAG is not for version $version"

# Another tool's release may push between the clone and the push, so retry on a fresh clone.
for attempt in 1 2 3; do
  [[ ! -d "$work/tap" ]] || rm -r "$work/tap"
  if git ls-remote --exit-code --heads "$tap" "$branch" > /dev/null 2>&1; then
    git clone --quiet --depth 1 --branch "$branch" "$tap" "$work/tap"
  else
    # A rehearsal branch starts from the tap's default branch the first time.
    git clone --quiet --depth 1 "$tap" "$work/tap"
    git -C "$work/tap" checkout --quiet -b "$branch"
  fi
  mkdir -p "$work/tap/Casks"
  if cmp -s "$work/asset/teamscrawl.rb" "$work/tap/Casks/teamscrawl.rb"; then
    echo "publish-cask: the tap ($branch) already serves teamscrawl $version"
    exit 0
  fi
  cp "$work/asset/teamscrawl.rb" "$work/tap/Casks/teamscrawl.rb"
  git -C "$work/tap" add Casks/teamscrawl.rb
  git -C "$work/tap" "${tap_author[@]}" commit --quiet -m "teamscrawl $version"
  if git -C "$work/tap" push --quiet origin "HEAD:refs/heads/$branch"; then
    echo "publish-cask: pushed teamscrawl $version to $tap ($branch)"
    exit 0
  fi
  echo "push attempt $attempt failed, retrying"
  sleep $((attempt * 5))
done
fail "could not push teamscrawl $version to $tap"
