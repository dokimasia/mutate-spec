#!/usr/bin/env bash
# Usage: rebench.sh
# Runs every measurement of the prototype, one at a time: both modes on the
# three packages, the instrumentation overhead with no mutant active, and the
# library form from go test, with all five operator classes and with
# gremlins' operators alone. Each step writes its own log under
# $W/rebench/.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd -P)"
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
L="$W/rebench"
mkdir -p "$L"
step() { echo "== $(date -u +%T) $*"; }
for t in 'google_btree@v1.1.3' 'dustin_go-humanize@v1.1.0'; do
  for m in schemata single; do
    step "$t $m"
    "$HERE/run-mode.sh" "$t" "$m" >"$L/$t-$m.log" 2>&1
    echo "exit=$?"
  done
done
t='shopspring_decimal@v1.4.0'
for m in schemata single; do
  step "$t $m every 36th"
  "$HERE/run-mode.sh" "$t" "$m" 36 >"$L/$t-$m.log" 2>&1
  echo "exit=$?"
done
step "overhead decimal"
"$HERE/overhead.sh" 'shopspring_decimal@v1.4.0' 5 1 >"$L/overhead-decimal.log" 2>&1
echo "exit=$?"
step "overhead btree"
"$HERE/overhead.sh" 'google_btree@v1.1.3' 5 20 >"$L/overhead-btree.log" 2>&1
echo "exit=$?"
for t in 'google_btree@v1.1.3' 'dustin_go-humanize@v1.1.0'; do
  step "library form $t"
  "$HERE/head2head.sh" gomut "$t" >"$L/library-$t.log" 2>&1
  echo "exit=$?"
done
step "library form, gremlins' operators, btree"
"$HERE/head2head.sh" gomut-five 'google_btree@v1.1.3' >"$L/library-five-btree.log" 2>&1
echo "exit=$?"
step done
