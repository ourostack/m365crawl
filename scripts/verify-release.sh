#!/usr/bin/env bash
#
# Verifies what users actually download from a published GitHub release: the two
# darwin tarballs and checksums.txt. Run by the verify-release job in release.yml
# on macos-latest (arm64) after the release is published.
#
#   TAG              release tag, e.g. v0.1.0 or v0.1.0-rc.2
#   REPO             owner/name (GITHUB_REPOSITORY)
#   EXPECT_COMMIT    full commit SHA the tag must report (GITHUB_SHA)
#   APPLE_TEAM_ID    expected signing team
#   GH_TOKEN         token for `gh release download`
#
# Checks, failing on the first mismatch: checksums, architecture of each
# tarball, every sign-notarize.sh gate on each downloaded binary, Gatekeeper
# acceptance after a quarantine attribute is added, and `m365crawl --json version`
# on the arm64 binary. The release flags are settled by the settle job (scripts/release-flags.sh
# settle), after this job and the Windows jobs have passed.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
gates="$here/sign-notarize.sh"

fail() {
  echo "error: $*" >&2
  exit 1
}

for name in TAG REPO EXPECT_COMMIT APPLE_TEAM_ID; do
  [[ -n "${!name:-}" ]] || fail "$name is required"
done
version="${TAG#v}"

work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/verify-release.XXXXXX")"
echo "==> Downloading $TAG from $REPO into $work"
gh release download "$TAG" -R "$REPO" -D "$work" -p 'checksums.txt' \
  -p "m365crawl_${version}_darwin_arm64.tar.gz" -p "m365crawl_${version}_darwin_amd64.tar.gz"

echo "==> Checksums"
(
  cd "$work"
  for arch in arm64 amd64; do
    line="$(grep -F "  m365crawl_${version}_darwin_${arch}.tar.gz" checksums.txt || true)"
    [[ -n "$line" ]] || fail "checksums.txt has no entry for darwin_${arch}"
    printf '%s\n' "$line" | shasum -a 256 -c - || fail "checksum mismatch for darwin_${arch}"
  done
)

for arch in arm64 amd64; do
  echo "==> darwin_${arch}"
  dir="$work/$arch"
  mkdir "$dir"
  tar -xzf "$work/m365crawl_${version}_darwin_${arch}.tar.gz" -C "$dir" m365crawl
  bin="$dir/m365crawl"
  want="$arch"
  [[ "$arch" == amd64 ]] && want=x86_64
  file "$bin" | grep -Fq "$want" || fail "file(1) does not report $want for the $arch tarball"
  [[ "$(lipo -archs "$bin")" == "$want" ]] || fail "lipo reports '$(lipo -archs "$bin")' for the $arch tarball, expected $want"
  "$gates" --verify "$bin"
  # A browser download carries a quarantine attribute; Gatekeeper must still accept it.
  "$gates" --verify-quarantined "$bin"
done

echo "==> Running the arm64 binary"
out="$("$work/arm64/m365crawl" --json version)"
got_version="$(printf '%s' "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')"
got_commit="$(printf '%s' "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["commit"])')"
[[ "$got_version" == "$version" ]] || fail "version is '$got_version', expected '$version'"
[[ "$got_commit" == "$EXPECT_COMMIT" ]] || fail "commit is '$got_commit', expected '$EXPECT_COMMIT'"
echo "verify-release: $TAG verified ($got_version, $got_commit)"
