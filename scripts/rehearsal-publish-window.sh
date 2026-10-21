#!/usr/bin/env bash
# Leave a bounded observation window before a rehearsal's first tap push.
set -euo pipefail

fail() {
  echo "error: $*" >&2
  exit 1
}

window() {
  [[ "${REHEARSAL:-}" == true || "${REHEARSAL:-}" == false ]] \
    || fail "REHEARSAL must be true or false"
  [[ "${PUBLISH_ONLY:-}" == true || "${PUBLISH_ONLY:-}" == false ]] \
    || fail "PUBLISH_ONLY must be true or false"
  if [[ "$REHEARSAL" == true && "$PUBLISH_ONLY" == false ]]; then
    echo "rehearsal-publish-window: waiting 60 seconds before the initial tap push"
    sleep 60
  fi
}

selftest() {
  local self tmp rehearsal publish_only name
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/rehearsal-window-selftest.XXXXXX")"
  mkdir "$tmp/bin"
  cat > "$tmp/bin/sleep" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$WINDOW_SLEEP_LOG"
STUB
  chmod +x "$tmp/bin/sleep"
  for rehearsal in true false; do
    for publish_only in true false; do
      name="$rehearsal-$publish_only"
      REHEARSAL="$rehearsal" PUBLISH_ONLY="$publish_only" \
        WINDOW_SLEEP_LOG="$tmp/$name.sleep" PATH="$tmp/bin:$PATH" \
        bash "$self" > "$tmp/$name.out" 2> "$tmp/$name.err" \
        || fail "selftest: valid publication mode $name failed"
      if [[ "$name" == true-false ]]; then
        [[ -f "$tmp/$name.sleep" ]] || fail "selftest: an initial rehearsal must open an observation window"
        [[ "$(cat "$tmp/$name.sleep")" == 60 ]] || fail "selftest: the window must be one bounded wait"
      else
        [[ ! -e "$tmp/$name.sleep" ]] || fail "selftest: stable publication and publish-only recovery must not wait"
        [[ ! -s "$tmp/$name.out" ]] || fail "selftest: a skipped window must not claim to wait"
      fi
    done
  done
  for name in missing-rehearsal missing-publish-only invalid-rehearsal invalid-publish-only; do
    rehearsal=true
    publish_only=false
    case "$name" in
      missing-rehearsal) rehearsal="" ;;
      missing-publish-only) publish_only="" ;;
      invalid-rehearsal) rehearsal=yes ;;
      invalid-publish-only) publish_only=no ;;
    esac
    if REHEARSAL="$rehearsal" PUBLISH_ONLY="$publish_only" \
      WINDOW_SLEEP_LOG="$tmp/$name.sleep" PATH="$tmp/bin:$PATH" \
      bash "$self" > "$tmp/$name.out" 2> "$tmp/$name.err"; then
      fail "selftest: $name must fail closed"
    fi
    [[ ! -e "$tmp/$name.sleep" && ! -s "$tmp/$name.out" && -s "$tmp/$name.err" ]] \
      || fail "selftest: $name must report an error without opening a window"
  done
  echo "rehearsal-publish-window selftest: ok"
}

case "${1:-}" in
  --selftest) selftest ;;
  "") window ;;
  *) fail "usage: scripts/rehearsal-publish-window.sh [--selftest]" ;;
esac
