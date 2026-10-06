#!/usr/bin/env bash
# Usage: run-mode.sh <target> <schemata|single> [every]
# Runs one measurement mode against one target package with a private build
# cache, warmed by one ordinary test run, and reports the cache's growth during
# the mode. every samples every k-th mutant, 1 by default. The target is a
# directory name under $W/targets, such as google_btree@v1.1.3.
set -euo pipefail
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
target="$1"
mode="$2"
every="${3:-1}"
pkg="$W/targets/$target"
cache="$W/gocache-$target-$mode"
work="$W/work-$target-$mode"
rm -rf "$cache" "$work"
mkdir -p "$cache" "$work" "$W/tmp"
export GOCACHE="$cache" TMPDIR="$W/tmp" GOWORK=off GOFLAGS=-mod=mod GOMAXPROCS=4
(cd "$pkg" && go test -count=1 . >/dev/null)
before=$(du -sb "$cache" | cut -f1)
echo "target=$target mode=$mode go=$(go env GOVERSION) GOMAXPROCS=$GOMAXPROCS start=$(date -u +%FT%TZ)"
"$W/bin/measure" -pkg "$pkg" -mode "$mode" -work "$work" -every "$every"
after=$(du -sb "$cache" | cut -f1)
echo "build cache: $before bytes before the mode, $after after, growth $((after - before)) bytes"
echo "end=$(date -u +%FT%TZ)"
