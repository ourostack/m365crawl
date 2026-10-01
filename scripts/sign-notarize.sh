#!/usr/bin/env bash
#
# Developer ID signs and notarizes a bare macOS binary (no .app bundle).
#
# Called by goreleaser as a builds[].hooks.post hook, once per darwin target, so the
# archives, checksums and Homebrew cask all reflect the signed binary. A clean no-op
# when APPLE_DEVELOPER_ID_CERTIFICATE_BASE64 is empty, so forks, PR snapshots and
# releases without secrets keep building unsigned binaries.
#
# Secrets (same names as ourostack/ouro-md, never printed):
#   APPLE_DEVELOPER_ID_CERTIFICATE_BASE64    base64 of the Developer ID .p12
#   APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD  .p12 password
#   APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY  e.g. "Developer ID Application: Name (TEAMID)"
#   APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID   notarytool credentials
#
# Bare binaries cannot be stapled; Gatekeeper checks notarization online.
set -euo pipefail

usage() {
  echo "Usage: scripts/sign-notarize.sh BINARY | --selftest" >&2
}

fail() {
  echo "error: $*" >&2
  exit 1
}

have_signing() {
  [[ -n "${APPLE_DEVELOPER_ID_CERTIFICATE_BASE64:-}" ]]
}

# Several goreleaser build hooks can run in parallel, so the shared keychain is
# created once under a mkdir lock and reused by later calls.
prepare_keychain() {
  local base="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
  base="${base%/}/teamscrawl-signing"
  mkdir -p "$base"
  local lock="$base/lock" waited=0
  until mkdir "$lock" 2>/dev/null; do
    sleep 1
    waited=$((waited + 1))
    [[ "$waited" -lt 120 ]] || fail "timed out waiting for the signing keychain lock"
  done
  # shellcheck disable=SC2064
  trap "rmdir '$lock' 2>/dev/null || true" RETURN

  KEYCHAIN="$base/signing.keychain-db"
  if [[ -f "$base/ready" ]]; then
    return 0
  fi

  [[ -n "${APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD:-}" ]] || fail "APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD is required for signing"
  local pw cert
  pw="$(uuidgen)"
  echo "::add-mask::$pw"
  cert="$base/developer-id.p12"
  printf '%s' "$APPLE_DEVELOPER_ID_CERTIFICATE_BASE64" | base64 --decode > "$cert"
  chmod 600 "$cert"
  security create-keychain -p "$pw" "$KEYCHAIN"
  security set-keychain-settings -lut 21600 "$KEYCHAIN"
  security unlock-keychain -p "$pw" "$KEYCHAIN"
  security import "$cert" -k "$KEYCHAIN" -P "$APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$pw" "$KEYCHAIN" >/dev/null
  # shellcheck disable=SC2046
  security list-keychains -d user -s "$KEYCHAIN" $(security list-keychains -d user | sed 's/[ "]*//g')
  rm -f "$cert"
  : > "$base/ready"
}

selftest() {
  local self="$0" out status=0
  out="$(env -u APPLE_DEVELOPER_ID_CERTIFICATE_BASE64 "$self" /definitely/missing 2>&1)" \
    || fail "selftest: expected no-op success when secrets are absent"
  grep -Fq "signing: skipped, secrets not available" <<<"$out" \
    || fail "selftest: no-op message missing"
  env -u APPLE_ID APPLE_DEVELOPER_ID_CERTIFICATE_BASE64=Zm9v "$self" /definitely/missing >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: expected failure for a missing binary when secrets are present"
  status=0
  env APPLE_DEVELOPER_ID_CERTIFICATE_BASE64=Zm9v "$self" >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: expected usage failure without a binary"
  echo "sign-notarize selftest ok"
}

case "${1:-}" in
  --selftest) selftest; exit 0 ;;
  -h|--help) usage; exit 0 ;;
esac

if ! have_signing; then
  echo "signing: skipped, secrets not available"
  exit 0
fi

[[ $# -eq 1 && -n "$1" ]] || { usage; exit 64; }
BINARY="$1"
[[ -f "$BINARY" ]] || fail "binary not found: $BINARY"

for name in APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY APPLE_ID APPLE_APP_SPECIFIC_PASSWORD APPLE_TEAM_ID; do
  [[ -n "${!name:-}" ]] || fail "$name is required for signing"
  echo "::add-mask::${!name}"
done
echo "::add-mask::$APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD"
for tool in codesign security ditto uuidgen; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
xcrun notarytool --help >/dev/null 2>&1 || fail "xcrun notarytool is required"

KEYCHAIN=""
prepare_keychain

echo "==> Developer ID signing $BINARY"
codesign --force --options runtime --timestamp --keychain "$KEYCHAIN" --sign "$APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY" "$BINARY"
codesign --verify --strict --verbose=2 "$BINARY"

work="$(mktemp -d "${TMPDIR:-/tmp}/teamscrawl-notary.XXXXXX")"
trap 'rm -f "$work/notary.zip"; rmdir "$work" 2>/dev/null || true' EXIT
echo "==> Submitting to Apple notary service"
ditto -c -k "$BINARY" "$work/notary.zip"
xcrun notarytool submit "$work/notary.zip" \
  --apple-id "$APPLE_ID" \
  --password "$APPLE_APP_SPECIFIC_PASSWORD" \
  --team-id "$APPLE_TEAM_ID" \
  --wait

echo "==> Verifying"
codesign -dv --verbose=2 "$BINARY" 2>&1 | grep -E 'Authority|TeamIdentifier|flags|Timestamp' || true
spctl --assess --type install --verbose=2 "$BINARY" 2>&1 || echo "note: spctl does not assess bare binaries offline; notarization is checked online by Gatekeeper"

echo "signing: developer-id signed and notarized: $BINARY"
