#!/usr/bin/env bash
# Usage: overhead.sh <target> [rounds] [count]
# Measures what the schemata instrumentation costs a run with no mutant
# active: the package's ordinary test binary against the schemata binary with
# GOMUT_ACTIVE=0, in alternating rounds so that background load hits both.
# count repeats the suite inside one run, for suites too short to time. Run
# run-mode.sh <target> schemata first: it builds the schemata binary.
set -euo pipefail
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
target="$1"
rounds="${2:-5}"
count="${3:-1}"
pkg="$W/targets/$target"
work="$W/work-$target-schemata"
export GOCACHE="$W/gocache-$target-schemata" TMPDIR="$W/tmp" GOWORK=off GOFLAGS=-mod=mod GOMAXPROCS=4
(cd "$pkg" && go test -c -o "$work/orig.test" .)
echo "target=$target rounds=$rounds count=$count"
for i in $(seq "$rounds"); do
  for bin in orig pkg; do
    start=$(date +%s.%N)
    (cd "$pkg" && GOMUT_ACTIVE=0 "$work/$bin.test" -test.count="$count" >/dev/null)
    end=$(date +%s.%N)
    echo "round=$i binary=$bin seconds=$(echo "$end - $start" | bc)"
  done
done
