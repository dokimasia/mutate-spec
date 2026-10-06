# The mutation-testing prototype and its harness

This directory contains the evidence behind
[Research-0001](../0001-mutation-testing-as-a-library-from-one-build.md): gomut,
a Go prototype of mutation testing as a library, and the scripts that measured
it against four other Go mutation tools.

## Contents

- `gomut/` is the prototype, a Go module named `example.com/gomut`:
  - Package `gomut` applies the five operator classes of Google's mutation
    testing service to one package. It checks the mutants in one test binary
    behind a runtime switch, or with one build per mutant.
  - `gomut.Check(t, dir)` runs the check from `go test`.
  - `cmd/measure` runs one mode on one package and writes one JSON line per
    mutant.
  - `cmd/count` lists the mutants of gremlins' five default operators in a
    package and type-checks each one.
- `harness/` contains the scripts that produced the research's figures.
- `probes/` contains three probes behind figures in mutate-spec's operator
  catalogue and mutate-go's engine design:
  - `loader` type-checks a package with the standard library alone, from the
    export data that `go list -export` reports.
  - `failures` runs a test binary directly and shows the exit status and the
    output for each kind of test failure.
  - `negations` counts a package's unary minus expressions that are not
    constants, the sites of `uoi-minus`.

## Requirements

- Go 1.27.1, Python 3, bash and bc.
- Network access to the Go module proxy, for the measured packages and the
  tools.
- Linux. The scripts call `du -sb` and `readlink -f`.

## Running the measurements

The scripts write everything to `$GOMUT_WORK`, which defaults to
`$XDG_CACHE_HOME/mutate-research`, or to
`~/.cache/mutate-research` when `XDG_CACHE_HOME` is unset. A build per
mutant grows a private build cache by up to 1.1 GB per package.

Run each script under a memory and CPU limit. Some of kanly's mutants on btree
allocate without bound, and one test binary grew to 8.2 GB. With systemd:

```sh
systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0 -p CPUQuota=400% -p OOMPolicy=continue harness/rebench.sh
```

`harness/setup.sh` runs first. It copies the five measured packages from the
module proxy, installs gremlins v0.6.0, mutest v0.6.2 and kanly v0.1.0, and
builds the prototype's commands. After it, these commands give each figure:

| Figure in the research | Commands |
|---|---|
| Both modes on three packages, the build cache, the overhead and the library form | `harness/rebench.sh`, then `harness/final.py <target>` for each package |
| Survivors per operator class | `harness/by-kind.py <target>` |
| The comparison of five tools on btree | `harness/h2h-all.sh google_btree@v1.1.3 gomut-five gomut gremlins ooze mutest-go120 kanly` |
| The 126 mutants of gremlins' operators on btree | `"$GOMUT_WORK/bin/count" -dir "$GOMUT_WORK/targets/google_btree@v1.1.3" > "$GOMUT_WORK/h2h/count-btree.jsonl"`, then `harness/reconcile.py` |
| kanly's operators outside gremlins' set | `harness/extra-ops.py`, after the comparison and the count |
| The gap at line 523 | `harness/hand-mutate.sh 523 'return hit, false' 'return hit, true'` |
| Verdicts that differ between runs | `harness/flaky.sh google_btree@v1.1.3 10 142 343`, after `rebench.sh` |
| The standard-library loader on five packages | `go -C probes/loader run . -dir "$GOMUT_WORK/targets/<target>"` for each target, with `google_go-cmp@v0.7.0/cmp` for go-cmp |
| A test binary's exit status and output per kind of failure | `probes/failures/run.sh` |
| The unary minus sites of five packages | `go -C probes/negations run . -dir "$GOMUT_WORK/targets/<target>"` for each target, with `google_go-cmp@v0.7.0/cmp` for go-cmp |

A target is a directory name under `$GOMUT_WORK/targets`, such as
`google_btree@v1.1.3`, `dustin_go-humanize@v1.1.0`,
`shopspring_decimal@v1.4.0`, `bits-and-blooms_bitset@v1.25.0` or
`google_go-cmp@v0.7.0`.

## Differences from the published figures

- btree's tests seed their random keys from the clock, and go-humanize ranges
  over a map while it initializes. A few verdicts differ from one run to the
  next.
- The research took its decimal sample before the prototype stopped running
  connector swaps that go vet rejects. The current prototype leaves those out
  of its list, so `-every 36` selects a different sample of decimal's
  mutants.
- The mutant IDs in the research are those of the current prototype. Mutants
  440 on go-humanize and 1333 on decimal ran in the research and are not
  viable now. `final.py` takes IDs to leave out of both modes.
