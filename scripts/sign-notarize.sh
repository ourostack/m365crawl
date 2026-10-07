#!/usr/bin/env bash
#
# Developer ID signs and notarizes a bare macOS binary (no .app bundle).
#
# Called by goreleaser as a builds[].hooks.post hook, once per darwin target, so the
# archives, checksums and Homebrew cask all reflect the signed binary. It fails
# closed: every Apple secret below must be set, and every gate in verify_binary must
# pass, or the release fails. There is no unsigned path; the only secret-free mode
# is --selftest. Also runs the same gates on a finished binary (--verify) and with a
# quarantine attribute set (--verify-quarantined), which verify-release uses.
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
  echo "Usage: scripts/sign-notarize.sh BINARY | --check-secrets | --verify BINARY | --verify-quarantined BINARY | --selftest" >&2
}

fail() {
  echo "error: $*" >&2
  exit 1
}

REQUIRED_SECRETS=(
  APPLE_DEVELOPER_ID_CERTIFICATE_BASE64
  APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD
  APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY
  APPLE_ID
  APPLE_APP_SPECIFIC_PASSWORD
  APPLE_TEAM_ID
)

# Names only, never values.
require_secrets() {
  local name missing=()
  for name in "${REQUIRED_SECRETS[@]}"; do
    [[ -n "${!name:-}" ]] || missing+=("$name")
  done
  [[ ${#missing[@]} -eq 0 ]] || fail "missing Apple signing secrets: ${missing[*]}; a release must be signed and notarized"
}

# Gatekeeper's online ticket lookup can lag notarization by a minute, so spctl
# is retried with backoff. SPCTL_BACKOFF (seconds, space separated) is overridable for tests.
spctl_gate() {
  local binary="$1" out attempt=0 delay
  local -a delays
  read -r -a delays <<<"${SPCTL_BACKOFF:-15 30 60 90}"
  while :; do
    # A bare Mach-O CLI is assessed as an install, not exec: `--type exec` only
    # accepts app bundles and rejects every CLI with "does not seem to be an app".
    if out="$(spctl --assess --type install -vv "$binary" 2>&1)" \
      && grep -Fq "accepted" <<<"$out" && grep -Fq "source=Notarized Developer ID" <<<"$out"; then
      echo "gate spctl: accepted, source=Notarized Developer ID"
      return 0
    fi
    if [[ $attempt -ge ${#delays[@]} ]]; then
      echo "$out" >&2
      fail "gate spctl: Gatekeeper did not report Notarized Developer ID for $binary"
    fi
    delay="${delays[$attempt]}"
    attempt=$((attempt + 1))
    echo "gate spctl: not accepted yet (attempt $attempt), retrying in ${delay}s" >&2
    sleep "$delay"
  done
}

# Every hard gate for one signed binary. Needs APPLE_TEAM_ID. (The notarytool
# "Accepted" gate runs at signing time in notarize_zip; for a finished binary the
# spctl "Notarized Developer ID" result is Apple's own proof of an accepted ticket.)
verify_binary() {
  local binary="$1" info
  [[ -f "$binary" ]] || fail "binary not found: $binary"
  [[ -n "${APPLE_TEAM_ID:-}" ]] || fail "APPLE_TEAM_ID is required to verify a binary"
  codesign --verify --strict --verbose=2 "$binary" || fail "gate codesign: --verify --strict failed for $binary"
  echo "gate codesign: verify --strict ok"
  info="$(codesign -dv --verbose=4 "$binary" 2>&1)" || fail "gate codesign: could not read the signature of $binary"
  grep -Fxq "TeamIdentifier=$APPLE_TEAM_ID" <<<"$info" || fail "gate team: TeamIdentifier is not the expected team for $binary"
  echo "gate team: TeamIdentifier matches APPLE_TEAM_ID"
  grep -Eq '^Authority=Developer ID Application' <<<"$info" || fail "gate authority: not signed by a Developer ID Application certificate: $binary"
  grep -Eq 'flags=0x[0-9a-f]+\([^)]*runtime[^)]*\)' <<<"$info" || fail "gate runtime: hardened runtime flag missing on $binary"
  echo "gate runtime: hardened runtime enabled"
  grep -Eq '^Timestamp=' <<<"$info" || fail "gate timestamp: no secure timestamp on $binary"
  echo "gate timestamp: secure timestamp present"
  spctl_gate "$binary"
}

# Gatekeeper must still accept a binary a browser quarantined.
verify_quarantined() {
  local binary="$1"
  [[ -f "$binary" ]] || fail "binary not found: $binary"
  xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");Safari;" "$binary" || fail "could not set the quarantine attribute"
  spctl_gate "$binary"
}

# Several goreleaser build hooks can run in parallel, so the shared keychain is
# created once under a mkdir lock and reused by later calls.
prepare_keychain() {
  local base="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
  base="${base%/}/m365crawl-signing"
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

# Submits a zip to the notary service and succeeds only when the final status is
# exactly "Accepted". `notarytool submit --wait` can exit 0 for an "Invalid"
# submission, so the JSON status is checked explicitly. The wait is bounded
# (NOTARY_TIMEOUT, default 20m) so a slow notary queue fails with its reason
# instead of running into the release job's own timeout without one.
notarize_zip() {
  local zip="$1" out id status
  out="$(xcrun notarytool submit "$zip" \
    --apple-id "$APPLE_ID" \
    --password "$APPLE_APP_SPECIFIC_PASSWORD" \
    --team-id "$APPLE_TEAM_ID" \
    --wait --timeout "${NOTARY_TIMEOUT:-20m}" --output-format json)" ||
    fail "notarytool submit failed or did not finish within ${NOTARY_TIMEOUT:-20m} (when Apple's notary queue is slow the submission carries on at Apple; run the Release workflow again later and it resumes from the tag)"
  status="$(json_field status "$out")"
  id="$(json_field id "$out")"
  if [[ "$status" != "Accepted" ]]; then
    echo "notarization status: ${status:-unknown} (submission ${id:-unknown})" >&2
    if [[ -n "$id" ]]; then
      xcrun notarytool log "$id" \
        --apple-id "$APPLE_ID" \
        --password "$APPLE_APP_SPECIFIC_PASSWORD" \
        --team-id "$APPLE_TEAM_ID" >&2 || true
    fi
    fail "notarization was not accepted"
  fi
  echo "notarization status: Accepted (submission $id)"
}

json_field() {
  if command -v plutil >/dev/null 2>&1; then
    printf '%s' "$2" | plutil -extract "$1" raw -o - - 2>/dev/null || true
  else
    printf '%s' "$2" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("'"$1"'",""))' 2>/dev/null || true
  fi
}

# Stubbed codesign, spctl, xattr and sleep exercise every gate in verify_binary.
selftest_gates() {
  local self="$1" stub="$2" tmp="$3" out status
  cat > "$stub/codesign" <<'STUB'
#!/usr/bin/env bash
if [[ "$1" == "--verify" ]]; then
  [[ "${STUB_VERIFY:-ok}" == "ok" ]] || { echo "stub: a sealed resource is missing or invalid" >&2; exit 1; }
  exit 0
fi
echo "Executable=/stub" >&2
echo "Identifier=m365crawl" >&2
echo "CodeDirectory v=20500 size=1 flags=${STUB_FLAGS:-0x10000(runtime)} hashes=1 location=embedded" >&2
echo "Authority=${STUB_AUTHORITY:-Developer ID Application: Stub (TEAMID)}" >&2
[[ "${STUB_TIMESTAMP:-yes}" == "yes" ]] && echo "Timestamp=Oct 1, 2026 at 12:00:00" >&2
echo "TeamIdentifier=${STUB_TEAM:-TEAMID}" >&2
exit 0
STUB
  cat > "$stub/spctl" <<'STUB'
#!/usr/bin/env bash
n=0
[[ -f "$STUB_SPCTL_COUNT" ]] && n="$(cat "$STUB_SPCTL_COUNT")"
n=$((n + 1))
echo "$n" > "$STUB_SPCTL_COUNT"
bin="${*: -1}"
# Mirror real spctl: exec assessment rejects any bare Mach-O CLI.
if [[ " $* " == *" --type exec "* ]]; then
  echo "$bin: rejected (the code is valid but does not seem to be an app)" >&2
  exit 3
fi
if [[ "$n" -le "${STUB_SPCTL_FAILS:-0}" ]]; then
  echo "$bin: rejected" >&2
  exit 3
fi
echo "$bin: accepted" >&2
echo "source=${STUB_SPCTL_SOURCE:-Notarized Developer ID}" >&2
STUB
  printf '#!/usr/bin/env bash\nexit 0\n' > "$stub/xattr"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$stub/sleep"
  chmod +x "$stub/codesign" "$stub/spctl" "$stub/xattr" "$stub/sleep"
  : > "$tmp/bin-under-test"
  local bin="$tmp/bin-under-test"
  export STUB_SPCTL_COUNT="$tmp/spctl-count" SPCTL_BACKOFF="1 1 1"
  run_gate() { # name, expected (pass|fail), expected message, env assignments...
    local name="$1" want="$2" msg="$3"
    shift 3
    rm -f "$STUB_SPCTL_COUNT"
    status=0
    out="$(env PATH="$stub:$PATH" APPLE_TEAM_ID=TEAMID "$@" "$self" --verify "$bin" 2>&1)" || status=$?
    if [[ "$want" == "pass" ]]; then
      [[ "$status" -eq 0 ]] || fail "selftest: gate $name should pass: $out"
    else
      [[ "$status" -ne 0 ]] || fail "selftest: gate $name should fail"
    fi
    [[ -z "$msg" ]] || grep -Fq "$msg" <<<"$out" || fail "selftest: gate $name output missing: $msg"
  }
  run_gate "all green" pass "gate spctl: accepted" STUB_VERIFY=ok
  run_gate "strict verify" fail "gate codesign" STUB_VERIFY=bad
  run_gate "team mismatch" fail "gate team" STUB_TEAM=OTHER
  run_gate "authority" fail "gate authority" "STUB_AUTHORITY=Apple Development: x"
  run_gate "runtime flag" fail "gate runtime" STUB_FLAGS=0x2
  run_gate "ad-hoc flag" fail "gate runtime" "STUB_FLAGS=0x2(adhoc)"
  run_gate "timestamp" fail "gate timestamp" STUB_TIMESTAMP=no
  run_gate "spctl source" fail "gate spctl" "STUB_SPCTL_SOURCE=Developer ID"
  run_gate "spctl retry succeeds" pass "retrying" STUB_SPCTL_FAILS=2
  run_gate "spctl retry exhausted" fail "gate spctl" STUB_SPCTL_FAILS=9
  status=0
  out="$(env PATH="$stub:$PATH" APPLE_TEAM_ID= "$self" --verify "$bin" 2>&1)" || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: gates need APPLE_TEAM_ID"
  rm -f "$STUB_SPCTL_COUNT"
  PATH="$stub:$PATH" "$self" --verify-quarantined "$bin" >/dev/null 2>&1 || fail "selftest: quarantined gate should pass"
  rm -f "$STUB_SPCTL_COUNT"
  status=0
  PATH="$stub:$PATH" STUB_SPCTL_FAILS=9 "$self" --verify-quarantined "$bin" >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: quarantined gate should fail when spctl rejects"
}

selftest() {
  local self="$0" out status=0
  # Fail closed: with any secret missing, signing fails and names what is missing.
  out="$(env -u APPLE_DEVELOPER_ID_CERTIFICATE_BASE64 "$self" /definitely/missing 2>&1)" || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: expected failure when secrets are absent"
  grep -Fq "missing Apple signing secrets: APPLE_DEVELOPER_ID_CERTIFICATE_BASE64" <<<"$out" \
    || fail "selftest: missing-secret message missing"
  status=0
  env -u APPLE_TEAM_ID "$self" --check-secrets >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: --check-secrets should fail without APPLE_TEAM_ID"
  status=0
  env APPLE_DEVELOPER_ID_CERTIFICATE_BASE64=Zm9v APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD=p APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY=i \
    APPLE_ID=a APPLE_APP_SPECIFIC_PASSWORD=p APPLE_TEAM_ID=T "$self" /definitely/missing >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: expected failure for a missing binary when secrets are present"
  env APPLE_DEVELOPER_ID_CERTIFICATE_BASE64=Zm9v APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD=p APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY=i \
    APPLE_ID=a APPLE_APP_SPECIFIC_PASSWORD=p APPLE_TEAM_ID=T "$self" --check-secrets >/dev/null 2>&1 || fail "selftest: --check-secrets should pass with every secret"
  status=0
  env APPLE_DEVELOPER_ID_CERTIFICATE_BASE64=Zm9v "$self" >/dev/null 2>&1 || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: expected usage failure without a binary"
  # Stubbed xcrun: Accepted succeeds, Invalid fails and fetches the log.
  local stub tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/sign-notarize-selftest.XXXXXX")"
  stub="$tmp/bin"
  mkdir "$stub"
  cat > "$stub/xcrun" <<'STUB'
#!/usr/bin/env bash
if [[ "$2" == "submit" ]]; then
  printf '{"id":"abc-123","status":"%s"}\n' "${STUB_STATUS:-Accepted}"
elif [[ "$2" == "log" ]]; then
  echo "stub notary log for $3"
fi
STUB
  chmod +x "$stub/xcrun"
  export APPLE_ID=a@example.com APPLE_APP_SPECIFIC_PASSWORD=x APPLE_TEAM_ID=T
  out="$(PATH="$stub:$PATH" STUB_STATUS=Accepted "$self" --selftest-notarize z.zip 2>&1)" \
    || fail "selftest: Accepted should succeed"
  grep -Fq "Accepted" <<<"$out" || fail "selftest: Accepted output missing"
  status=0
  out="$(PATH="$stub:$PATH" STUB_STATUS=Invalid "$self" --selftest-notarize z.zip 2>&1)" || status=$?
  [[ "$status" -ne 0 ]] || fail "selftest: Invalid should fail"
  grep -Fq "stub notary log for abc-123" <<<"$out" || fail "selftest: Invalid should print the notary log"
  selftest_gates "$self" "$stub" "$tmp"
  rm -f "$stub/xcrun" "$stub/codesign" "$stub/spctl" "$stub/xattr" "$stub/sleep" "$tmp/bin-under-test" "$tmp/spctl-count"
  rmdir "$stub" "$tmp"
  echo "sign-notarize selftest ok"
}

case "${1:-}" in
  --selftest) selftest; exit 0 ;;
  --selftest-notarize) notarize_zip "${2:-}"; exit 0 ;;
  --check-secrets) require_secrets; echo "signing: all Apple secrets present"; exit 0 ;;
  --verify) [[ $# -eq 2 ]] || { usage; exit 64; }; verify_binary "$2"; exit 0 ;;
  --verify-quarantined) [[ $# -eq 2 ]] || { usage; exit 64; }; verify_quarantined "$2"; exit 0 ;;
  -h|--help) usage; exit 0 ;;
esac

require_secrets

[[ $# -eq 1 && -n "$1" ]] || { usage; exit 64; }
BINARY="$1"
[[ -f "$BINARY" ]] || fail "binary not found: $BINARY"

for name in "${REQUIRED_SECRETS[@]}"; do
  echo "::add-mask::${!name}"
done
for tool in codesign security ditto uuidgen spctl; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
xcrun notarytool --help >/dev/null 2>&1 || fail "xcrun notarytool is required"

KEYCHAIN=""
prepare_keychain

echo "==> Developer ID signing $BINARY"
codesign --force --options runtime --timestamp --keychain "$KEYCHAIN" --sign "$APPLE_DEVELOPER_ID_CERTIFICATE_IDENTITY" "$BINARY"

work="$(mktemp -d "${TMPDIR:-/tmp}/m365crawl-notary.XXXXXX")"
trap 'rm -f "$work/notary.zip"; rmdir "$work" 2>/dev/null || true' EXIT
echo "==> Submitting to Apple notary service"
ditto -c -k "$BINARY" "$work/notary.zip"
notarize_zip "$work/notary.zip"

echo "==> Verifying gates"
verify_binary "$BINARY"

echo "signing: developer-id signed, notarized and verified: $BINARY"
