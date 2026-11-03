---
rfc: 0005
title: The tests that kill and cover a mutant
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Accepted
created: 2026-11-03
updated: 2026-11-03
discussion: https://github.com/dokimasia/mutate-spec/issues/36, https://github.com/dokimasia/mutate-spec/issues/38
supersedes: none
superseded-by: none
produces-adr: tbd
---

# RFC-0005: The tests that kill and cover a mutant

## Summary

Each mutant's record lists the tests that killed it and the tests that cover
it. dokimi reads both lists to attribute kills to its generated checks. This
proposal fixes the tests of a kill to the first test that failed, so two
runs of the same code list the same test. It adds two modes of a run, both
off by default. In the first, each part of a mutant's run runs to its end,
and the record lists every test that failed. In the second, the engine
traces each leaf test alone, such as a Go subtest, so that `coveredBy` lists
the leaf tests that executed a site. The record states both modes. A run
reuses only the verdicts of a run with the same modes.

## Motivation

### The tests of a kill change between equal runs

The engine ends a mutant's run at the first line that states a failed test.
In Go, the failure of a subtest also fails the test that started it. The
testing package states that test's failure on a later line, when its
function returns. The engine lists each failure that the program wrote
before the kill took effect. The test that started the subtest is then in
`tests` in some runs and not in others.

dokimi-mutate-go v0.2.0 ran 20 times over one package. Its test `TestPair`
starts the subtests `max` and `min`, which check one function each:

| Mutant | Runs whose `tests` lists `TestPair` | Runs whose `tests` does not |
|---|---|---|
| `Max`: `a > b` became `false` | 12 | 8 |
| `Max`: `if a > b { return a }` removed | 9 | 11 |
| `Max`: `return a` became `return 0` | 13 | 7 |
| `Min`: `return b` became `return 0` | 19 | 1 |

The records of equal runs then differ. The platform compares the verdicts of
two records with equal inputs. A run with a history reuses a kill while each
file that declares a test of `tests` is unchanged. A list that changes
between two equal runs serves neither.

### A kill lists one test

The run of a killed mutant stops at its first failing test, so `tests` lists
one test. A test whose every kill another test also makes is redundant. A
test that alone kills a mutant is not. With the first killer alone, a test
that runs after another killer looks as if it killed nothing. dokimi runs
each generated check as a test whose name is the check's identity. It
reports a check whose killed mutants another check also kills.

### Coverage lists the top-level test

The engine runs each top-level test of a Go program alone, so `coveredBy`
lists the top-level test of a subtest. In a run of dokimi-mutate-go v0.1.0,
`TestPair` started the subtests `max` and `min`, and `TestAbs` called `Abs`:

| Site | Verdict | `coveredBy` |
|---|---|---|
| `Max`: `a > b` became `false` | killed | `TestPair` |
| `Max`: `a > b` became `a >= b` | survived | `TestPair` |
| `Min`: `a < b` became `false` | killed | `TestPair` |
| `Min`: `a < b` became `a <= b` | survived | `TestPair` |

The survivor in `Max` lists `TestPair`, whose subtest `min` does not call
`Max`. dokimi runs each check of a suite as a subtest of the test of its
application, such as `TestInMemoryStore/Get/zero-on-error`. It reports a
check that covered mutants and did not kill any. With the coverage of the
top-level test, each check of an application seems to cover every mutant
that the application covered.

### Prior art

- PIT's option `fullMutationMatrix`, false by default, continues after a
  test fails and records the other failing tests in the XML report. PIT's
  documentation calls the option partially supported.
- StrykerJS's option `disableBail`, false by default, makes the test runner
  report all failing tests when a mutant is killed. Its default coverage
  analysis, `perTest`, determines which tests cover which mutant in the
  initial test run.

## Detailed design

### Components

| Component | Responsibility |
|---|---|
| Leaf tests | The tests that a record lists |
| The first killer | The tests of a kill in a run that stops at its first failing test |
| Every killer | A mode that runs each part of a mutant's run to its end |
| Leaf coverage | A mode that traces each leaf test alone |
| The record | The modes of the run, and the kills that list every killer |
| The corpus | Cases of both modes and of the first killer |

### Leaf tests

A test of a language's runner can start other tests, as a Go test starts a
subtest with `t.Run`. A leaf test is a test that the run started and that
did not start another test of the run. In Go, a leaf test is a subtest
without subtests of its own, or a top-level test without subtests. The
engine lists a leaf test by the name that the runner gives it, such as
`TestPair/max`. It lists a test of another target as RFC-0002 states.

When a test fails, the runner fails each test that started it. Go's testing
package marks the parent test failed at once. That failure does not add a
fact about the mutant. The engine lists a failed test only when every test
that it started passed. RFC-0002 already leaves a test that waits
for one of its subtests out of the tests that were running at the deadline.

### The first killer

In a run that stops at its first failing test, the engine lists in `tests`
of a killed mutant the test whose failure the runner reported first. It
does not list another test:

- In Go, the engine lists the test of the first line `--- FAIL:`. The
  testing package states a subtest's failure before the failure of each test
  that started it. The engine then lists the subtest.
- A killed mutant whose run did not report a failed test lists the tests
  that were running, as RFC-0002 states. A test that calls `os.Exit(1)` ends
  such a run.

The record of a kill then depends on the order in which the tests fail. It
does not depend on when the engine ends the run. Which of two parallel tests
fails first still depends on the schedule.

A run in which a test failed is `killed`, whatever ended the run after the
failure: the deadline, the memory ceiling, or a signal that the engine did
not send. RFC-0002 gives `timed-out`, `exhausted` and `error` to a run that a
limit or a signal ended. It does not order these verdicts against a test
that also failed, and this rule gives that order. The caller's cancellation
of a run gives the mutant `not-run`, as RFC-0002 states.

### Every killer

In this mode, the engine runs each part of a mutant's run to its end. It
does not pass the runner an option that stops at a failure, such as Go's
`-test.failfast`. It does not end a run at its first failed test either:

- The engine lists in `tests` of a killed mutant every failed test whose
  started tests all passed. The list follows the order of the runner's
  reports.
- The parts of a mutant's run are those of RFC-0002. In a program with a
  record of each test's sites, the run starts with the tests that executed
  the mutant's site. It goes on with the program's whole suite only when
  they pass.
- A program's run ends on its own when its runner reports the end of its
  tests and the program then exits. A Go test binary reports the end with
  its last line, `PASS` or `FAIL`. After such a run, the mutant's run goes on
  with the next program whose trace contains the site. It also goes on when
  the program killed the mutant.
- The first program whose run did not end on its own ends the mutant's run.
- The mutant's verdict is the verdict of the first program's run that did
  not pass, by the rule of the first killer.
- The engine sets `allKillers` on a killed mutant when every program's run
  of the mutant ended on its own. `tests` then lists every test that failed.
  A kill whose run ended otherwise lists the tests that failed before the
  end, without `allKillers`.
- The mode applies to the confirmation runs of RFC-0002 as well.

In a run that stops at its first failing test, a failure ends the run
before a later test can hang or exhaust the memory. The rule of the first
killer gives the same run in this mode the verdict `killed` too. The mode
then changes the tests of a kill and keeps its verdict, for a suite whose
tests fail in a fixed order.

### Leaf coverage

In this mode, step 4 of RFC-0002 runs each leaf test alone, in place of
each test of the runner:

- The leaf tests of a program are those that its opening control run
  started. The engine runs each of them alone, with no mutant active and a
  trace on. It records the sites that the leaf test executed and the wall
  time of its run.
- A leaf test runs inside the tests that started it, so its sites include
  the code of those tests. In Go, the engine selects a leaf test with
  `-test.run` and one anchored pattern per element of its name, such as
  `^TestPair$/^max$`. It quotes each element, so the pattern matches the
  element alone.
- The engine traces every program that has more than one leaf test.
  RFC-0002's condition that a program have fewer tests than mutants to run
  does not apply. The record of each leaf test is the purpose of the mode.
- A program does not get a record of each leaf test's sites in four cases:
  a leaf test fails alone, a trace has no start mark, the caller's deadline
  leaves too little time, or a leaf test's run alone did not start it. A
  subtest whose name contains a random value is such a leaf test.
- The engine lists in `coveredBy` the leaf tests whose run alone executed
  the mutant's site.
- A mutant's run starts with the leaf tests that executed its site, as
  RFC-0002 starts it with the tests that executed it. In Go, the engine
  joins their patterns with `|`. Go's testing package reads `|` as a
  separator of alternatives of whole names, such as
  `^TestPair$/^max$|^TestAbs$`. That part ends at 10 × the sum of the leaf
  tests' wall times in their runs alone + 2 s.

### The record

The record gains three fields:

| Field | Value |
|---|---|
| `killers` | `all` for a run in the mode every killer, and `first` for any other run |
| `coverage` | `leaves` for a run in the mode leaf coverage, and `tests` for any other run |
| `mutants[].allKillers` | `true` on a killed mutant whose `tests` lists every test that failed in its run, as the mode every killer states. Absent otherwise |

- The record's version stays 2. A reader of version 2 reads `tests` and
  `coveredBy` with their meanings in RFC-0002: the tests that failed, and
  the tests whose run alone executed the site.
- A record without `killers` and `coverage` is the record of a run with
  `first` and `tests`.
- `spec/protocol.json` lists the values of `killers` and of `coverage`. The
  record's schema enumerates the same values.

```json
{
  "record": "dokimi-mutate",
  "version": 2,
  "killers": "all",
  "coverage": "leaves",
  "mutants": [
    {
      "key": "3f9a0c71d2b4e816",
      "verdict": "killed",
      "tests": ["TestStore/Get/zero-on-error", "TestStore/Get/reads-what-was-written"],
      "allKillers": true,
      "coveredBy": ["TestStore/Get/zero-on-error", "TestStore/Get/reads-what-was-written"]
    }
  ]
}
```

The excerpt shows the new fields alone.

### The reuse of a verdict

The settings of RFC-0004 decide which earlier verdicts a run may reuse: a
run reuses only the verdicts of a run with the same settings. The settings
also contain `killers` and `coverage`. A kill of a run with `first` lists
one test, where a run with `all` lists each. A survivor of a run with
`tests` lists top-level tests in `coveredBy`.

### The corpus

`case.json` gains three fields:

- `killers` is `all` for a case whose run is in the mode every killer.
- `coverage` is `leaves` for a case whose run is in the mode leaf coverage.
- `mutants[].allKillers` is `true` for each mutant whose record states
  `allKillers`, in a case whose run is in the mode every killer.

An engine passes such a case when its record states the case's `killers`
and `coverage`. The record must also state `allKillers` on exactly the
mutants that `case.json` marks, besides the rules of RFC-0002.

| Case | What its fixture does | What it proves |
|---|---|---|
| `leaves` | `TestPair` starts the subtests `larger` and `smaller`, which check one function each, and the top-level test `TestEqual` calls `larger` with two equal numbers | `coveredBy` with the leaf tests whose runs alone executed each site, and `tests` with the first test that failed, without `TestPair` |
| `killers` | `TestSign` starts four subtests. Two of them detect the mutant that makes `sign` return 0 for a number that is not negative, and the last waits until the deadline when `sign` gives a negative number the wrong sign | `tests` with every subtest that failed, `allKillers` on each kill whose run ended on its own, and a kill without `allKillers` whose run the deadline ended after a failure |

## Alternatives considered

### Every failed test, with the tests that started it

The engine lists every test that failed, and a reader removes each test
whose subtest failed.

**Why not:** a run that stops at its first failing test ends before the
runner reports the tests that started the subtest. The engine would add
them itself. A group test would then kill every mutant that its subtests
kill. A tool that looks for redundant tests would find each subtest
redundant against its group.

### Every killer by default

**Why not:** each killed mutant then runs every test of the part that
killed it, where the default run ends at the first failure. The score counts
each kill once, so a run for a score needs the first killer alone. A tool
that attributes kills to tests asks for the mode. PIT and StrykerJS both
stop at the first failure by default.

### The whole suite for every killed mutant

The run of each killed mutant also runs the tests that did not execute its
site. The engine then lists every test of the suite that fails.

**Why not:** a test that did not execute the site in its run alone fails
only through state that another test left. Finding such tests would cost a
run of the whole suite for each kill.

### A trace that records the running test

The instrumented program records the test that runs when a site executes,
so one run gives the sites of every test.

**Why not:** Go gives the code under test no way to find the test that runs
it. A goroutine that a test starts can execute a site while another test
runs, and parallel subtests run at once. A run per leaf test attributes
each site exactly.

### The top-level tests as the first part of a mutant's run

A mutant's run starts with the top-level tests of the leaf tests that
executed its site.

**Why not:** a top-level test can start hundreds of subtests, such as the
checks of a dokimi suite. Go's testing package reads `|` between whole
patterns as alternatives, so the engine selects the leaf tests alone.

### Record version 3

**Why not:** RFC-0004 raised the version because a reader of version 1
would misread a reused verdict. No field of version 2 changes its meaning
here, so a reader of version 2 reads a record of this proposal correctly.

## Drawbacks

- **The mode every killer costs time.** A killed mutant's run lasts until
  the tests of its part end, where the default run ends at the first
  failure.
- **Leaf coverage costs a run per leaf test.** Each run starts a process
  and runs the code of the tests that start the leaf test.
- **The coverage of a leaf test includes the code of the tests that start
  it.** A site in the setup of a group test counts as covered by each of
  its subtests.
- **A test that fails on its own after its subtest failed is not listed.**
  The runner states one failure for the test. That failure does not show
  whether the test failed on its own or through its subtest.
- **A kill whose run the deadline ended lists only the tests that failed
  before the end.** The record then does not state `allKillers`.
- **A validator with an earlier copy of the schema rejects the new
  fields.** The schema of version 2 sets `additionalProperties` to false,
  so a validator needs the schema of this definition.

## Unresolved and future work

- A list of the tests that pass with a mutant active, which PIT's full
  mutation matrix contains, is not proposed.

## References

| What | Where |
|---|---|
| PIT's `fullMutationMatrix`: false by default, and partially supported | https://pitest.org/quickstart/maven/ |
| StrykerJS's `disableBail` and `coverageAnalysis` | https://stryker-mutator.io/docs/stryker-js/configuration/ |
| Go 1.27.3's testing package: `common.Fail` marks the parent test failed, and `M.Run` ends with the line `PASS` or `FAIL` | https://github.com/golang/go/blob/go1.27.3/src/testing/testing.go |
| Go 1.27.3's `splitRegexp`, which reads `\|` outside parentheses and brackets as alternatives of whole patterns | https://github.com/golang/go/blob/go1.27.3/src/testing/match.go |
| dokimasia/mutate-spec#36, every test that kills a mutant | https://github.com/dokimasia/mutate-spec/issues/36 |
| dokimasia/mutate-spec#38, the coverage of a subtest, with the run of dokimi-mutate-go v0.1.0 | https://github.com/dokimasia/mutate-spec/issues/38 |
