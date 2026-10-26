#!/usr/bin/env bash
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
work="$(cd "$work" && pwd -P)"
echo "verify-homebrew selftest fixtures: $work"
mkdir -p "$work/bin" "$work/tap/Casks" "$work/prefix/bin"
cp "$here/verify-homebrew.sh" "$work/verify-homebrew.sh"
cat > "$work/bin/brew" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$SELFTEST_ROOT/commands"
case "$*" in
  "tap ourostack/tap" | "update --quiet") ;;
  "--repository ourostack/tap") printf '%s\n' "$SELFTEST_ROOT/tap" ;;
  "--prefix") printf '%s\n' "$SELFTEST_ROOT/prefix" ;;
  "install --cask ourostack/tap/m365crawl")
    [[ "${SELFTEST_INSTALL_FAIL:-0}" == 0 ]] || exit 43
    ;;
  "info --cask --json=v2 ourostack/tap/m365crawl")
    if ! grep -Fxq 'install --cask ourostack/tap/m365crawl' "$SELFTEST_ROOT/commands"; then
      echo "Refusing to load cask from untrusted tap before explicit installation" >&2
      exit 42
    fi
    printf '{"casks":[{"version":"1.2.3"}]}\n'
    ;;
  *) echo "unexpected Homebrew command: $*" >&2; exit 44 ;;
esac
SH
cat > "$work/prefix/bin/m365crawl" <<'SH'
#!/usr/bin/env bash
printf '{"version":"%s"}\n' "${SELFTEST_INSTALLED_VERSION:-1.2.3}"
SH
cat > "$work/sign-notarize.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == "--verify $SELFTEST_ROOT/prefix/bin/m365crawl" ]]
printf 'signature-check\n' >> "$SELFTEST_ROOT/commands"
[[ "${SELFTEST_SIGNATURE_FAIL:-0}" == 0 ]]
SH
chmod +x "$work/bin/brew" "$work/prefix/bin/m365crawl" "$work/sign-notarize.sh"

run_verify() {
  env SELFTEST_ROOT="$work" TAG=v1.2.3 APPLE_TEAM_ID=fixture \
    POLL_TIMEOUT_MINUTES=0 PATH="$work/bin:$PATH" "$@" bash "$work/verify-homebrew.sh"
}
fail() {
  cat "$work/output"
  echo "error: verify-homebrew selftest: $*" >&2
  exit 1
}
fixture() {
  printf 'cask "m365crawl" do\n  version "%s"\nend\n' "$1" > "$work/tap/Casks/m365crawl.rb"
  : > "$work/commands"
}

fixture 1.2.3
if ! run_verify > "$work/output" 2>&1; then
  fail "an untrusted cask must reach the existing fully qualified install without preloading Ruby"
fi
grep -Fxq 'install --cask ourostack/tap/m365crawl' "$work/commands" || fail "canonical install was omitted"
grep -Fxq 'signature-check' "$work/commands" || fail "signature verification was omitted"
if grep -Eq '^(info|trust) ' "$work/commands"; then
  fail "metadata was loaded or trust was granted separately from the canonical install"
fi

for content in 'version "1.2.2"' 'desc "no literal version"' $'version "1.2.3"\nversion "1.2.3"'; do
  printf 'cask "m365crawl" do\n%s\nend\n' "$content" > "$work/tap/Casks/m365crawl.rb"
  : > "$work/commands"
  if run_verify > "$work/output" 2>&1; then
    fail "stale, missing or ambiguous metadata passed"
  fi
  if grep -q '^install ' "$work/commands"; then
    fail "unqualified metadata reached installation"
  fi
done

for setting in SELFTEST_INSTALL_FAIL=1 SELFTEST_INSTALLED_VERSION=0.0.0 SELFTEST_SIGNATURE_FAIL=1; do
  fixture 1.2.3
  if run_verify "$setting" > "$work/output" 2>&1; then
    fail "a failed install, wrong installed version or failed signature became success"
  fi
done
echo "verify-homebrew selftests: ok"
