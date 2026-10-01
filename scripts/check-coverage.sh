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
trap 'rm -rf "$tmp"' EXIT
profile="$tmp/cover.out"

# shellcheck disable=SC2086
go test -count=1 -coverprofile="$profile" $packages >"$tmp/test.log" 2>&1 || {
	cat "$tmp/test.log"
	echo "check-coverage: go test failed" >&2
	exit 1
}
go tool cover -func="$profile" | grep -v '^total:' >"$tmp/func.txt"

# One line per function: "<repo-relative file>:<FuncName><TAB><percent><TAB><file:line>".
# go tool cover prints only the bare name, so same-named methods in one file share a key.
awk -v mod="$modpath/" '{
	loc = $1; sub(/:[0-9]+:$/, "", loc); sub(/:[0-9]+:[0-9]+:$/, "", loc)
	sub("^" mod, "", loc)
	row = $1; sub("^" mod, "", row)
	print loc ":" $2 "\t" $NF "\t" row
}' "$tmp/func.txt" >"$tmp/rows.tsv"
cut -f1 "$tmp/rows.tsv" | sort | uniq -d >"$tmp/ambiguous.txt"
cut -f1 "$tmp/rows.tsv" | sort -u >"$tmp/all.txt"
awk -F'\t' '$2 != "100.0%" {print $1}' "$tmp/rows.tsv" | sort -u >"$tmp/below.txt"

# Package paths under test, repo-relative, and every package that exists in the module.
# shellcheck disable=SC2086
go list -f '{{.ImportPath}}' $packages | sed "s#^$modpath/##" | sort -u >"$tmp/pkgs.txt"
go list -f '{{.ImportPath}}' ./... | sed "s#^$modpath/##" | sort -u >"$tmp/existing.txt"

: >"$tmp/allowed.txt"
status=0
for file in coverage/allow/*.txt; do
	[ -e "$file" ] || continue
	name="$(basename "$file" .txt)"
	pkg=""
	while IFS= read -r p; do
		if [ "${p//\//-}" = "$name" ]; then pkg="$p"; fi
	done <"$tmp/existing.txt"
	if [ -z "$pkg" ]; then
		echo "FAIL $file: orphan allow file, no Go package matches $name" >&2
		status=1
		continue
	fi
	under_test=0
	grep -Fxq -- "$pkg" "$tmp/pkgs.txt" && under_test=1
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in '' | '#'*) continue ;; esac
		key="${line%%[[:space:]]*}"
		reason="${line#"$key"}"
		reason="${reason#"${reason%%[![:space:]]*}"}"
		if [ -z "$reason" ]; then
			echo "FAIL $file: entry has no reason: $key" >&2
			status=1
		fi
		path="${key%%:*}"
		if [ "$(dirname "$path")" != "$pkg" ]; then
			echo "FAIL $file: entry names a function outside package $pkg: $key" >&2
			status=1
			continue
		fi
		[ "$under_test" -eq 1 ] || continue
		echo "$key" >>"$tmp/allowed.txt"
		if ! grep -Fxq -- "$key" "$tmp/all.txt"; then
			echo "FAIL $file: stale entry, function no longer exists: $key" >&2
			status=1
		elif grep -Fxq -- "$key" "$tmp/ambiguous.txt"; then
			echo "FAIL $file: ambiguous entry, $path has several functions named ${key#*:}; rename one so the entry names a single function: $key" >&2
			status=1
		elif ! grep -Fxq -- "$key" "$tmp/below.txt"; then
			echo "FAIL $file: stale entry, function is already 100% covered: $key" >&2
			status=1
		fi
	done <"$file"
done
sort -u "$tmp/allowed.txt" -o "$tmp/allowed.txt"

grep -Fxv -f "$tmp/allowed.txt" "$tmp/below.txt" >"$tmp/uncovered.txt" || true
if [ -s "$tmp/uncovered.txt" ]; then
	echo "FAIL functions below 100% statement coverage and not in coverage/allow:" >&2
	awk -F'\t' 'NR == FNR {bad[$1] = 1; next} ($1 in bad) && $2 != "100.0%" {print "  " $3 "  " $2 "  " $1}' \
		"$tmp/uncovered.txt" "$tmp/rows.tsv" >&2
	status=1
fi

if [ "$status" -eq 0 ]; then
	echo "coverage OK: every function in [$(echo $packages)] is at 100% or allowlisted"
fi
exit "$status"
