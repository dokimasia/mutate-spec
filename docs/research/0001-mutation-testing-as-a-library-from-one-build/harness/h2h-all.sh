#!/usr/bin/env bash
# Usage: h2h-all.sh <target> <tool>...
# Runs head2head.sh for each tool in turn, one at a time, with a log per tool
# in $W/h2h/<tool>-<target>.log.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd -P)"
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
target="$1"
shift
mkdir -p "$W/h2h"
for tool in "$@"; do
  "$HERE/head2head.sh" "$tool" "$target" >"$W/h2h/$tool-$target.log" 2>&1
  echo "$tool exit=$? $(grep -e '^exit=' "$W/h2h/$tool-$target.log")"
done
