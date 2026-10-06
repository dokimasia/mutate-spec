#!/usr/bin/env bash
# Usage: head2head.sh <gomut|gomut-five|ooze|gremlins|mutest|mutest-go120|kanly> <target>
# Runs one mutation tool on a fresh copy of one target package, one mutant at
# a time, with a private build cache warmed by one ordinary test run, and
# reports the wall time and the cache's growth. The library forms, gomut and
# ooze, also have their own test binary compiled during the warm-up, so the
# timed run measures the mutants and not the first compile of the library.
# The tool's own output goes to $W/h2h/<tool>-<target>.out.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd -P)"
PROTO="$(cd "$HERE/../gomut" && pwd -P)"
W="${GOMUT_WORK:-${XDG_CACHE_HOME:-$HOME/.cache}/mutate-research}"
tool="$1"
target="$2"
run="$W/h2h/$tool-$target"
cache="$W/h2h/gocache-$tool-$target"
rm -rf "$run" "$cache"
mkdir -p "$W/h2h" "$cache" "$W/tmp"
cp -a "$W/targets/$target" "$run"
export GOCACHE="$cache" TMPDIR="$W/tmp" GOWORK=off GOMAXPROCS=4 GOFLAGS=
cd "$run"
pkg=$(go list -f '{{.Name}}' .)
warm=(go test -count=1 .)
case "$tool" in
gomut | gomut-five)
  # gomut-five applies only gremlins' default operators, for a like-for-like
  # comparison; gremlins' INVERT_NEGATIVES has no counterpart here.
  if [ "$tool" = gomut-five ]; then
    export GOMUT_OPERATORS=aor,ror-boundary,ror-negation,uoi-incdec
  fi
  go mod edit -replace=example.com/gomut="$PROTO"
  go get example.com/gomut@v0.0.0 >/dev/null 2>&1
  printf '%s\n' '//go:build mutation' '' "package $pkg" '' 'import (' '	"testing"' '' '	"example.com/gomut"' ')' '' 'func TestMutation(t *testing.T) {' '	gomut.Check(t, ".")' '}' >zz_mutation_test.go
  go mod tidy >/dev/null 2>&1
  warm=(go test -count=1 -tags mutation -run '^$' .)
  cmd=(go test -count=1 -tags mutation -run '^TestMutation$' -v -timeout 0 .)
  ;;
ooze)
  go get github.com/gtramontina/ooze@v0.2.0 >/dev/null 2>&1
  printf '%s\n' '//go:build mutation' '' "package ${pkg}_test" '' 'import (' '	"testing"' '' '	"github.com/gtramontina/ooze"' '	"github.com/gtramontina/ooze/viruses/arithmetic"' '	"github.com/gtramontina/ooze/viruses/comparison"' '	"github.com/gtramontina/ooze/viruses/comparisoninvert"' ')' '' 'func TestMutation(t *testing.T) {' '	ooze.Release(t,' '		ooze.WithTestCommand("go test -count=1 -timeout 30s ./..."),' '		ooze.WithViruses(arithmetic.New(), comparison.New(), comparisoninvert.New()),' '	)' '}' >zz_mutation_test.go
  go mod tidy >/dev/null 2>&1
  warm=(go test -count=1 -tags mutation -run '^$' .)
  cmd=(go test -count=1 -tags mutation -run '^TestMutation$' -v -timeout 0 .)
  ;;
gremlins)
  # gremlins v0.6.0 takes the first line of go.mod as the module name
  # (internal/gomodule/gomodule.go), so a leading comment matches no coverage.
  python3 -c 'import re,sys; p=sys.argv[1]; s=open(p).read(); open(p,"w").write(s[re.search(r"^module ", s, re.M).start():])' go.mod
  cmd=("$W/bin/gremlins" unleash --workers 1 "$run")
  ;;
mutest)
  cmd=("$W/bin/mutest" -workers 1 -json -v .)
  ;;
mutest-go120)
  # mutest refuses a module whose go line is below 1.20.
  go mod edit -go=1.20
  cmd=("$W/bin/mutest" -workers 1 -json -v .)
  ;;
kanly)
  cmd=("$W/bin/kanly" --jobs=1 --format=json .)
  ;;
*)
  echo "unknown tool $tool" >&2
  exit 2
  ;;
esac
"${warm[@]}" >/dev/null
before=$(du -sb "$cache" | cut -f1)
echo "tool=$tool target=$target go=$(go env GOVERSION) go-line=$(go mod edit -json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Go"])') GOMAXPROCS=$GOMAXPROCS start=$(date -u +%FT%TZ)"
echo "command: ${cmd[*]}"
start=$(date +%s.%N)
set +e
"${cmd[@]}" >"$W/h2h/$tool-$target.out" 2>&1
rc=$?
set -e
end=$(date +%s.%N)
after=$(du -sb "$cache" | cut -f1)
echo "exit=$rc wall=$(python3 -c "print(f'{$end - $start:.2f}')")s"
echo "build cache: $before bytes before the run, $after after, growth $((after - before)) bytes"
echo "end=$(date -u +%FT%TZ)"
