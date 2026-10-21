#!/usr/bin/env bash
# Build and verify the exact body supplied to the normal release publisher.
# TAG is required; verification also needs REPO and gh authentication.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

generate() {
  local file
  [[ "${TAG:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "TAG must be a release version"
  file="docs/releases/${TAG}.md"
  [[ -f "$file" ]] || fail "missing release notes: $file"
  grep -q '[^[:space:]]' "$file" || fail "empty release notes: $file"
  cat "$file"
  printf '\nThe darwin binaries are Developer ID signed and notarized by Apple.\n'
  printf 'The Windows binaries are unsigned.\n'
}

verify() {
  local expected actual
  [[ -n "${REPO:-}" ]] || fail "REPO is required"
  expected="$(generate)"
  actual="$(gh release view "$TAG" -R "$REPO" --json body --jq .body)" \
    || fail "could not read the published release notes for $TAG"
  [[ "$actual" == "$expected" ]] || fail "published release notes for $TAG do not match the supplied notes and signing disclosures"
  echo "release-notes: $TAG verified"
}

selftest() {
  local self tmp expected out
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-notes-selftest.XXXXXX")"
  mkdir -p "$tmp/repo/docs/releases" "$tmp/bin"
  printf '# m365crawl v1.2.3\n\nA compatible patch.\n' > "$tmp/repo/docs/releases/v1.2.3.md"
  expected='# m365crawl v1.2.3

A compatible patch.

The darwin binaries are Developer ID signed and notarized by Apple.
The Windows binaries are unsigned.'
  out="$(cd "$tmp/repo" && TAG=v1.2.3 bash "$self" generate)"
  [[ "$out" == "$expected" ]] || fail "selftest: generate must preserve the notes and signing disclosures"
  if (cd "$tmp/repo" && TAG=v9.9.9 bash "$self" generate) > "$tmp/out" 2> "$tmp/err"; then
    fail "selftest: missing notes must fail"
  fi
  [[ ! -s "$tmp/out" ]] || fail "selftest: missing notes emitted a partial body"
  printf ' \n\t\n' > "$tmp/repo/docs/releases/v1.2.4.md"
  if (cd "$tmp/repo" && TAG=v1.2.4 bash "$self" generate) > "$tmp/out" 2> "$tmp/err"; then
    fail "selftest: whitespace-only notes must fail"
  fi
  [[ ! -s "$tmp/out" ]] || fail "selftest: empty notes emitted signing-only success"
  cat > "$tmp/bin/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == 'release view v1.2.3 -R o/r --json body --jq .body' ]] || exit 64
[[ "${STUB_FAILURE:-0}" == 0 ]] || exit 1
cat "$STUB_BODY"
STUB
  chmod +x "$tmp/bin/gh"
  printf '%s\n' "$expected" > "$tmp/body"
  (
    cd "$tmp/repo"
    export TAG=v1.2.3 REPO=o/r STUB_BODY="$tmp/body" PATH="$tmp/bin:$PATH"
    bash "$self" verify > "$tmp/verified"
    grep -Fq 'release-notes: v1.2.3 verified' "$tmp/verified" || fail "selftest: verification did not report success"
    printf '\n' > "$tmp/body"
    if bash "$self" verify > "$tmp/out" 2> "$tmp/err"; then
      fail "selftest: the published blank-body regression must fail"
    fi
    printf '# m365crawl v1.2.2\n' > "$tmp/body"
    if bash "$self" verify > "$tmp/out" 2> "$tmp/err"; then
      fail "selftest: another version's body must fail"
    fi
    printf '# m365crawl v1.2.3\n\nA compatible patch.\n' > "$tmp/body"
    if bash "$self" verify > "$tmp/out" 2> "$tmp/err"; then
      fail "selftest: missing signing disclosures must fail"
    fi
    printf '%s\n' "$expected" > "$tmp/body"
    if STUB_FAILURE=1 bash "$self" verify > "$tmp/out" 2> "$tmp/err"; then
      fail "selftest: a GitHub read failure must fail closed"
    fi
  )
  echo "release-notes selftest: ok"
}

case "${1:-}" in
  --selftest) selftest ;;
  generate) generate ;;
  verify) verify ;;
  *) fail "usage: scripts/release-notes.sh generate | verify | --selftest" ;;
esac
