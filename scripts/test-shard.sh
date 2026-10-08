#!/usr/bin/env bash
#
# Runs one shard of the unit tests, so CI can spread the race-detector run over several runners.
# Every top-level test, example and fuzz seed name in the module is listed once, sorted, and dealt
# round-robin into SHARDS shards; this run takes shard SHARD (1-based). The shards together run
# every test exactly once (a name that two packages share lands in one shard and runs in both
# packages there). GO_TEST_FLAGS are passed to both the listing and the run, so the listing builds
# the same test binaries the run then reuses from the build cache.
#
#   scripts/test-shard.sh SHARD SHARDS [GO_TEST_FLAG...]
#   scripts/test-shard.sh --selftest
set -euo pipefail

# pick SHARD SHARDS: reads names on stdin and prints the ones that belong to SHARD.
pick() {
  sort -u | awk -v shard="$1" -v shards="$2" 'NF { if (n % shards == shard - 1) print; n++ }'
}

selftest() {
  local names all shards s got
  names="$(printf 'Test%s\n' C A B A D E F G H I J K L M N O P Q R S T U V W X Y Z)"
  all="$(printf '%s\n' "$names" | sort -u)"
  for shards in 1 2 3 4 7 30; do
    got=""
    for s in $(seq 1 "$shards"); do
      got+="$(printf '%s\n' "$names" | pick "$s" "$shards")"$'\n'
    done
    # Every name in exactly one shard: the shards' union, kept with duplicates, is the sorted set.
    if [[ "$(printf '%s' "$got" | sed '/^$/d' | sort)" != "$all" ]]; then
      echo "error: selftest: $shards shards do not partition the names" >&2
      exit 1
    fi
  done
  if [[ -n "$(printf '%s\n' "$names" | pick 30 30)" ]]; then
    echo "error: selftest: a shard past the last name should be empty" >&2
    exit 1
  fi
  echo "test-shard selftest: ok"
}

if [[ "${1:-}" == "--selftest" ]]; then
  selftest
  exit 0
fi

if [[ $# -lt 2 || ! "$1" =~ ^[1-9][0-9]*$ || ! "$2" =~ ^[1-9][0-9]*$ || "$1" -gt "$2" ]]; then
  echo "usage: scripts/test-shard.sh SHARD SHARDS [GO_TEST_FLAG...]  (1 <= SHARD <= SHARDS)" >&2
  exit 2
fi
shard="$1"
shards="$2"
shift 2

cd "$(dirname "$0")/.."
listing="$(go test "$@" -list . ./...)"
names="$(printf '%s\n' "$listing" | grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' | pick "$shard" "$shards")"
if [[ -z "$names" ]]; then
  echo "test-shard: shard $shard of $shards has no tests"
  exit 0
fi
total="$(printf '%s\n' "$listing" | grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' | sort -u | wc -l | tr -d ' ')"
echo "test-shard: shard $shard of $shards runs $(printf '%s\n' "$names" | wc -l | tr -d ' ') of $total test names"
go test "$@" -run "^($(printf '%s\n' "$names" | paste -sd '|' -))\$" ./...
