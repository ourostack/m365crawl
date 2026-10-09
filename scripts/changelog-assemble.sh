#!/usr/bin/env bash
#
# Builds a release's CHANGELOG.md section from the fragments in changes/. A pull request adds
# one fragment, changes/<short-slug>.md, instead of editing CHANGELOG.md, so two pull requests
# never conflict on the same lines. A fragment holds Keep a Changelog subsections: a line
# "### Added", "### Changed", "### Deprecated", "### Removed", "### Fixed" or "### Security",
# then "- " bullets (a line indented with spaces continues the bullet above it). See
# changes/README.md.
#
#   scripts/changelog-assemble.sh VERSION DATE   writes "## [VERSION] - DATE" under "## [Unreleased]"
#   scripts/changelog-assemble.sh --check        what CI runs: [Unreleased] is empty and every fragment is valid
#   scripts/changelog-assemble.sh --selftest     runs every case against a throwaway directory
#
# Assembling reads every changes/*.md except README.md, sorted by name, merges the bullets by
# subsection in the order Added, Changed, Deprecated, Removed, Fixed, Security, writes the new
# section directly under "## [Unreleased]" and deletes the fragments it used. It fails, and
# changes nothing, when there are no fragments, when a fragment has an unknown heading, a bullet
# outside a subsection, a subsection without bullets or any other line, when [Unreleased] has
# entries, or when CHANGELOG.md already has a section for VERSION.
#
#   ROOT   the directory that holds CHANGELOG.md and changes/ (default: the repository root)
set -euo pipefail
export LC_ALL=C

order="Added Changed Deprecated Removed Fixed Security"

fail() {
  if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
    echo "::error title=Changelog::$*"
  fi
  echo "error: $*" >&2
  exit 1
}

root() {
  if [[ -n "${ROOT:-}" ]]; then
    echo "$ROOT"
  else
    cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
  fi
}

# fragments DIR prints each fragment path, sorted by name.
fragments() {
  local f
  for f in "$1"/changes/*.md; do
    [[ -e "$f" ]] || continue
    [[ "$(basename "$f")" != README.md ]] || continue
    echo "$f"
  done | sort
}

# parse FILE prints one line per bullet, "Subsection<TAB>text", with continuation lines joined
# by a \001 byte; or fails with the file, line and reason.
parse() {
  awk -v file="$1" -v order="$order" '
    BEGIN { n = split(order, names, " "); for (i = 1; i <= n; i++) known[names[i]] = 1 }
    function flush() { if (bullet != "") { print section "\t" bullet; bullet = "" } }
    function bad(why) { printf "%s:%d: %s\n", file, NR, why > "/dev/stderr"; failed = 1; exit 1 }
    /^[ \t]*$/ { next }
    /^### / {
      flush()
      if (section != "" && count == 0) bad("### " section " has no bullets")
      name = substr($0, 5); sub(/[ \t]+$/, "", name)
      if (!(name in known)) bad("unknown heading \"" $0 "\" (use ### " names[1] ", " names[2] ", " names[3] ", " names[4] ", " names[5] " or " names[6] ")")
      section = name; count = 0; next
    }
    /^- / {
      flush()
      if (section == "") bad("bullet outside a subsection (put a ### heading above it)")
      bullet = $0; count++; next
    }
    /^[ \t]+[^ \t]/ {
      if (bullet == "") bad("indented line that does not continue a bullet")
      bullet = bullet "\001" $0; next
    }
    { bad("unexpected line \"" $0 "\" (a fragment holds ### headings and - bullets only)") }
    END {
      if (failed) exit 1
      flush()
      if (section == "") { printf "%s: no entries\n", file > "/dev/stderr"; exit 1 }
      if (count == 0) { printf "%s: ### %s has no bullets\n", file, section > "/dev/stderr"; exit 1 }
    }
  ' "$1"
}

# unreleased_entries CHANGELOG prints the non-blank lines between "## [Unreleased]" and the next "## ".
unreleased_entries() {
  awk '
    /^## \[Unreleased\][ \t]*$/ { inside = 1; seen = 1; next }
    /^## / { inside = 0 }
    inside && !/^[ \t]*$/ { print }
    END { if (!seen) exit 2 }
  ' "$1"
}

require_unreleased_empty() {
  local log="$1" entries status=0
  entries="$(unreleased_entries "$log")" || status=$?
  [[ "$status" -ne 2 ]] || fail "CHANGELOG.md has no \"## [Unreleased]\" heading"
  [[ -z "$entries" ]] || fail "CHANGELOG.md has entries under ## [Unreleased]. Add a fragment in changes/ instead (see changes/README.md); a release moves the fragments into its version section with scripts/changelog-assemble.sh. Lines found: $(tr '\n' ' ' <<<"$entries")"
}

check() {
  local dir f why
  dir="$(root)"
  require_unreleased_empty "$dir/CHANGELOG.md"
  while IFS= read -r f; do
    [[ -n "$f" ]] || continue
    why="$(parse "$f" 2>&1 > /dev/null)" || fail "invalid fragment: $why"
  done <<<"$(fragments "$dir")"
  echo "changelog: [Unreleased] is empty and every fragment in changes/ is valid"
}

assemble() {
  local version="$1" date="$2" dir log list f parsed="" out why section tmp
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "VERSION must look like X.Y.Z (got \"$version\")"
  [[ "$date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "DATE must look like YYYY-MM-DD (got \"$date\")"
  dir="$(root)"
  log="$dir/CHANGELOG.md"
  [[ -f "$log" ]] || fail "no CHANGELOG.md in $dir"
  if awk -v v="## [$version]" 'index($0, v) == 1 { found = 1 } END { exit !found }' "$log"; then
    fail "CHANGELOG.md already has a section for $version"
  fi
  require_unreleased_empty "$log"
  list="$(fragments "$dir")"
  [[ -n "$list" ]] || fail "no fragments in changes/: nothing to release (each pull request adds changes/<short-slug>.md)"
  while IFS= read -r f; do
    why="$(parse "$f" 2>&1 > /dev/null)" || fail "invalid fragment: $why"
    out="$(parse "$f")"
    parsed="$parsed$out"$'\n'
  done <<<"$list"

  tmp="$(mktemp "$dir/.CHANGELOG.md.XXXXXX")"
  {
    echo
    echo "## [$version] - $date"
    for section in $order; do
      out="$(awk -F '\t' -v s="$section" '$1 == s { sub(/^[^\t]*\t/, ""); print }' <<<"$parsed")"
      [[ -n "$out" ]] || continue
      echo
      echo "### $section"
      echo
      tr '\001' '\n' <<<"$out"
    done
  } > "$tmp.section"
  awk -v section="$tmp.section" '
    { print }
    /^## \[Unreleased\][ \t]*$/ && !done { while ((getline line < section) > 0) print line; done = 1 }
  ' "$log" > "$tmp"
  rm "$tmp.section"
  mv "$tmp" "$log"
  while IFS= read -r f; do
    rm "$f"
  done <<<"$list"
  echo "CHANGELOG.md: added ## [$version] - $date from $(wc -l <<<"$list" | tr -d ' ') fragment(s); removed them from changes/"
}

selftest() {
  local self status out bad
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/changelog-selftest.XXXXXX")"
  trap 'rm -r "${tmp:?}"' EXIT
  st_fail() { echo "error: selftest: $*" >&2; exit 1; }
  fresh() {
    rm -rf "${tmp:?}/r"
    mkdir -p "$tmp/r/changes"
    printf '# Changelog\n\nIntro.\n\n## [Unreleased]\n\n## [0.1.0] - 2026-10-01\n\n### Added\n\n- First.\n' > "$tmp/r/CHANGELOG.md"
    printf '# Fragments\n\nNot a fragment.\n' > "$tmp/r/changes/README.md"
  }
  run() { # run ARGS...: sets status and out
    status=0
    out="$(ROOT="$tmp/r" GITHUB_ACTIONS='' "$self" "$@" 2>&1)" || status=$?
  }
  expect_fail() { # expect_fail WHAT MESSAGE ARGS...
    local what="$1" msg="$2"
    shift 2
    run "$@"
    [[ "$status" -ne 0 ]] || st_fail "$what: should fail"
    grep -Fq -- "$msg" <<<"$out" || st_fail "$what: expected \"$msg\" in: $out"
  }

  # Two fragments merge by subsection in the fixed order, sorted by file name.
  fresh
  printf '### Fixed\n\n- B fixed.\n\n### Added\n\n- B added.\n  More about B.\n' > "$tmp/r/changes/b-two.md"
  printf '### Security\n- A secured.\n### Added\n- A added.\n### Changed\n- A changed.\n' > "$tmp/r/changes/a-one.md"
  cp "$tmp/r/CHANGELOG.md" "$tmp/before"
  run 0.2.0 2026-10-09
  [[ "$status" -eq 0 ]] || st_fail "assemble should pass: $out"
  printf '# Changelog\n\nIntro.\n\n## [Unreleased]\n\n## [0.2.0] - 2026-10-09\n\n### Added\n\n- A added.\n- B added.\n  More about B.\n\n### Changed\n\n- A changed.\n\n### Fixed\n\n- B fixed.\n\n### Security\n\n- A secured.\n\n## [0.1.0] - 2026-10-01\n\n### Added\n\n- First.\n' > "$tmp/want"
  diff -u "$tmp/want" "$tmp/r/CHANGELOG.md" >&2 || st_fail "assembled CHANGELOG.md differs"
  [[ ! -e "$tmp/r/changes/a-one.md" && ! -e "$tmp/r/changes/b-two.md" ]] || st_fail "used fragments should be deleted"
  [[ -e "$tmp/r/changes/README.md" ]] || st_fail "changes/README.md should stay"
  run --check
  [[ "$status" -eq 0 ]] || st_fail "--check after a release should pass: $out"

  # The section already exists (the same run again, with a new fragment).
  printf '### Fixed\n\n- C.\n' > "$tmp/r/changes/c.md"
  expect_fail "existing version" "already has a section for 0.2.0" 0.2.0 2026-10-09
  [[ -e "$tmp/r/changes/c.md" ]] || st_fail "a refused run should keep the fragments"

  # No fragments.
  fresh
  expect_fail "no fragments" "no fragments in changes/" 0.2.0 2026-10-09
  cmp -s "$tmp/r/CHANGELOG.md" "$tmp/before" || st_fail "a refused run should leave CHANGELOG.md unchanged"

  # Invalid fragments, each refused by assemble and by --check, changing nothing.
  for bad in 'unknown heading:### Improved\n- X.\n' \
    'bullet outside a subsection:- X.\n' \
    'has no bullets:### Added\n### Fixed\n- X.\n' \
    'has no bullets:### Added\n' \
    'unexpected line:### Added\nSome prose.\n' \
    'does not continue a bullet:### Added\n  indented.\n' \
    'no entries:\n'; do
    fresh
    printf '### Added\n\n- Good.\n' > "$tmp/r/changes/a.md"
    # shellcheck disable=SC2059 # the case is a printf format on purpose.
    printf -- "${bad#*:}" > "$tmp/r/changes/b.md"
    expect_fail "fragment: ${bad%%:*}" "${bad%%:*}" 0.2.0 2026-10-09
    grep -Fq "changes/b.md" <<<"$out" || st_fail "the error should name the fragment: $out"
    cmp -s "$tmp/r/CHANGELOG.md" "$tmp/before" || st_fail "a refused run should leave CHANGELOG.md unchanged (${bad%%:*})"
    [[ -e "$tmp/r/changes/a.md" ]] || st_fail "a refused run should keep the fragments (${bad%%:*})"
    expect_fail "--check fragment: ${bad%%:*}" "${bad%%:*}" --check
  done

  # Bad arguments.
  fresh
  printf '### Added\n\n- Good.\n' > "$tmp/r/changes/a.md"
  expect_fail "bad version" "VERSION must look like" v0.2.0 2026-10-09
  expect_fail "bad date" "DATE must look like" 0.2.0 09-10-2026
  run 0.2.0-rc.1 2026-10-09
  [[ "$status" -eq 0 ]] || st_fail "a prerelease version should pass: $out"
  grep -q '^## \[0\.2\.0-rc\.1\] - 2026-10-09$' "$tmp/r/CHANGELOG.md" || st_fail "prerelease section missing"

  # --check: entries under [Unreleased] are refused with a pointer to changes/; empty passes.
  fresh
  run --check
  [[ "$status" -eq 0 ]] || st_fail "--check with no fragments should pass: $out"
  printf '### Fixed\n\n- Good.\n' > "$tmp/r/changes/a.md"
  run --check
  [[ "$status" -eq 0 ]] || st_fail "--check with a valid fragment should pass: $out"
  awk '{ print } /^## \[Unreleased\]$/ { print ""; print "### Added"; print ""; print "- Edited by hand." }' "$tmp/before" > "$tmp/r/CHANGELOG.md"
  expect_fail "--check entries under [Unreleased]" "Add a fragment in changes/" --check
  expect_fail "assemble with entries under [Unreleased]" "Add a fragment in changes/" 0.2.0 2026-10-09
  printf '# Changelog\n\n## [0.1.0] - 2026-10-01\n' > "$tmp/r/CHANGELOG.md"
  expect_fail "--check without [Unreleased]" "no \"## [Unreleased]\" heading" --check

  # The repository itself.
  ROOT='' "$self" --check > /dev/null || st_fail "the repository's CHANGELOG.md or changes/ is invalid"
  echo "changelog-assemble selftest: ok"
}

case "${1:-}" in
  --selftest) selftest ;;
  --check) check ;;
  *)
    [[ $# -eq 2 ]] || fail "usage: scripts/changelog-assemble.sh VERSION DATE (or --check, --selftest)"
    assemble "$1" "$2"
    ;;
esac
