#!/usr/bin/env bash
# Builds the probe's test binary into a temporary directory, runs each probe
# test alone the way a mutation engine runs a test binary directly, and
# prints its exit status and every line that names the failure.
set -u
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go -C "$here" test -c -o "$work/probe.test" . || exit 1
cd "$here" || exit 1
for t in TestPass TestError TestPanic TestGoroutinePanic TestExit1 TestExit0 TestSub; do
  out=$("$work/probe.test" -test.run "^${t}\$" -test.failfast 2>&1)
  rc=$?
  echo "== $t exit=$rc"
  printf '%s\n' "$out" | grep -E -e '--- FAIL' -e '^panic' -e '^FAIL' -e '^PASS'
done
out=$("$work/probe.test" -test.run '^TestHang$' -test.timeout=1s 2>&1)
rc=$?
echo "== TestHang with -test.timeout=1s exit=$rc"
printf '%s\n' "$out" | grep -E -e '--- FAIL' -e '^panic' -e 'running tests' -e 'TestHang'
out=$("$work/probe.test" -test.run '^TestExit0$' -test.paniconexit0 2>&1)
rc=$?
echo "== TestExit0 with -test.paniconexit0 exit=$rc"
printf '%s\n' "$out" | grep -E -e '--- FAIL' -e '^panic'
