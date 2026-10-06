#!/usr/bin/env bash
# Usage: flaky.sh <target> <runs> <mutant-id>...
# Runs each mutant several times in both modes, against the binaries and
# overlays that run-mode.sh left behind, and prints how often each run failed.
set -uo pipefail
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
target="$1"
runs="$2"
shift 2
# The go command keys overlays by physical path, so enter the package through
# it rather than through a symbolic link.
pkg="$(readlink -f "$W/targets/$target")"
export TMPDIR="$W/tmp" GOWORK=off GOFLAGS=-mod=mod GOMAXPROCS=4
for id in "$@"; do
  s=0
  for _ in $(seq "$runs"); do
    (cd "$pkg" && GOMUT_ACTIVE="$id" GOCACHE="$W/gocache-$target-schemata" "$W/work-$target-schemata/pkg.test" -test.count=1 -test.failfast >/dev/null 2>&1) || s=$((s + 1))
  done
  m=0
  for _ in $(seq "$runs"); do
    (cd "$pkg" && GOCACHE="$W/gocache-$target-single" go test -count=1 -failfast -overlay "$W/work-$target-single/single_$id.json" . >/dev/null 2>&1) || m=$((m + 1))
  done
  echo "mutant $id: schemata failed $s of $runs runs, single failed $m of $runs runs"
done
