#!/usr/bin/env bash
# Usage: hand-mutate.sh <line> <from> <to>
# Applies one textual replacement on one line of btree_generic.go in a fresh
# copy of google/btree v1.1.3, then runs the package's tests verbosely. Prints
# whether the copy builds, and the test verdict.
set -uo pipefail
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
line="$1"
from="$2"
to="$3"
run="$W/hand/btree-$line"
rm -rf "$run"
mkdir -p "$W/hand" "$W/tmp"
cp -a "$W/targets/google_btree@v1.1.3" "$run"
export GOCACHE="$W/hand/gocache" TMPDIR="$W/tmp" GOWORK=off GOFLAGS= GOMAXPROCS=4
python3 - "$run/btree_generic.go" "$line" "$from" "$to" <<'EOF'
import sys
path, line, frm, to = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
lines = open(path).read().split("\n")
old = lines[line - 1]
if old.count(frm) != 1:
    sys.exit(f"line {line} contains {old.count(frm)} copies of {frm!r}: {old!r}")
lines[line - 1] = old.replace(frm, to)
open(path, "w").write("\n".join(lines))
print(f"line {line}: {old.strip()!r} -> {lines[line - 1].strip()!r}")
EOF
if ! go -C "$run" test -c -o /dev/null . 2>&1; then
  echo "verdict: does not build"
  exit 0
fi
go -C "$run" test -count=1 -v . >"$run.log" 2>&1
rc=$?
grep -c -e '^--- FAIL' "$run.log" | sed 's/^/failing tests: /'
grep -e '^--- FAIL' "$run.log" | head -5
echo "go test exit=$rc"
if [ "$rc" -eq 0 ]; then echo "verdict: survived"; else echo "verdict: killed"; fi
