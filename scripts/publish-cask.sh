#!/usr/bin/env bash
#
# Publishes the Homebrew cask of a stable release to the tap. Run by the
# publish-homebrew job in release.yml, after verify-release, so a release whose
# artifacts fail verification never reaches the tap.
#
#   TAG                      release tag, e.g. v0.2.0
#   GH_TOKEN                 token that can read this repository's releases
#   HOMEBREW_TAP_DEPLOY_KEY  private half of the tap's deploy key (write access to
#                            ourostack/homebrew-tap only)
#   TAP_URL                  git URL of the tap (default git@github.com:ourostack/homebrew-tap.git)
#
# It downloads teamscrawl.rb from the release, commits it to the tap as
# "teamscrawl <version>" and pushes. Running it again for the same tag changes
# nothing. The key is written to a private temporary file and never printed.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}
for name in TAG GH_TOKEN HOMEBREW_TAP_DEPLOY_KEY; do
  [[ -n "${!name:-}" ]] || fail "$name is required"
done
[[ "$TAG" != *-* ]] || fail "$TAG is a prerelease; prereleases never reach the tap"
version="${TAG#v}"
tap="${TAP_URL:-git@github.com:ourostack/homebrew-tap.git}"
repo="${GITHUB_REPOSITORY:-ourostack/teamscrawl}"

work="$(mktemp -d)"
trap 'rm -r "$work"' EXIT
(umask 077 && printf '%s\n' "$HOMEBREW_TAP_DEPLOY_KEY" > "$work/key")
export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$work/known_hosts"

gh release download "$TAG" -R "$repo" -p teamscrawl.rb -D "$work/asset"
grep -q "version \"$version\"" "$work/asset/teamscrawl.rb" || fail "the cask attached to $TAG is not for version $version"

# Another tool's release may push between the clone and the push, so retry on a fresh clone.
for attempt in 1 2 3; do
  [[ ! -d "$work/tap" ]] || rm -r "$work/tap"
  git clone --quiet --depth 1 "$tap" "$work/tap"
  mkdir -p "$work/tap/Casks"
  if cmp -s "$work/asset/teamscrawl.rb" "$work/tap/Casks/teamscrawl.rb"; then
    echo "publish-cask: the tap already serves teamscrawl $version"
    exit 0
  fi
  cp "$work/asset/teamscrawl.rb" "$work/tap/Casks/teamscrawl.rb"
  git -C "$work/tap" add Casks/teamscrawl.rb
  git -C "$work/tap" -c user.name="Ari Mendelow" -c user.email="16390116+arimendelow@users.noreply.github.com" commit --quiet -m "teamscrawl $version"
  if git -C "$work/tap" push --quiet origin HEAD:main; then
    echo "publish-cask: pushed teamscrawl $version to $tap"
    exit 0
  fi
  echo "push attempt $attempt failed, retrying"
  sleep $((attempt * 5))
done
fail "could not push teamscrawl $version to $tap"
