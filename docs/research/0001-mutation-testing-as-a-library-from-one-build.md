---
research: 0001
title: Can mutation testing run as a library that the test runner drives, and from one build for all mutants, in Go, Java, Kotlin, Python, TypeScript and Rust?
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Answered
created: 2026-10-02
updated: 2026-10-02
freshest-source: 2026-10-02
supersedes: none
superseded-by: none
---

# Research-0001: Can mutation testing run as a library that the test runner drives, and from one build for all mutants, in Go, Java, Kotlin, Python, TypeScript and Rust?

## The question

gremlins, the mutation tester that the Go repositories gate on, is a binary
installed beside the toolchain. For each mutant it writes the mutated file
into a copy of the module and runs `go test`, which compiles and links a
test binary. The question has two parts for each language that dokimasia
supports:

- Can mutation testing run as a library that the language's own test
  runner drives, with nothing installed beside it?
- Can every mutant run from one build, and what does one build save over a
  build per mutant?

Building a Go prototype raised a third part: which mutation operators such a
library should apply, and how its results compare with the existing Go
tools on the same package.

### What would count as an answer

- A tool in the language that has the property, read in its source or its
  documentation.
- A measurement of a build per mutant against one build, on the same
  mutants.
- A construct of the language that one build cannot express, with the
  reason.
- Developers' judgments of each operator's mutants, collected at scale.
- The existing tools run on one package, with each tool's mutants checked
  against a count from the type checker.

### What sources are admissible

Peer-reviewed papers, the tools' source code and documentation,
maintainers' statements, and measurements of our own.

### What would change the answer

- A language whose build or module loading forces a build per mutant.
- A measurement in which a build per mutant costs less than one build that
  contains every mutant.
- Developer feedback on our own code in which more than half of an
  operator's survivors are not worth a test.

## The answer

Both properties are possible in all five languages. The table lists the
tools that have each property today:

| Language | A library that the test runner drives | Every mutant from one build | Both in one tool |
|---|---|---|---|
| Go | ooze, called from a test. It builds once per mutant | mutest and kanly, both separate binaries | None published. Our prototype has both |
| Java | None beyond two projects with 1 and 2 stars | PIT rewrites bytecode and compiles nothing per mutant. Major compiles every mutant once inside javac | None |
| Kotlin | mutflow, a compiler plugin with a JUnit extension | mutflow, and PIT on Kotlin's bytecode | mutflow, 35 stars |
| Python | pytest-gremlins, a pytest plugin | Python compiles nothing. mutmut 3 and pytest-gremlins load every mutant once and switch between them | pytest-gremlins, released as alpha |
| TypeScript | None found | StrykerJS transpiles once and switches the active mutant inside a running test runner | None |
| Rust | mutagen, an attribute macro in the dev-dependencies, run by `MUTATION_ID=n cargo test` | mutagen, and mutest-rs, a replacement compiler driver | mutagen, nightly only, last changed in May 2023 |

The technique behind one build is mutant schemata. The program is compiled
once with every mutant behind a runtime switch, and each run selects one
mutant. Untch, Offutt and Harrold proposed it in 1993. Papadakis et al.'s
2019 survey calls it the most commonly used technique against the cost of
compiling each mutant. The JVM offers a second technique, which PIT uses:
rewrite the loaded bytecode in memory.

What one build saves depends on the time a test run takes against the time
a build takes. On three Go packages, with the same 1,318 mutants in both
modes, one build was 55 times faster when the suite ran in milliseconds,
3.1 times faster when it ran in a fifth of a second, and 1.1 times faster
when it ran in four seconds. A build per mutant added 1.3 to 2.4 MB to the
build cache per mutant. mutest-rs, with one build, finished in a median of
22.9 seconds on eight Rust crates, where cargo-mutants, with a build per
mutant, took 1.6 hours. That comparison also includes test selection.

The library should apply the five operator classes of Google's mutation
testing service: arithmetic, relational, logical connector, unary insertion
and statement removal. Against gremlins' defaults, that set adds the
logical connector and statement removal, and the prototype leaves out
gremlins' invert-negatives operator. Developers at Google judged 74.5% to 84.1%
of the mutants of each class shown to them worth a test. Literal
replacement is left out. Google has no literal operator, and every
equivalent mutant we read on btree came from one.

On google/btree, the prototype ran gremlins' operators 5.0 times faster than
gremlins did, and spent a fifteenth of ooze's time per mutant. Running them
side by side also exposed a defect or a limit in each of gremlins, ooze,
mutest and kanly. With all five operator classes, the prototype ran 605
mutants on btree in 76 seconds.

A library that the test runner drives must keep the build it starts free
of the test that started it. ooze and our prototype use a build tag, and
mutflow compiles twice. One build has costs of its own. It cannot mutate
constants, and type context needs the instrumenter's help. A run that
does not execute any switch passes for every mutant, unless a run that
forces every switch to fail catches it.

## Findings

### Mutant schemata compile every mutant once, and the literature measures the saving

Untch, Offutt and Harrold (ISSTA 1993) encode every mutant of a program in
one metaprogram, which the program's own compiler compiles once. They
report preliminary speedups of over 300% against the interpretive mutation
systems of the time. They expect schemata to run somewhat slower than
mutation built into a compiler, because each mutated operation becomes a
call.

Papadakis et al.'s survey (Advances in Computers 112, 2019, §5.2) states
that a separate source file per mutant "requires approximately 3 seconds
(on average) to compile a single mutant of a large project", citing the
trivial compiler equivalence study. The survey names meta-mutation, or
mutant schemata, the most commonly used technique against that cost. It
names bytecode manipulation as the alternative: PIT for Java, and tools for
.NET's intermediate language and for LLVM bitcode.

Pizzoleto et al.'s systematic review (JSS 157, 2019) classifies 153 studies
of cost reduction into 21 techniques. Metamutants, its name for mutant
schemata, are one of them.

Just, Kapfhammer and Schweiggert (AST 2011) generate every mutant inside
javac, as conditional expressions and statements. For aspectj's 406,382
mutants, generating and compiling them took 33% longer than an ordinary
compile. Running the test suites with every condition in place took 15%
longer on average, with every condition evaluated. The tool paper on Major
(ASE 2011) reports that the overhead ranged from 1% for Apache Ant to 29%
for Java PathFinder.

Wang et al. (ISSTA 2017) fork one process for each group of mutants that
leave the same state after a mutated statement. On top of split-stream
execution, which forks at the first mutated statement, this was 2.56 times
faster on average.

**What we concluded:** compiling a program once with every mutant is an
established technique with measured savings. What differs between
languages is the tooling.

### Go: one tool is a library, two build once, and no published tool is both

- **gremlins** v0.6.0 applies each mutant to a worker's copy of the module
  and runs `go test -failfast` on the package
  (`internal/engine/executor.go`, lines 160 to 242). Every mutant compiles
  and links a test binary.
- **ooze** v0.2.0 is a library. A test file behind `//go:build mutation`
  calls `ooze.Release(t)`, and `go test -tags=mutation` runs it. For each
  mutant, ooze copies the repository to a new temporary directory, writes
  the mutated file and runs `go test -count=1 ./...` there
  (`internal/laboratory/laboratory.go`). It builds once per mutant, and runs
  one mutant at a time unless the test passes `ooze.Parallel()`.
- **mutest** v0.6.2, by fchimpan, and **kanly** v0.1.0 are separate
  binaries that compile each package once. Each replaces a mutated
  operator with a call to a generated generic function, which reads the
  active mutant from an environment variable. mutest hands the rewritten
  files to the compiler through `go build`'s `-overlay` flag and leaves the
  source tree untouched. Neither publishes a measurement against a build
  per mutant. mutest's README names the MIT licence, and its repository
  contains no licence file.
- **gomutants** applies each mutant as an overlay and builds once per
  mutant. It runs only the tests whose coverage includes the mutated line.

We wrote a prototype library, gomut, that has both properties. Its source
and the harness that measured it are in the directory beside this document,
[`0001-mutation-testing-as-a-library-from-one-build/`](0001-mutation-testing-as-a-library-from-one-build/README.md),
whose README maps each figure here to the commands that produce it. gomut
applies the five operator classes of Google's mutation testing service,
adapted to Go:

| Class | The mutants of one site |
|---|---|
| Arithmetic | `+`, `-`, `*`, `/` and `%` to its counterpart |
| Relational | `<` to `<=`, to `>=`, to `true` and to `false`, and likewise for `<=`, `>` and `>=`; `==` and `!=` to the other, to `true` and to `false` |
| Logical connector | `a && b` to `a \|\| b`, to `a`, to `b`, to `true` and to `false`, and likewise for `\|\|` |
| Unary insertion | `++` and `--` swapped; a boolean variable, field, call or `!x` negated |
| Statement removal | A statement deleted; a `return`'s results replaced by zero values |

The prototype rewrites the package's source into schemata and hands the
result to the compiler through `-overlay`, which builds one test binary.
Each class has its own form:

- An arithmetic or relational site calls a generic helper of its own, which
  switches on the active mutant.
- A logical connector calls a helper that takes each operand as a closure,
  so each operand runs at most once and in its original order.
- A negation compares the value with the switch, as in
  `x != _gomutIs(17)`.
- A statement removal wraps the statement in `if !_gomutIs(17) { ... }`.
  A zeroed return gets `if _gomutIs(17) { return ... }` in front of it.

`gomut.Check(t, ".")` runs the whole check from a test behind a build tag,
so `go test -tags mutation` drives it. The same harness can instead build
once per mutant, from an overlay that contains that mutant alone, which is
how gremlins and ooze work.

We ran both modes on three packages, one mutant at a time, each mode with a
fresh build cache warmed by one ordinary test run (Go 1.27.1,
`GOMAXPROCS=4`, 4 cores). Each mutant's timeout was ten times the
package's test run plus two seconds:

| Package | Mutants | Suite | Verdicts | One build | A build per mutant | Ratio |
|---|---|---|---|---|---|---|
| go-humanize v1.1.0 | 655 | 0.006 s | 606 killed, 49 survived, the same in both modes | 1.9 s, median 3 ms per mutant | 104.2 s, median 161 ms | 55× |
| google/btree v1.1.3 | 605 | 0.2 s | 457 and 455 killed, 2 verdicts differ | 49.0 s, median 7 ms | 150.8 s, median 196 ms | 3.1× |
| shopspring/decimal v1.4.0, every 36th mutant | 58 of 2,088 | 4 s | 41 killed, 17 survived, the same in both modes | 135.0 s, median 4,050 ms | 145.4 s, median 4,184 ms | 1.1× |

The one build took 0.24 to 0.44 seconds. A build per mutant added 130 to
190 milliseconds to every mutant. The times leave out the mutants that ran
into the timeout: 7, 5 and 1, most of them statement deletions. With them,
the totals are 27.5 against 130.2 seconds,
78.7 against 180.9 and 179.7 against 189.6.

On btree, the ratio fell from 5.2 with gremlins' operators alone to 3.1 with
all five classes. A quarter of btree's new mutants survive, and each
survivor runs the whole suite, so test time outgrew build time. The two
differing verdicts were killed in some runs and not in others, in both
modes: btree's tests generate random keys from a seed taken from the clock.

A build per mutant also grew the build cache: by 848.0 MB on go-humanize,
1,120.9 MB on btree and 143.3 MB on the decimal sample, about 1.3, 1.9 and
2.4 MB per mutant. One build grew it by 4.0, 5.3 and 8.7 MB.

Run as a test, `go test -tags mutation -run TestMutation` on a copy of
go-humanize ran all 655 mutants in 17 seconds. It reported each of the 50
survivors as a failure of the test, at the mutant's file, line and column.
On a copy of btree it ran 605 mutants in 76 seconds. Adding the prototype as
a dependency raised go-humanize's go line from 1.21 to 1.27, the version
that the prototype declares. Its dependency `golang.org/x/tools` v0.50.0
declares 1.26.0.

With no mutant active, the schemata binary ran decimal's suite in a median
of 4.094 seconds against 4.016 for the ordinary binary, 1.9% slower, over
five alternating rounds. btree's suite, repeated 20 times in each run, took
4.594 seconds against 4.080, 12.6% slower. With gremlins' operators alone,
the same measurement gave 1.1% and 5.9%.

Building the prototype found these limits of schemata in Go:

- A constant expression cannot contain a runtime switch, so constant
  declarations and array lengths keep their operators. kanly states the
  same limit for constant declarations and struct tags.
- An untyped constant in a non-constant shift takes its type from the
  surrounding expression. Passed to a generic helper, the constant in
  `(1 << k) + 1`, inside a `uint` declaration in decimal, became an `int`,
  and the build failed. The call needs its type argument written out, and
  the prototype skips a site whose type it cannot name. kanly restricts its
  literal mutations to plain `int` for the same reason.
- Generic helpers need language version 1.18. decimal's `go.mod` declares
  `go 1.10`, and the build failed with "type parameter requires go1.18 or
  later". A `//go:build go1.18` line raises the language version of one
  file. Every rewritten file needs it too, because a call that infers a
  generic function's type arguments fails in an older file with "implicit
  function instantiation requires go1.18 or later". A module at go 1.22 or
  above must not get the line, because it would lower the language version
  and change the semantics of loop variables.
- The go command matches an overlay against the paths it computes from its
  working directory. When the package's path went through a symbolic link,
  the overlay matched no file, the original package ran for every mutant,
  and all 193 mutants of go-humanize's first run survived. The prototype
  now resolves links first, and it runs the binary once with a value that
  makes every switch panic. That run must fail, and the prototype stops
  when it passes. mutmut runs the same check before its mutants.
- An operand whose type is a type parameter, and a comparison of an
  interface value with a concrete value, do not give the helper a single
  type argument. The prototype skips both.
- A swapped connector can write `x != 'a' || x != 'b'`, which go vet's
  bools check rejects, so `go test` refuses to build that mutant alone. The
  helper hides the expression from vet. The prototype runs such a swap in
  neither mode: 1 on go-humanize and 4 on decimal.
- Statement removal leaves declarations, labelled statements and branches
  alone, and a final statement that terminates its list, whose deletion
  would leave a function without a return. A `return` with results gets
  zero values instead, written as `*new(T)` for each result type.
- Each mutant runs in a fresh process, so package initialization runs under
  the mutant. go-humanize computes its range of SI exponents in a
  package-level initializer that ranges over a map. A mutant there was
  killed in 36 of 40 runs, because Go randomizes the order of a map.

**What we concluded:** Go can have both properties in one library, with all
five operator classes. The library needs the go command when the tests
run, which is present wherever `go test` runs, and `-overlay` keeps the
source tree untouched. One build cut the time 55 times on a package whose
suite runs in milliseconds and 3.1 times on one whose suite runs in a fifth
of a second. It saved little on a package whose suite takes four seconds.
On every package it cut the build cache's growth, by 17 to 213 times.

### On btree, each Go tool finds a different number of mutants

We counted the mutants of gremlins' five default operators in the code that
Go 1.27 compiles from google/btree v1.1.3, and asked the type checker
whether each one compiles. There are 126: 31 arithmetic mutants, 62 from 31
ordered comparisons, 25 from equality comparisons and 8 increments. A 127th,
`descend = direction(-1)` to `+1`, gives both direction constants the value
1 and a `switch` a duplicate case. The same count over every `.go` file
gives ooze's 258 file by file, and gremlins' 318 apart from a unary `+` in
each implementation file.

| Tool | Listed | Of the 126 | Executed |
|---|---|---|---|
| Prototype, gremlins' operators | 124 | 124 | 124 |
| gremlins v0.6.0 | 318 | 126 | 140 runs of 123 |
| ooze v0.2.0, three viruses | 258 | 118 | 258 |
| mutest v0.6.2 | 56 | 56 | 56 |
| kanly v0.1.0 | 566 | 108 | 543 |

The counts differ in the files each tool reads, the operators it applies
and the way it counts:

- **Files.** btree contains `btree_generic.go`, behind `//go:build go1.18`,
  `btree.go`, behind `//go:build !go1.18`, and `btree_mem.go`, behind
  `// +build ignore`. Go 1.27 compiles only the first. gremlins and ooze
  read every `.go` file, so 172 of gremlins' 318 mutants and 140 of ooze's
  258 are in code that no build contains. gremlins reports them as not
  covered. ooze builds and tests each one, and reports it as survived.
- **Operators.** mutest mutates only comparisons, by design: the boundary
  of `<`, `<=`, `>` and `>=`, and the negation of `==` and `!=`. kanly
  applies 17 operators, 10 of them outside gremlins' set, and its
  comparison operators accept integer operands only, so it leaves out 16
  `== nil` and `!= nil` checks on pointers. ooze has no operator for `++`
  and `--`. The prototype and kanly leave out `a < b` in `Less`, whose
  operands are a type parameter.
- **Counting.** gremlins maps the `-` token to INVERT_NEGATIVES and to
  ARITHMETIC_BASE (`internal/engine/mappings.go`), and both make the same
  edit, so 17 mutants ran twice with the same verdict. gremlins and kanly
  skip mutants on lines that no test executes: 3 and 23.

Running the four tools exposed these defects and limits:

- gremlins v0.6.0 takes the first line of `go.mod` as the module name
  (`internal/gomodule/gomodule.go`). btree's `go.mod` opens with a licence
  comment, so no coverage path matched, all 318 mutants were reported as
  not covered, and gremlins exited 0. We removed the comment in its copy
  to measure it.
- ooze copies the module to a new temporary path for every mutant, so no
  compiled package repeats a build cache key. Its run grew the cache by
  477.5 MB.
- Some of kanly's mutants allocate without bound. Under an 8 GB memory
  limit, one test binary grew to 8.2 GB, and systemd stopped the whole run.
  Under 4 GB, the kernel ended 9 test binaries, and kanly counted their
  mutants as killed. kanly also reported `max` returning zero values
  (line 356) as survived. Applied by hand, that mutant fails 4 tests.
- mutest refuses a module whose `go.mod` declares a version below 1.20,
  and btree declares `go 1.18`. We raised the line in its copy.

**What we concluded:** a tool that reads files without the build's
constraints counts mutants in code that never compiles, and a tool keyed on
tokens counts some mutants twice. A count of mutants means little until it
is checked against the type checker.

### One build ran gremlins' operators five times faster than gremlins

We ran each tool on a fresh copy of btree, one mutant at a time, each with
a private build cache warmed by one test run:

| Tool | Builds | Mutants run | Wall | Per mutant | Build cache growth |
|---|---|---|---|---|---|
| Prototype, gremlins' operators | Once | 124 | 6.1 s | 49 ms | 3.0 MB |
| Prototype, all five classes | Once | 605 | 76.2 s | 126 ms | 3.2 MB |
| mutest v0.6.2, go line raised to 1.20 | Once | 56 | 4.7 s | 83 ms | 2.6 MB |
| kanly v0.1.0 | Once | 543 | 87.5 s | 77 ms, without 3 runaway mutants | 6.8 MB |
| gremlins v0.6.0, `go.mod` comment removed | Per mutant | 123, 17 of them twice | 30.2 s | 245 ms | 231.9 MB |
| ooze v0.2.0, three viruses, 30 s timeout | Per mutant | 258 | 85.7 s | 727 ms per mutant in compiled code | 477.5 MB |

The prototype with gremlins' operators finished in 6.1 seconds, where
gremlins took 30.2, 5.0 times faster. Per mutant in compiled code, ooze
took 15 times as long as the prototype. mutest and kanly, which also build
once, cost about the same as the prototype: their medians were 6 and
8 ms, against the prototype's 5 to 7 ms, the time of one test run. With all
five classes, the prototype's average rose because more of its mutants
survive, and 5 ran into the timeout.

**What we concluded:** against the build-per-mutant tools, one build saves
the build, and against the other one-build tools it costs the same. Of the
one-build tools, only the prototype runs from `go test`.

### A library should apply the five operator classes of Google's service

The literature measures which operators find gaps that developers fix:

- Offutt et al.'s five-operator set, of relational, logical connector,
  arithmetic, absolute value and unary insertion operators, is "usually
  considered as a minimum standard for mutation testing" (Papadakis et
  al.'s survey, §2). gremlins' defaults leave out the logical connector and
  absolute value operators. Their unary insertion covers only `++`, `--`
  and unary minus.
- Google's service applies AOR, LCR, ROR, UOI and SBR, statement block
  removal, in ten languages, Go among them (Petrović et al., TSE 2021).
  Over 16,935,148 mutants, developers marked the ROR mutants shown to them
  productive 84.1% of the time, LCR 83.2%, SBR 82.7%, AOR 75.4% and UOI
  74.5%. SBR produced 68.0% of all mutants.
- Google dropped ABS as unproductive, "mostly because it acted on
  time-and-count related expressions". The service has no literal operator,
  and its example of an unproductive mutant is a changed collection
  capacity.
- Before its suppression rules, developers at Google marked 85% of the
  mutants shown to them unproductive. More than a hundred rules for arid
  nodes, such as logging calls and memory reservation, raised the
  productive share from 15% to 89%. The service also limits itself to one
  mutant per changed, covered line: a median of 7 mutants per change,
  against 820 for traditional mutagenesis.
- Delamaro et al. found that deletion operators "produce significantly
  less equivalent mutants" than other operators (quoted in the survey,
  §5.1.2).
- Pseudo-tested methods are methods that the tests execute, yet no test
  fails when their body is removed. Vera-Pérez et al. (EMSE 2019) found
  them in every project they studied.
- Selecting mutants by operator improves on a random sample of the same
  size by at most 13% (Gopinath et al., in the survey). The number of
  mutants does not measure a set's strength.

On btree, kanly's ten operators outside gremlins' set produced 458
mutants, and 84 survived. 58 of those survivors are in functions where
every mutant of gremlins' operators was killed. Some of them are real gaps.
We applied one by hand: at line 523, `return hit, false` changed to
`return hit, true` lets an iterator be called again after its callback
returned false, and every btree test still passes. No arithmetic or
comparison is on that path. At least 17 of the 58 are equivalent mutants,
all literal replacements:

- 10 change the `includeStart` or `hit` argument of `iterate`, which reads
  them only when a start bound is set and `includeStart` is false.
- 7 change `len(n.children) > 0` to `> 1`, and a node has no children or
  at least two.

The prototype's five classes left these shares of their mutants alive:

| Class | go-humanize | btree | Google's survivability |
|---|---|---|---|
| Arithmetic | 5 of 59, 8.5% | 3 of 31, 9.7% | 13.5% |
| Relational | 38 of 279, 13.6% | 46 of 195, 23.6% | 14.7% |
| Logical connector | 3 of 34, 8.8% | 22 of 70, 31.4% | 16.3% |
| Unary insertion | 0 of 30 | 17 of 67, 25.4% | 9.6% |
| Statement removal | 3 of 253, 1.2% | 60 of 242, 24.8% | 12.7% |

Google's survivability counts the mutants that its suppression rules
allow on changed lines. Ours counts every mutant of the package. On btree,
replacing a comparison with `false` survived 24 of 55 times, and a
connector with `false` 9 of 14 times.

**What we concluded:** the library should apply the five classes of
Google's service. They are the minimum standard minus absolute value, plus
statement removal. Developers judged 74.5% or more of each class's surfaced
mutants worth a test. Literal replacement is left out: no
literal operator is in Google's set, and literals produced every equivalent
survivor we read. A gate that requires every mutant killed needs two more
mechanisms with these classes. It needs suppression rules like Google's,
and a way to mark one mutant equivalent with a reason.

### Java: bytecode rewriting avoids the build, and no mature tool runs from the test runner

PIT generates the mutated bytecode in a child JVM and inserts it into the
running JVM through the Java instrumentation API (Coles et al., ISSTA
2016). It keeps one mutant in memory at a time. By default it starts one
JVM for the mutants of each class, and it can start one per mutant at the
cost of speed. It runs from the command line, Ant and Maven, and a separate
plugin adds Gradle. PIT's FAQ notes that "code in static initializer blocks
is not re-run so the mutants have no effect".

Major replaces javac. With `-XMutator`, it compiles every mutant into the
classes as conditional expressions, and its analysis back-end extends Ant's
JUnit task (Major documentation). Javalanche (ESEC/FSE 2009) combines both
techniques. It rewrites bytecode to avoid recompilation and guards every
mutation with a runtime flag, and it records that µJava also uses mutant
schemata.

MutKt and JAllele run from the test runner. MutKt, with 1 star, offers a
JUnit extension and a Gradle plugin and mutates compiled bytecode. JAllele,
with 2 stars, attaches an agent to its own JVM. We read only their READMEs.

**What we concluded:** on the JVM, compiling nothing per mutant needs no
schemata, because rewriting loaded bytecode is cheaper than any build. A
JUnit extension could drive the same mechanism, and no mature tool does.

### Kotlin: one young tool has both properties

mutflow is a compiler plugin for Kotlin's K2 compiler and a JUnit
extension. The plugin compiles every variant of a mutation point into the
class, as `when` branches that ask a runtime registry which variant is
active. The extension, `@MutFlowTest`, runs a test class once per mutation.
mutflow compiles twice, so the production JAR contains no mutation code. It
mutates only classes annotated `@MutationTarget` or matched by a pattern in
the Gradle plugin, and it finds mutation points while a baseline run
executes `MutFlow.underTest { }` blocks. It has 35 stars, and its last push
was on 2026-09-27.

PIT also mutates Kotlin's bytecode. The open-source plugin that filters the
junk mutations of compiler-generated code is no longer maintained, and
Arcmutate's replacement needs a commercial licence key.

**What we concluded:** a JVM engine that the test runner drives would
combine a compiler plugin with a JUnit extension, the two parts that give mutflow
both properties for Kotlin today. Java code that javac compiles would need
a plugin of its own, or bytecode rewriting.

### Python: nothing is compiled, and the cost per mutant is the import

mutmut 3 copies each mutated function into one version per mutant and
replaces the function with a trampoline. The trampoline calls the version
that the environment variable `MUTANT_UNDER_TEST` names
(`src/mutmut/mutation/trampoline.py`). mutmut imports pytest once and forks
one process per mutant (`src/mutmut/workers/isolation.py`), so it needs
`fork` and runs on POSIX systems only. It mutates code inside functions
only. Its 3.0.0 release, on 2024-10-20, "switched to mutation schemata,
which enabled parallel execution". It runs as a separate command,
`mutmut run`.

pytest-gremlins is a pytest plugin, and `pytest --gremlins` runs it. It
instruments the code once through import hooks, switches between mutants
with an environment variable, and runs only the tests whose coverage
includes a mutant. Its design document puts the requirement as "Not a
wrapper. Not a separate CLI. A proper pytest plugin". Version 1.9.0 is
marked alpha. Its first release was on 2026-01-27.

**What we concluded:** in Python, the cost that corresponds to a build per
mutant is importing the code and starting a process. Switching between
mutants in one loaded process removes both. pytest-gremlins gives Python
both properties, at alpha quality.

### TypeScript: one transpile exists, and the test runner does not drive it

StrykerJS 4.0 (October 2020) switched to mutation switching, its name for
mutant schemata. Its authors expected "somewhere between 20% to 70% speed
increase", and the code is transpiled or bundled once instead of once per
mutant. Its instrumenter relies on `// @ts-nocheck`, so the code type-checks
with every mutant in place. A separate TypeScript checker removes the
mutants that would not compile on their own.

StrykerJS 6.0 (May 2022) added hot reload: a worker loads the code once and
switches the active mutant between test runs. On Stryker's own core that
was "a whopping 70% performance improvement". A static mutant, which code
executes once while it loads, needs a fresh worker. Ignoring the static
mutants, 6% of the mutants, saved another 50% on the same code.

Stryker's runner for vitest creates one Vitest instance and reruns the
tests in it for each mutant, with the active mutant passed in before each
run (`packages/vitest-runner/src/vitest-test-runner.ts`, version 10.0.0).
So the runner depends on Vitest's internals. Issue #5928 reports that an API
change between Vitest 4.0 and 4.1 broke per-test coverage and produced
false survivors.

mutineer writes each mutant to a temporary file and swaps it in through a
Vite plugin, so it loads a module per mutant. Vitest has offered plugins a
`configureVitest` hook since version 3.1, beside Vite's `transform` hook.
We found no tool that uses them to run mutation testing from inside vitest.

**What we concluded:** TypeScript has one transpile and hot reload, from a
separate command. Whether a vitest plugin can switch mutants between runs
without reloading modules is not established.

### Rust: one build is measured at two orders of magnitude, and the library form needs nightly

cargo-mutants copies the tree to a scratch directory, reused so that
builds are incremental. For each mutant it patches the file, checks that
`cargo test --no-run` compiles, runs `cargo test` and reverts the patch. Its
maintainer argues that "for most trees ... the test time will be longer
than incremental builds", and its documentation states that building each
mutant lets it try mutants that might not compile.

mutagen is an attribute macro, `#[mutate]`, added as a dev-dependency. It
compiles every mutation into the code and selects one at runtime through
`MUTATION_ID`. `cargo test` writes the list of mutations, so
`MUTATION_ID=1 cargo test` runs a mutant without mutagen's runner. A
procedural macro sees no type information, so every mutation must compile
without it, and constants, statics, patterns and unsafe code are not
mutated. mutagen needs nightly Rust, and its last push was on 2023-05-29.

mutest-rs replaces rustc with a driver that uses the compiler's type
analysis to generate only valid mutations. It compiles one meta-mutant per
crate, with `match` expressions on a global handle, and it needs a second
meta-mutant for each integration-test crate (Lévai, Shin and McMinn, ICST
2026). Its repository pins one nightly toolchain, nightly-2026-07-18. On
eight crates, its default configuration took a median of 22.9 seconds
where cargo-mutants took 1.6 hours. Across crates and its three
configurations, it was 8 to 2,252 times faster.

The sources disagree on how much the build costs. cargo-mutants' maintainer
expects tests to outweigh incremental builds. mutest-rs measured two
orders of magnitude at the median, and attributes the speedup both to the
single compilation and to its call graph, which drops unreachable mutations
and irrelevant tests. Our Go measurements reproduce both positions: the
build dominates when the suite is short, and the tests dominate when it is
long.

**What we concluded:** Rust has both properties in mutagen, which needs
nightly and is unmaintained, and one build at scale in mutest-rs, which
needs a pinned nightly compiler.

### What one build cannot mutate

- **Constants.** Stryker.NET states that mutant schemata cannot mutate
  constant values. mutagen leaves `const` and `static` expressions alone,
  and kanly and our prototype leave constant declarations alone.
- **Code that runs while the program loads, in a reused process.** StrykerJS
  needs a fresh worker for each static mutant, and PIT's mutants in static
  initializer blocks have no effect. Our prototype starts a process per
  mutant, so Go's package initialization runs under every mutant.
- **A mutant that does not compile.** One such mutant breaks the build of
  all. Stryker.NET removes the mutations that the compiler rejects
  and compiles again, usually one to three times. mutest-rs generates only
  valid mutations. cargo-mutants avoids the problem by building each mutant.
  Our prototype's rules compiled on the first build on all three packages.
- **Type context.** Go's untyped constants and TypeScript's type checker
  both need the instrumenter's help, as the Go and TypeScript sections
  describe.
- **Every run executes the switches.** Major measured 15% on average. Our
  Go prototype measured 1.9% and 12.6% with all five classes.
- **Mutants share a process.** PIT starts a JVM per class to limit leaked
  state. mutmut forks per mutant, and offers a fork server for test setups
  that are not safe to fork. mutest-rs runs tests on threads by default,
  and in separate processes for mutations that affect unsafe code.
- **A switch that no test executes.** Every mutant then survives, as all
  193 did when our overlay matched no file. A run that forces every switch
  to fail detects it.
- **Tests that differ between runs.** A verdict can then change from one
  run to the next in either mode. btree's tests seed their random keys from
  the clock, and go-humanize's initializer ranges over a map. 3 of 1,318
  verdicts differed between runs.

### A library that the test runner drives must leave itself out of the build it starts

- ooze and our prototype put the calling test behind a build tag that the
  nested `go test` does not set.
- mutflow compiles the code twice. Only the test compilation contains
  mutations.
- mutagen's documentation tells the caller to write
  `#[cfg_attr(test, mutate)]`, so only test builds contain mutations.

A library also becomes a dependency of the module under test, with its own
minimum language version. Adding our prototype raised go-humanize's go line
from 1.21 to 1.27.

**What we concluded:** the guard is one build condition in each language.
A library form makes its own version floor the floor of every module that
uses it.

## What we could not establish

- **A Java library that the test runner drives, of any maturity.** We read
  MutKt and JAllele only through their READMEs.
- **A tool that vitest drives.** Whether a vitest plugin can switch mutants
  without reloading modules needs a prototype.
- **Schemata for Go's generic code.** Our prototype skips operands whose
  type is a type parameter. We did not check how mutest and kanly handle
  them.
- **Go at the scale of a large module.** We measured three single-package
  modules whose builds took 0.2 to 0.4 seconds. A package with a longer
  build or many dependents moves the ratio towards one build.
- **Which survivors of the added classes a developer would act on.** We
  counted survivors on three packages. Google's productivity figures come
  from developer feedback on Google's code.
- **pytest-gremlins' speed.** Its design document sets speed goals and
  reports no measurement.

## What would change this answer

- A Go release that changes how `-overlay` matches paths, or removes it.
- A measurement in which the switches slow a suite by more than the builds
  they save.
- A tool that JUnit or vitest drives and that builds once, which would fill
  the two empty cells of the table.
- Developer feedback on our code that marks more than half of one operator
  class's survivors not worth a test.

## Sources

| # | Source | What it is | Retrieved | What it supports |
|---|---|---|---|---|
| 1 | Untch, Offutt and Harrold, "Mutation analysis using mutant schemata", ISSTA 1993, <https://doi.org/10.1145/154183.154265>, PDF at <https://www.albany.edu/faculty/offutt/research/papers/schema.pdf> | Peer-reviewed paper, abstract and §3 | 2026-10-02 | The technique, over 300% against interpretive systems, call overhead |
| 2 | Papadakis, Kintis, Zhang, Jia, Le Traon and Harman, "Mutation Testing Advances: An Analysis and Survey", Advances in Computers 112, 2019, <https://doi.org/10.1016/bs.adcom.2018.03.015>, preprint at <https://mutationtesting.uni.lu/survey.pdf> | Survey, §2, §5.1.2 and §5.2 read | 2026-10-02 | About 3 s to compile a mutant of a large project, schemata the most common technique, bytecode manipulation, Offutt et al.'s five operators as the minimum standard, deletion operators, operator selection against random sampling |
| 3 | Pizzoleto, Ferrari, Offutt, Fernandes and Ribeiro, "A systematic literature review of techniques and metrics to reduce the cost of mutation testing", JSS 157, 2019, <https://doi.org/10.1016/j.jss.2019.07.100> | Systematic review, abstract and §5 | 2026-10-02 | 153 studies, 21 techniques, metamutants among them |
| 4 | Just, Kapfhammer and Schweiggert, "Using conditional mutation to increase the efficiency of mutation analysis", AST 2011, <https://www.gregorykapfhammer.com/download/research/papers/key/Just2011a-paper.pdf> | Peer-reviewed paper | 2026-10-02 | 406,382 mutants for 33% more compile time, 15% more test time |
| 5 | Just, Schweiggert and Kapfhammer, "MAJOR: An efficient and extensible tool for mutation analysis in a Java compiler", ASE 2011, <https://homes.cs.washington.edu/~rjust/publ/major_compiler_ase_2011.pdf>, and the Major manual, <https://mutation-testing.org/doc/major.pdf> | Tool paper and manual | 2026-10-02 | Overhead from 1% to 29%, `-XMutator`, the Ant back-end |
| 6 | Schuler and Zeller, "Javalanche: efficient mutation testing for Java", ESEC/FSE 2009, <https://doi.org/10.1145/1595696.1595750> | Tool paper | 2026-10-02 | Bytecode rewriting with mutant schemata, µJava's schemata |
| 7 | Coles, Laurent, Henard, Papadakis and Ventresque, "PIT: a practical mutation testing tool for Java", ISSTA 2016, <https://doi.org/10.1145/2931037.2948707> | Tool paper | 2026-10-02 | Mutants inserted through the instrumentation API, a JVM per class |
| 8 | PIT FAQ, <https://pitest.org/faq/> | Maintainer's documentation | 2026-10-02 | Static initializer blocks are not re-run |
| 9 | Wang, Xiong, Shi, Zhang and Hao, "Faster mutation analysis via equivalence modulo states", ISSTA 2017, <https://arxiv.org/abs/1702.06689> | Peer-reviewed paper, abstract | 2026-10-02 | 2.56 times over split-stream execution |
| 10 | Lévai, Shin and McMinn, "mutest-rs: Flexible, efficient mutation analysis tool for Rust programs, using extensive static analysis", ICST 2026, <https://philmcminn.com/publications/levai2026b.pdf> | Peer-reviewed tool paper, read in full | 2026-10-02 | One meta-mutant per crate, Table II, the medians |
| 11 | mutest-rs repository, `rust-toolchain.toml`, <https://github.com/zalanlevai/mutest-rs> | Source code | 2026-10-02 | nightly-2026-07-18 |
| 12 | cargo-mutants documentation, <https://mutants.rs/how-it-works.html> and <https://mutants.rs/goals.html>, and discussion #209, <https://github.com/sourcefrog/cargo-mutants/discussions/209> | Maintainer's documentation and statement | 2026-10-02 | A build per mutant, tests outweigh incremental builds |
| 13 | mutagen README, <https://github.com/llogiq/mutagen> | Maintainer's documentation | 2026-10-02 | `#[mutate]`, `MUTATION_ID`, nightly, its limits, last push 2023-05-29 |
| 14 | gremlins v0.6.0, `internal/engine/executor.go`, `internal/engine/mappings.go`, `internal/gomodule/gomodule.go` and `internal/coverage/coverage.go`, <https://github.com/go-gremlins/gremlins> | Source code | 2026-10-02 | A `go test` per mutant, two mutant types on `-`, the first line of `go.mod` as the module name |
| 15 | ooze v0.2.0, README, `release.go`, `options.go`, the arithmetic and comparison viruses and `internal/laboratory/laboratory.go`, <https://github.com/gtramontina/ooze> | Source code | 2026-10-02 | A library run by `go test`, a repository copy and a build per mutant, sequential by default |
| 16 | mutest v0.6.2, README, `mutator/comparison.go` and `engine/instrument.go`, <https://github.com/fchimpan/mutest> | Source code and author's documentation | 2026-10-02 | One build per package, comparisons only, `-overlay`, the go 1.20 floor, the MIT licence in the README and no licence file |
| 17 | kanly v0.1.0 README, <https://github.com/devenjarvis/kanly> | Author's documentation | 2026-10-02 | Mutant schema generation, 17 operators, integer operands only, the limits on constants and literals |
| 18 | gomutants README, <https://github.com/gomutants/gomutants> | Author's documentation | 2026-10-02 | Overlay patches per mutant, per-test coverage routing |
| 19 | mutmut, `src/mutmut/mutation/trampoline.py`, `src/mutmut/workers/isolation.py`, `HISTORY.rst` and the documentation at <https://mutmut.readthedocs.io/en/latest/> | Source code and documentation | 2026-10-02 | Trampolines, `MUTANT_UNDER_TEST`, a fork per mutant, schemata since 3.0.0 |
| 20 | pytest-gremlins documentation, <https://pytest-gremlins.readthedocs.io/en/latest/>, and PyPI metadata, <https://pypi.org/project/pytest-gremlins/> | Author's documentation and registry metadata | 2026-10-02 | A pytest plugin with mutation switching, version 1.9.0, alpha |
| 21 | StrykerJS announcements of 4.0, <https://stryker-mutator.io/blog/announcing-stryker-4-mutation-switching/>, and 6.0, <https://stryker-mutator.io/blog/stryker-js-v6-expeditious-superior-mutations/>, and the page on static mutants, <https://stryker-mutator.io/docs/mutation-testing-elements/static-mutants/> | Maintainers' documentation | 2026-10-02 | 20% to 70%, 70% from hot reload, static mutants |
| 22 | `@stryker-mutator/vitest-runner` 10.0.0, `src/vitest-test-runner.ts`, and StrykerJS issue #5928, <https://github.com/stryker-mutator/stryker-js/issues/5928> | Source code and issue thread | 2026-10-02 | One Vitest instance, coupling to Vitest's API |
| 23 | Stryker.NET, "Mutant schemata", <https://stryker-mutator.io/docs/stryker-net/technical-reference/mutant-schemata/> | Maintainers' documentation | 2026-10-02 | Compile errors rolled back, constants not mutated |
| 24 | mutflow README and `DESIGN.md`, <https://github.com/anschnapp/mutflow> | Author's documentation | 2026-10-02 | A K2 plugin, a JUnit extension, two compilations |
| 25 | Arcmutate Kotlin support, <https://docs.arcmutate.com/docs/kotlin.html>, and pitest-kotlin, <https://github.com/pitest/pitest-kotlin> | Vendor documentation and maintainer's note | 2026-10-02 | Junk-mutation filtering, a commercial licence, the open plugin unmaintained |
| 26 | MutKt README, <https://github.com/rodrigotimoteo/mutkt>, and JAllele README, <https://github.com/gliptak/jallele> | Authors' documentation | 2026-10-02 | JVM tools that run from the test runner |
| 27 | mutineer README, <https://github.com/mutineerjs/mutineer>, and Vitest's plugin API, <https://vitest.dev/api/advanced/plugin> | Author's and maintainers' documentation | 2026-10-02 | A file swap per mutant, `configureVitest` since 3.1 |
| 28 | `go help build`, Go 1.27.1 | Toolchain documentation | 2026-10-02 | The `-overlay` flag |
| 29 | Our measurement: the prototype in both modes on go-humanize, btree and a sample of decimal, one mutant at a time, Go 1.27.1, four cores, a private build cache per mode, with `harness/rebench.sh`, `final.py` and `by-kind.py` | Own measurement | 2026-10-02 | The Go table, the build cache's growth, the timeouts, the survivors per class |
| 30 | Our run of the library form: `gomut.Check` from `TestMutation` in copies of go-humanize and btree, with `harness/head2head.sh` | Own measurement | 2026-10-02 | Both properties from `go test`, the raised go line |
| 31 | Our measurement: the schemata binary with no mutant active against the ordinary test binary, five alternating rounds on decimal and btree, with `harness/overhead.sh` | Own measurement | 2026-10-02 | 1.9% and 12.6% |
| 32 | Our probe: a `go 1.10` module with a `//go:build go1.18` file | Own measurement | 2026-10-02 | A build line raises one file's language version |
| 33 | GitHub repository metadata for the tools above | Registry metadata | 2026-10-02 | Stars and last push dates |
| 34 | Petrović, Ivanković, Fraser and Just, "Practical Mutation Testing at Scale: A view from Google", IEEE TSE, 2021, <https://arxiv.org/abs/2102.11378> | Peer-reviewed paper, §1 to §5 read | 2026-10-02 | The five operators, productivity and survivability per operator, arid nodes, 15% to 89%, one mutant per line |
| 35 | Vera-Pérez, Danglot, Monperrus and Baudry, "A comprehensive study of pseudo-tested methods", Empirical Software Engineering, 2019, <https://arxiv.org/abs/1807.05030> | Peer-reviewed paper, abstract | 2026-10-02 | Pseudo-tested methods in every subject |
| 36 | Our run of gremlins, ooze, mutest and kanly on copies of btree, one mutant at a time, each with a private build cache, with `harness/h2h-all.sh` | Own measurement | 2026-10-02 | The comparison table, the tools' defects and limits |
| 37 | Our count: every mutant of gremlins' five default operators in btree's compiled files, each checked with `go/types`, joined with each tool's list, with `gomut/cmd/count` and `harness/reconcile.py` | Own measurement | 2026-10-02 | The 126, the reasons the counts differ |
| 38 | Our hand mutations of btree and repeated runs of single mutants, with `harness/hand-mutate.sh`, `flaky.sh` and `extra-ops.py` | Own measurement | 2026-10-02 | The gap at line 523, kanly's false survivor, the 17 equivalent mutants, the verdicts that differ between runs |

## What we searched

| Search | Tool | Date | Useful |
|---|---|---|---|
| Go mutation testing as a library run from `go test` | Web search | 2026-10-02 | Yes: ooze, mutest, gooze, mutate4go |
| Mutant schemata in Go, compile once and switch by environment variable | Web search | 2026-10-02 | Yes: mutest, kanly, gomutants |
| StrykerJS mutation switching and hot reload | Web search | 2026-10-02 | Yes |
| mutmut 3 trampolines and its fork per mutant | Web search, then the source | 2026-10-02 | Yes |
| Rust meta-mutants, mutest-rs and cargo-mutants' design | Web search, then the paper and the documentation | 2026-10-02 | Yes |
| `mutant schemata`, `meta-mutant`, `mutation switching`, `split-stream` | arXiv | 2026-10-02 | AccMut and Mull only |
| Conditional mutation, Major, Javalanche, PIT | Web search, then the papers | 2026-10-02 | Yes |
| A pytest plugin with mutation switching | Web search | 2026-10-02 | Yes: pytest-gremlins |
| A mutation runner that vitest drives | Web search | 2026-10-02 | No: Stryker's runner and mutineer only |
| A Kotlin compiler plugin with mutant schemata | Web search | 2026-10-02 | Yes: mutflow |
| A JUnit extension that runs mutants inside the test JVM | Web search | 2026-10-02 | Only MutKt and JAllele |
| Mull and LLVM-level schemata | Web search | 2026-10-02 | Yes: one binary with every mutant behind a flag, for C and C++ |
| The survey's "3 seconds" per compile, and its reference | The survey's PDF, converted to text | 2026-10-02 | Yes: the trivial compiler equivalence study |
| Google's mutation operators, arid nodes and productivity | Web search, then the paper | 2026-10-02 | Yes: Petrović et al., TSE 2021 |
| Pseudo-tested methods and extreme mutation | Web search, then the abstract | 2026-10-02 | Yes: Vera-Pérez et al. |
| Sufficient mutant operators and selective mutation | Web search, then the survey | 2026-10-02 | Yes: Offutt et al.'s five operators, through the survey |
| PIT's extended operator set | Web search | 2026-10-02 | The abstract of Laurent et al., ICST 2017, only |
