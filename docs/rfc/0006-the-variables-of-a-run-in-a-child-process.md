---
rfc: 0006
title: The variables of a run in a child process
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Accepted
created: 2026-11-03
updated: 2026-11-03
discussion: https://github.com/dokimasia/mutate-spec/issues/37
supersedes: none
superseded-by: none
produces-adr: tbd
---

# RFC-0006: The variables of a run in a child process

## Summary

A test can run the code under test in a child process, as a sandbox runs
its first process. The engine turns on the mutant and the trace through the
environment, so a child takes part in the run only when it gets the run's
variables. This proposal lists every variable of a run in the protocol,
gives each of them the prefix `DOKIMI_MUTATE_`, and adds the variable of the
trace. Every child that inherits the variables with the prefix takes part in
the run. A sandbox that sets the environment of its child passes them, as it
passes `GOCOVERDIR` for Go's coverage.

## Motivation

RFC-0002 defines `DOKIMI_MUTATE_MUTANT` and `DOKIMI_MUTATE_INSTRUMENTED`,
and the cases in which a test reads them. It does not state what a process
that the test program starts needs to take part in the run:

- A child that inherits the environment runs the active mutant and appends
  its sites to the trace.
- A child with an environment of its own does not run the mutant and does
  not append to the trace. Its sites get `no-coverage`, although the tests
  execute them and detect their mutants.

A program that sets the environment of its child cannot pass the trace,
because the protocol does not define its variable. The Go engine reads the
name from a constant of its own.

dokimi-mutate-go v0.2.0 ran a function that a test calls in a child process
of the test program, with four environments of the child:

| The child's environment | The package's result |
|---|---|
| One variable of its own | `0 of 2 mutants detected (0%): 2 not covered` |
| The test program's environment | `2 of 2 mutants detected (100%): 2 killed` |
| One variable of its own and each variable with the prefix | `2 of 2 mutants detected (100%): 2 killed` |
| One variable of its own, under `--confirm` | `2 of 2 mutants detected (100%): 2 killed` |

The confirmation costs one build of the test program for each mutant of the
child. A sandbox package runs the first process of a sandbox as a child of
the test program, with an environment of two variables. Its tests detect 2
of the 135 mutants of that process's file. Of the other 133, 132 get
`no-coverage` and 1 survives. Confirming the 133 mutants costs 133 builds
of the test program.

Go's coverage and coverage.py measure a child process through a variable
that the child inherits. A binary that `go build -cover` built writes its
coverage into the directory that `GOCOVERDIR` names, and runs without
coverage when the variable is not set. coverage.py starts its measurement
in a subprocess when `COVERAGE_PROCESS_START` is visible there.

## Detailed design

### The variables of a run

`spec/protocol.json` names each variable of a run, and the prefix that
each of them starts with:

```json
{
  "variable": "DOKIMI_MUTATE_MUTANT",
  "instrumented": "DOKIMI_MUTATE_INSTRUMENTED",
  "trace": "DOKIMI_MUTATE_TRACE",
  "prefix": "DOKIMI_MUTATE_"
}
```

| Run | `DOKIMI_MUTATE_MUTANT` | `DOKIMI_MUTATE_INSTRUMENTED` | `DOKIMI_MUTATE_TRACE` |
|---|---|---|---|
| The opening control run, and a run of one test alone | `0` | `1` | The path of the run's trace |
| The closing control run | `0` | `1` | Not set, or empty |
| A mutant's run | A positive integer that identifies the mutant within the run | `1` | Not set, or empty |
| The ordinary control run | `0` | Not set | Not set |
| A confirmation run | The mutant's integer | Not set | Not set |

- The instrumented program appends its start mark and its sites to the file
  that `DOKIMI_MUTATE_TRACE` names. It traces nothing when the variable is
  not set or empty.
- Each process of the instrumented program appends to the same file, so
  the trace of a run contains the sites of every process that took part.
  The format of the trace's lines is the engine's own, because the engine
  alone writes and reads them.
- Every variable of a run starts with `DOKIMI_MUTATE_`, and so does each
  variable that the protocol adds.
- A variable of the caller can have the prefix too, such as an option of an
  engine's command. A child inherits it with the run's variables.

### A child process

A process that the test program starts takes part in the run when it runs
the instrumented program and inherits every variable whose name starts
with `DOKIMI_MUTATE_`:

- A child that inherits the test program's environment takes part without
  a change.
- A program that sets the environment of its child, such as a sandbox,
  copies each variable of its own environment whose name starts with the
  prefix into the child's environment, as it copies `GOCOVERDIR`.
- A child that runs another program, such as a command that a test builds
  with the toolchain, runs code without the instrumentation. The run sees
  none of that child's sites.

### The engine

An engine reads the names of the variables from `spec/protocol.json`. Its
documentation lists each variable and the prefix, and states that a child
takes part in the run when it inherits every variable with the prefix.

### The corpus

| Case | What its fixture does | What it proves |
|---|---|---|
| `child` | A test runs a function in a child process of the test program, whose environment contains a variable that marks the child and each variable with the prefix, and no other | The child runs the mutant and appends its sites to the trace: the test kills both mutants of the function, and neither gets `no-coverage` |

## Alternatives considered

### Each variable by name

A program that sets its child's environment copies `DOKIMI_MUTATE_MUTANT`,
`DOKIMI_MUTATE_INSTRUMENTED` and `DOKIMI_MUTATE_TRACE` by name.

**Why not:** each variable that the protocol adds would need a change in
every such program. A program that copies the prefix also passes each
variable that the protocol adds.

### The state of a run in a file

The engine writes the active mutant and the trace's path into a file, and
the instrumented program finds the file from its own path.

**Why not:** the workers of a run start one test binary in a process per
worker at once, so one file next to the binary cannot state the mutant of
each process. A sandbox can also run its child with another view of the
file system. An environment variable belongs to one process and to the
processes that it starts.

### One variable with every value

The engine sets one variable, such as `DOKIMI_MUTATE=mutant=3;trace=/tmp/t`.

**Why not:** tests and test libraries read `DOKIMI_MUTATE_MUTANT` and
`DOKIMI_MUTATE_INSTRUMENTED` as RFC-0002 defines them, and a test that
checks one value would parse a list. The prefix gives a child one rule as
well.

### Confirmation of each mutant without coverage

`--confirm` already kills the mutants of a child, in each mutant's ordinary
build.

**Why not:** it costs one build of the test program per mutant, 133 builds
for one file of the sandbox package. A child that inherits the variables
costs no build.

## Drawbacks

- **A program that sets its child's environment needs a change.** A
  sandbox that passes a fixed list of variables passes none of the run's
  until it copies the variables with the prefix.
- **Variables of the caller pass too.** A child inherits each variable of
  the caller with the prefix, such as `DOKIMI_MUTATE_GO_WORKERS` of the Go
  engine's command. The instrumented program reads none of them.
- **A child with another view of the file system traces nothing.** A child
  in a container or a chroot cannot open the trace's path. Its sites get
  `no-coverage` unless the sandbox gives it the trace's directory.
- **A child that runs another program is not instrumented.** A command
  that an integration test builds, such as with `go build`, contains the
  target's code without the switches, so its sites get `no-coverage`.

## References

| What | Where |
|---|---|
| Go's coverage of integration tests: `GOCOVERDIR`, and a binary that runs without it | https://go.dev/doc/build-cover |
| coverage.py in subprocesses: `COVERAGE_PROCESS_START` | https://coverage.readthedocs.io/en/latest/subprocess.html |
| dokimasia/mutate-spec#37, the request of this proposal, with the counts of the sandbox package | https://github.com/dokimasia/mutate-spec/issues/37 |
