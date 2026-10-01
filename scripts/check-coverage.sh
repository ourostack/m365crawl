#!/usr/bin/env bash
# Enforce 100% statement coverage of every function in internal/... .
# Carve-outs live in coverage/allow/<package>.txt (see coverage/allow/README.md).
# COVERAGE_PACKAGES overrides the package list (default: ./internal/...).
set -euo pipefail

cd "$(dirname "$0")/.."
export GOWORK=off

modpath="$(go list -m)"
packages="${COVERAGE_PACKAGES:-./internal/...}"
tmp="$(mktemp -d)"
profile="$tmp/cover.out"

# shellcheck disable=SC2086
go test -count=1 -coverprofile="$profile" $packages >"$tmp/test.log" 2>&1 || {
	cat "$tmp/test.log"
	echo "check-coverage: go test failed" >&2
	exit 1
}
go tool cover -func="$profile" | grep -v '^total:' >"$tmp/func.txt"

# Functions below 100%, as "<repo-relative file>:<FuncName>".
awk -v mod="$modpath/" '$NF != "100.0%" {
	loc = $1; sub(/:[0-9]+:$/, "", loc); sub(/:[0-9]+:[0-9]+:$/, "", loc)
	sub("^" mod, "", loc)
	print loc ":" $2
}' "$tmp/func.txt" | sort -u >"$tmp/below.txt"

# Every function seen at all, to detect stale entries.
awk -v mod="$modpath/" '{
	loc = $1; sub(/:[0-9]+:$/, "", loc); sub(/:[0-9]+:[0-9]+:$/, "", loc)
	sub("^" mod, "", loc)
	print loc ":" $2
}' "$tmp/func.txt" | sort -u >"$tmp/all.txt"

# Package directories under test, as repo-relative paths.
# shellcheck disable=SC2086
go list -f '{{.ImportPath}}' $packages | sed "s#^$modpath/##" | sort -u >"$tmp/pkgs.txt"

: >"$tmp/allowed.txt"
status=0
while IFS= read -r pkg; do
	name="${pkg//\//-}"
	file="coverage/allow/$name.txt"
	[ -f "$file" ] || continue
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in '' | '#'*) continue ;; esac
		key="${line%% *}"
		reason="${line#"$key"}"
		reason="${reason# }"
		if [ -z "$reason" ]; then
			echo "FAIL $file: entry has no reason: $key" >&2
			status=1
		fi
		echo "$key" >>"$tmp/allowed.txt"
		if ! grep -Fxq -- "$key" "$tmp/all.txt"; then
			echo "FAIL $file: stale entry, function no longer exists: $key" >&2
			status=1
		elif ! grep -Fxq -- "$key" "$tmp/below.txt"; then
			echo "FAIL $file: stale entry, function is already 100% covered: $key" >&2
			status=1
		fi
	done <"$file"
done <"$tmp/pkgs.txt"
sort -u "$tmp/allowed.txt" -o "$tmp/allowed.txt"

uncovered="$(grep -Fxv -f "$tmp/allowed.txt" "$tmp/below.txt" || true)"
if [ -n "$uncovered" ]; then
	echo "FAIL functions below 100% statement coverage and not in coverage/allow:" >&2
	while IFS= read -r key; do
		grep -F -- "${key##*:}" "$tmp/func.txt" | grep -F -- "${key%:*}" | head -1 >&2
	done <<<"$uncovered"
	status=1
fi

if [ "$status" -eq 0 ]; then
	echo "coverage OK: every function in [$(echo $packages)] is at 100% or allowlisted"
fi
exit "$status"
