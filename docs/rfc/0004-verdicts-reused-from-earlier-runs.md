---
rfc: 0004
title: Verdicts reused from earlier runs
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Accepted
created: 2026-10-28
updated: 2026-10-28
discussion: https://github.com/dokimasia/mutate-spec/issues/35
supersedes: none
superseded-by: none
produces-adr: tbd
---

# RFC-0004: Verdicts reused from earlier runs

## Summary

A run can read the records of the earlier runs of its target, and reuse
the verdict of each mutant whose inputs have not changed since the run that
observed it. The run tests the mutants without a reusable verdict first. It
spends the time that they leave on the reused verdicts, so each verdict is
observed again once per cycle of runs. A run that the caller's deadline ends
keeps the verdicts of the mutants that it tested, and the next run starts
after them. A target whose full run exceeds the time of one CI job then
gets a verdict for each of its mutants within one cycle of runs.

## Motivation

ergon's nightly workflow gives the mutation of a module 60 minutes, and
GitHub cancels a job at its limit. Every nightly run of eidos, mutate-go,
core, kanon and sharder from 2026-10-22 to 2026-10-26 ended as cancelled.
One worker tests 0.1 to 0.3 mutants a second in their large targets:

| Repository | Where the run stood at the limit of 60 minutes |
|---|---|
| thesm-os/core | Four packages done, and mutant 714 of 1,004 in `btree` |
| thesm-os/kanon | `internal/kanon` at mutant 287 of 5,800, with 16 h 24 min left |
| dokimasia/mutate-go | `cmd/dokimi-mutate-go` at mutant 145 of 516, with 2 h 21 min left |

GitHub ends a job on a hosted runner after 6 hours, so no limit of a job
lets kanon test every mutant of `internal/kanon` in one run. A deadline of
the caller does not help either. RFC-0002 starts the mutants in key order,
so every run that a deadline ends tests the same first keys.

dokimasia/mutate-go#32 asked for the reuse of a whole record whose `inputs`
digest is unchanged, and was closed as not planned. The digest does not
cover the environment variables that the tests read, or the files outside
the suite's targets that they open. A reused record would keep the verdicts
of the earlier inputs until the digest changed. This proposal reuses single
verdicts and observes each of them again within a bounded number of runs.
A change of an input that the digests leave out then affects every verdict
within one cycle.

PIT and StrykerJS reuse verdicts by the same rules:

- PIT reuses a kill while neither the class under test nor the killing test
  changed. It reuses a survivor while no new test covers it and none of the
  covering tests changed. PIT calls the assumption behind its analysis
  "currently unproven".
- StrykerJS reuses a kill while the test that killed it exists and did not
  change. It reuses any other verdict while no new test covers the mutant
  and no test changed. Its documentation lists the changes that it does not
  detect: files other than the mutated files and the test files, the
  dependencies, the environment variables, and `.snap` files.
- StrykerJS saves the results of an interrupted run, and the next run
  continues from them.

## Detailed design

### Components

| Component | Responsibility |
|---|---|
| The history | A directory with one record of each target, the newest |
| The inputs of a verdict | The digests that decide whether a verdict is reused |
| The order of the runs | Which mutants a run tests first, and how it spends the time that is left |
| The record, version 2 | The digests of the runs that observed the record's verdicts, and the run that observed each verdict |
| The corpus | Cases that run a fixture against a history |

### The history

The caller passes the engine a directory. Each language's engine chooses the
name of the option. ergon's RFC-0011 calls it `--history` in
`dokimi-mutate-go`.

- The directory contains at most one record of each target, under the file
  name that the engine gives a record of the target.
- The engine reads the record of its target before step 4 of RFC-0002. A
  record that does not parse, or that states another record version or
  another engine, does not count as history. The engine writes a note of it
  on its standard error, and the run continues without reused verdicts.
- The engine writes the record of its run into the directory in place of
  the earlier one, through a new file that it renames, so an interrupted
  write leaves the earlier record intact.
- A run with a run error does not write into the directory. Its record
  would state no verdict that a later run could reuse, and the earlier
  record still has them.
- A run that reuses verdicts takes no selection of lines. A selection
  would leave the other mutants without a verdict to keep. A limit on the
  number of mutant runs applies in the order of the runs, as the section
  on that order states.

The run's record contains every mutant of the target, each with the verdict
that the run tested or reused. The record that the run writes is then the
history of the next run.

### The inputs of a verdict

Each run computes a digest of each file of the suite and a digest of the
requirements:

- **The files.** Every file of the target and of each other target of the
  suite, as RFC-0002 defines a target's files, and every file of each target
  of the same project that the target imports. A Go engine takes the code
  files, the test files and the data files of each package.
- **The requirements.** The engine's name and version, with the identity of
  its executable for a development build, the toolchain's version, the
  catalogue's and the overlay's versions, and the files that state the
  project's external dependencies. In Go these are `go.mod`, `go.sum`,
  `go.work` and `go.work.sum`.

A verdict of an earlier run is reusable when the run that observed it has
the same requirements and the same settings as this run, and its kind of
verdict passes the rule of the following table. The settings are the
suite's other targets, whether the run confirms its survivors, and whether
it includes generated files.

| Verdict of the earlier run | Reused while |
|---|---|
| `killed`, `timed-out`, `exhausted` | The mutant's file and each file that declares a test of `tests` are unchanged |
| `survived` | The mutant's file is unchanged, and no test file or data file of the suite was added, changed or removed |
| `no-coverage`, `not-viable`, `suppressed`, `not-selected` | Never. The run decides them again, because none of them costs a mutant run |
| `not-run`, `error` | Never |

- A mutant has a reusable verdict only when the history contains its key.
  A new key, such as one whose site's tokens changed, has none.
- A mutant whose site this run's opening control run did not execute gets
  `no-coverage`, whatever the history states.
- A verdict that a confirmation decided is reused only in a run that
  confirms its survivors. A survivor of a run without confirmation is not
  reused in a run that confirms.
- Each language's overlay states the file that declares a test. In Go, it
  is the file of the test's package that declares the top-level test
  function, so `TestStore/get` and `TestStore` share a file.

The unit of a change is the mutant's own file, as PIT's unit is the class
under test. A change of another file of the target, or of a target that it
imports, does not make a verdict unreusable. It moves the verdict forward in
the order of the runs, as the next section states. The commits of
`internal/kanon` decide the unit:

- From 2026-09-28 to 2026-10-28, 23 commits changed the package, on 11 of
  the 31 days. The longest gap between two such days was 12 days.
- The median commit changed 3 of the package's 35 code files, and 18 of the
  23 changed a test file.
- The history of `internal/kanon` covers every mutant after about 20 runs,
  by ergon's RFC-0011. With the target as the unit, each of those 11 days
  would have made all 5,800 verdicts unreusable, so the history would never
  have covered the package.

### The order of the runs

A run that has a history runs its mutants in three groups, after the
opening control run:

1. The mutants without a reusable verdict, in key order.
2. The mutants with a reusable verdict whose wider inputs changed since the
   run that observed it, oldest observation first, and in key order within
   one observation. The wider inputs are the files of the target and of
   the targets that it imports, beyond the files of the table's rule.
3. The other mutants with a reusable verdict, in the same order.

A mutant of group 2 or 3 gets the verdict of its new run, which replaces
the reused one. When the run ends before such a mutant's run starts, the
mutant keeps its reused verdict. Group 3 bounds the age of every verdict,
also of a verdict that depends on an input that the digests leave out. The
runs test group 1 and spend the rest of their time on the oldest verdicts,
so they observe each verdict again within one cycle. A cycle is the time of
a full run divided by the time of one run, about 20 nights for
`internal/kanon`.

A run with a history runs every mutant of the three groups when the caller
sets neither a deadline nor a limit. Such a run reuses no verdict. The caller's deadline and
the caller's limit on the number of mutant runs end the runs as in
RFC-0002, with one change. A mutant that ends the run without a verdict,
in place of a reused one, is `not-run`. A reused verdict whose new run did
not start is a verdict, so the run does not fail for it:

- The limit counts the mutant runs that start, in the order of the three
  groups. A mutant of group 2 or 3 that the limit leaves unrun keeps its
  reused verdict.
- A run fails when a mutant of group 1 is `not-run` because the caller's
  deadline passed or the caller ended the run, as RFC-0002 fails such a
  run.
- A run whose mutants of group 1 the limit leaves unrun does not fail, as
  RFC-0002 does not fail a run that its limit alone ends. Those mutants are
  `not-run`, and the score is the score of the mutants with a verdict.
- A run passes when each counted mutant has a verdict, tested or reused,
  and every other rule of RFC-0002 holds.
- The score counts reused verdicts with tested ones.

The limit makes the reuse deterministic: two runs of one target with one
history and one limit test the same mutants on every machine. A caller
that wants a budget that does not depend on the machine's speed, such as
at most 500 mutant runs a night, sets the limit in place of a deadline.

### The record, version 2

The record's `version` is `2`. A reader of version 1 would count a reused
verdict as a second observation of the same mutant, so it must not read a
record of version 2. The record gains two fields:

| Field | Value |
|---|---|
| `observations` | The runs that observed the record's verdicts, this run first: each run's `startedAt`, the `files` map from each file's path relative to `root` to its digest, and the `requirements` digest. A run that no verdict of the record names is left out |
| `mutants[].observedAt` | The `startedAt` of the run that observed the mutant's verdict, the record's own `startedAt` for a verdict of this run. Absent for a verdict that the run decided without a mutant run |

The `tests`, `coveredBy`, `seconds` and `confirmed` of a reused verdict are
those of the run that observed it.

```json
{
  "record": "dokimi-mutate",
  "version": 2,
  "startedAt": "2026-11-12T02:10:04Z",
  "observations": [
    {
      "startedAt": "2026-11-12T02:10:04Z",
      "files": { "internal/kanon/decode.go": "sha256:4be1…", "internal/kanon/decode_test.go": "sha256:90c2…" },
      "requirements": "sha256:1f07…"
    },
    {
      "startedAt": "2026-11-11T02:10:11Z",
      "files": { "internal/kanon/decode.go": "sha256:4be1…", "internal/kanon/decode_test.go": "sha256:77ad…" },
      "requirements": "sha256:1f07…"
    }
  ],
  "mutants": [
    { "key": "0a3e…", "verdict": "killed", "tests": ["TestDecode"], "seconds": 0.4, "observedAt": "2026-11-11T02:10:11Z" },
    { "key": "0b91…", "verdict": "survived", "seconds": 1.2, "observedAt": "2026-11-12T02:10:04Z" }
  ]
}
```

The excerpt shows the new fields alone. Its digests and keys are
shortened.

- `inputs` keeps its meaning: the digest of this run's inputs.
- The platform's check of unstable verdicts compares two verdicts of a key
  only when their `observedAt` differ, because a reused verdict is no new
  observation.
- `sample.before` is `null` in a run with a history. Its mutants with a
  verdict are not the first keys of the target, so the sample states their
  counts and their score, and no first key.
- A run with a history states a sample only when a mutant is `not-run`.
  When the limit leaves only mutants of groups 2 and 3 unrun, every mutant
  has a verdict. The run then states no sample, and its score counts every
  mutant.

### The corpus

A case states its history as data. Each engine's corpus runner writes the
history record from it. The record's requirements digest contains the
version of the engine and of the toolchain that run the case. No record in
mutate-spec can state that digest.

- `case.json` states `history`, with its `engine`: `same` for a history of
  the engine that runs the case, and `other` for a history whose record the
  runner writes with another engine's name.
- For each mutant in the history, `case.json` states its `earlier` verdict
  and the `observed` age of its observation, 1 for the newest earlier run.
  `reused: true` marks each mutant whose verdict the run reuses.
- `<language>/expect.json` states the history's `changed` files, relative
  to the fixture, each with the age `since` which it differs: the file
  differs in that observation and in each older one. It also states the
  `earlierTests` of each earlier kill.
- The runner writes each file's digest from the fixture's file, and another
  digest for a file that the case states as changed. It writes the
  requirements digest that the engine computes for the fixture.
- A case's `sample` is the limit of its run, as in RFC-0002.

| Case | What its history and its fixture differ in | What it proves |
|---|---|---|
| `history` | A kill whose file changed, a kill whose killing test's file changed, a kill whose other test file changed, a survivor beside a changed test file, and a mutant without coverage in the history, in a run with a limit of the new mutants alone | The rules of the table, and `no-coverage` decided again |
| `history-order` | Two observations of different ages, a changed file of the target that no mutant's rule names, and a run with a limit of two runs beyond group 1 | Groups 1, 2 and 3 in order, and a pass with reused verdicts left |
| `history-foreign` | A history of another engine | No verdict reused, and the note |

## Alternatives considered

### The reuse of a whole record

dokimasia/mutate-go#32 reused a record whose `inputs` digest is equal.

**Why not:** one changed file of the suite runs every mutant again, so a
target that changes more often than its full run takes never gets a
record. A record whose digest stays equal keeps its verdicts for as long as
it stays equal, also when an input outside the digest changed.

### The target's files as the unit of a change

ergon's RFC-0011 states the rule with the files of the package, and the
issue that requested this proposal with the files of the target.

**Why not:** the commits of `internal/kanon` changed the package on 11 of
31 days, with at most 12 days between two of them. Its history needs about
20 runs, so no history of the package would ever be complete. The order of
the runs gives a mutant whose target changed elsewhere a new run first.

### The export data of each imported package

A Go engine reads the export file of each dependency, so a digest of those
files would detect a change of an imported package.

**Why not:** with go1.27.3, the export file that `go list -export` reports
changed after each change of the package's source, a doc comment
included. The rule would make every verdict of a target unreusable after
any edit of a package that the target imports. The order of the runs
re-runs those verdicts first instead.

### A digest of each test's own source

The engine digests the function of each test in place of the file that
declares it.

**Why not:** a test calls helpers in other files of its package, so the
source of the function alone misses changes that the file misses too. PIT's
unit is the test class, and StrykerJS's is the test file. A language-aware
digest of each test would also need a parser per language.

### No new run of an unchanged verdict

The run tests only the mutants without a reusable verdict.

**Why not:** a verdict that depends on an input that the digests leave out
would then be reused without a bound. That is the reason that
dokimasia/mutate-go#32 was declined. Group 3 costs only the time that group
1 leaves.

### A write of the history after each verdict

**Why not:** the record of a large target has thousands of mutants. A
rewrite per verdict writes the whole record each time. ergon's nightly job
ends the run with its timeout before GitHub's limit. An interrupt ends a
run with its record written.

## Drawbacks

- **A reused kill can be stale for one cycle.** A change of another file of
  the target can turn a kill into a survivor. The run reuses the kill until
  the mutant's new run in group 2. Group 2 follows the new mutants, so a run
  whose deadline ends during group 1 reuses the kill. How often a change of
  another file turns a kill into a survivor is not measured.
- **A history fills over a cycle.** A target without a history fails each
  run until group 1 is empty: about 20 nights for `internal/kanon`.
- **A new engine version or toolchain resets the history.** Each release of
  an engine makes every verdict of the target unreusable, so a large
  target fails until its history fills again.
- **The history needs storage between runs.** ergon keeps it in the cache
  of GitHub Actions, which removes an entry that no job read in seven days.
  A history that expired makes the next run start over.
- **The record grows.** Each observation that a verdict names lists the
  digest of each file of the suite. A history of `internal/kanon` names at
  most the runs of one cycle.
- **The order of the runs is no longer the order of the keys.** A run with
  a history states no uniform sample, so its score with a deadline is not
  the estimate that RFC-0002 gives a sample.
- **A survivor runs again after each change of a test file.** 18 of the 23
  commits of `internal/kanon` changed a test file. The survivors are the
  share of the mutants that the tests miss, about 8% in go-humanize.

## Unresolved and future work

- Sending the history to the platform is not proposed here.

## References

| What | Where |
|---|---|
| PIT's incremental analysis: the rules of a kill and of a survivor, and "currently unproven" | https://pitest.org/quickstart/incremental_analysis/ |
| StrykerJS's incremental mode: its rules, its interrupted runs and its limitations | https://stryker-mutator.io/docs/stryker-js/incremental/ |
| ergon's RFC-0011, the nightly job and the contract of `--history` | https://github.com/dokimasia/ergon/blob/main/docs/rfc/0011-scheduled-jobs-that-finish.md |
| dokimasia/mutate-go#32, the reuse of a whole record, and the reasons that declined it | https://github.com/dokimasia/mutate-go/issues/32 |
| dokimasia/mutate-spec#35, the request of this proposal | https://github.com/dokimasia/mutate-spec/issues/35 |
| The commits of `internal/kanon` from 2026-09-28 to 2026-10-28 | https://github.com/thesm-os/kanon/commits/main/internal/kanon |
| The limit of 6 hours of a job on a hosted runner | https://docs.github.com/en/actions/reference/limits |
