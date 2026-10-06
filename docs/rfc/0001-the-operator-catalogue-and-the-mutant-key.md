---
rfc: 0001
title: The operator catalogue and the mutant key
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Draft
created: 2026-10-02
updated: 2026-10-06
discussion: none
supersedes: none
superseded-by: none
produces-adr: tbd
---

# RFC-0001: The operator catalogue and the mutant key

## Summary

A mutation engine measures a test suite by changing the code under test in
small, defined ways and checking that a test fails. This RFC defines those
changes once, for every language that dokimasia has an engine for. It
defines five operator classes and thirteen kinds, the code exempt from
mutation, the directive and the run option that make generated files
targets, the two ways to suppress a mutant, and a key that identifies one
mutant across runs.
Every engine applies the same catalogue, so a mutant in Go and the same
mutant in Java have the same kind, the same exclusions and a key built by
the same rule.

## Motivation

Mutation tools do not agree on what a mutant is, so their counts and scores
cannot be compared:

- **One package, counts from 56 to 566.** On google/btree v1.1.3, mutest
  listed 56 mutants, ooze 258, gremlins 318 and kanly 566. The type checker
  counts 126 mutants for gremlins' own five default operators in the code
  that Go 1.27 compiles.
- **Files outside the build.** btree has three implementation files behind
  build constraints, and Go 1.27 compiles one of them. gremlins and ooze
  read every `.go` file. 172 of gremlins' 318 mutants and 140 of ooze's 258
  were in code that no build contains.
- **Different operator sets.** mutest mutates only comparisons. kanly
  applies 17 operators, and its comparison operators accept integer operands
  only.
- **Different counting.** gremlins maps the `-` token to two operators that
  make the same edit. 17 mutants ran twice.

Across languages the gap is wider. PIT, StrykerJS, cargo-mutants and mutmut
each define their own operators under their own names. dokimasia offers
mutation testing in every language it supports, from one engine per
language written in that language. Every engine reports to one platform,
and without one definition that platform would compare numbers that count
different things.

The operator set also decides which gaps a run finds. Google's mutation
testing service applies five operator classes in ten languages, Go among
them. Over 16,935,148 mutants, developers marked the mutants shown to them
productive at these rates:

| Class | Productive |
|---|---|
| Relational operator replacement | 84.1% |
| Logical connector replacement | 83.2% |
| Statement removal | 82.7% |
| Arithmetic operator replacement | 75.4% |
| Unary operator insertion | 74.5% |

gremlins' defaults leave out the connector and statement classes. On btree,
kanly's operators outside gremlins' set left 58 survivors in functions where
every gremlins mutant was killed. One of them is a real gap. Changing
`return hit, false` to `return hit, true` at line 523 lets an iterator
continue after its callback returned false, and every btree test still
passes.

## Detailed design

### Components

| Component | What it defines | Where it is |
|---|---|---|
| The catalogue | The classes, the kinds, the rule families, the annotation and its keyword for every kind, the include directive and the catalogue version | `spec/catalogue.json` in mutate-spec |
| A language overlay | The syntax each kind applies to, how the language writes a zero value, the rules of each rule family, the compound statements that a family suppresses, the comment syntax of the directives, how scopes are named, and the overlay's version | `spec/overlays/<language>.json` in mutate-spec |
| The key | The identity of one mutant across runs | Computed by each engine from the rules below |

Each engine vendors the catalogue and its language's overlay. The run
protocol defines how an engine checks its vendored copy.

### The classes and kinds

A site is one place in the source that a class applies to. Each kind makes
one mutant at a site. The table uses Go's spelling:

| Class | Kind | Original | Mutant |
|---|---|---|---|
| Arithmetic | `aor` | `a + b`, `a - b`, `a * b`, `a / b`, `a % b` | `a - b`, `a + b`, `a / b`, `a * b`, `a * b` |
| | | `+=`, `-=`, `*=`, `/=`, `%=` | `-=`, `+=`, `/=`, `*=`, `*=` |
| Relational | `ror-boundary` | `<`, `<=`, `>`, `>=` | `<=`, `<`, `>=`, `>` |
| | `ror-true` | `<=`, `>=`, `==`, `!=` | `true` |
| | `ror-false` | `<`, `>`, `==`, `!=` | `false` |
| Logical connector | `lcr-left` | `a && b`, `a \|\| b` | `a` |
| | `lcr-right` | `a && b`, `a \|\| b` | `b` |
| | `lcr-true` | `a \|\| b` | `true` |
| | `lcr-false` | `a && b` | `false` |
| Unary insertion | `uoi-incdec` | `x++`, `x--` | `x--`, `x++` |
| | `uoi-not` | a boolean operand `x` that is not an operand of a connector, or `!x` | `!x`, or `x` |
| | `uoi-minus` | `-x`, where `x` is not a constant | `x` |
| Statement removal | `sbr-delete` | a statement | nothing |
| | `sbr-zero` | `return e1, e2` | the zero value of each result type |

Every kind obeys five invariants:

- **A mutant is well typed.** Its replacement has the type of the original,
  as the language's type checker decides. A kind does not apply where the
  replacement would not compile. In a language without static types, the
  overlay states the syntactic condition instead. A mutant that does not
  evaluate the only use of a name, in a language that rejects an unused
  name, keeps that use in its source behind a constant that skips it, such
  as `if false { … }` in Go, so it compiles wherever the original does. The
  overlay states how each kind writes it, and each mutant that the compiler
  still rejects, such as a division by a constant 0.
- **A mutant changes the program.** A kind does not apply where its
  replacement denotes the values of the original. `sbr-zero` has no site at
  a return whose every result is already the zero value of its type, such
  as `return Point{}, false` in Go. The overlay states which expressions
  are zero values.
- **Operands evaluate as the mutated expression does.** No operand
  evaluates twice, operands evaluate in source order, and a connector keeps
  its short-circuit order. `lcr-right` does not evaluate `a` at all,
  because the mutated expression is `b`.
- **A site and a kind identify one mutant.** A kind makes at most one
  mutant at a site.
- **A run activates one mutant at a time.**

Each language's overlay maps every kind onto the language's syntax, such as
`and` and `or` in Python, and settles three differences:

- **Connectors that return an operand.** Python's `and` and `or`, and `&&`
  and `||` in TypeScript, return one of their operands. `lcr-true` and
  `lcr-false` apply there only where the expression's type is boolean, or,
  without static types, where the expression is a condition.
- **No increment operator.** Python and Rust write `x += 1`, which `aor`
  covers. `uoi-incdec` has no sites in them.
- **No zero value.** `sbr-zero` returns the language's nearest equivalent,
  such as `None` in Python, and the overlay states it per result type.

The overlay writes each zero value as code in the language writes it, so a
record's replacement reads as code. Go's overlay writes `0`, `""`, `false`,
`nil` and `T{}`, and `*new(T)` only for a type parameter `T`, which has no
literal.

### Subsumed mutants

A set of mutants subsumes a mutant when every test set that detects each
mutant of the set also detects that mutant. Such a mutant costs a run and
adds a detected mutant to the score, and the tests that the set requires
already detect it. The relational and connector kinds make none:

- **An ordered comparison.** Its operands are in one of three regions:
  `a < b`, `a == b` or `a > b`. For `a < b`, the boundary mutant `a <= b`
  differs from the original only where `a == b`, and `false` only where
  `a < b`. `true` differs where `a <= b` does and more, and `a >= b`
  differs in every region, so a test set that detects the two kept
  mutants detects both. `<=`, `>` and `>=` mirror `<`, with `true` in place
  of `false` for `<=` and `>=`.
- **An equality.** `true` differs from `a == b` only where the operands
  differ, and `false` only where they are equal. `a != b` differs
  everywhere.
- **A connector.** `a`, `b` and `false` each differ from `a && b` for one
  pair of operand values: `a` true with `b` false, `a` false with `b`
  true, and both true. `a || b` and `true` differ where `a` or `b` alone
  does and more. A negation of either operand differs where `false` does,
  so no operand of a connector gets a `uoi-not` mutant. `||` mirrors `&&`
  with `true` in place of `false`.

Kaminski, Ammann and Offutt proved the relational hierarchy, and Just,
Kapfhammer and Schweiggert the connector's, for weak mutation: a test
detects a mutant when the mutated expression's value differs. Under strong
mutation, an assertion must also observe the difference, and Lindström and
Márki show that a test can then detect a kept mutant and miss a subsumed
one. Runs on two Go packages, of 2,913 and 1,306 mutants with the subsumed
mutants included, had no site where a subsumed mutant survived and every
kept mutant was detected.

### Removing a unary minus

`uoi-minus` removes a minus that the code wrote. No kind inserts one.

- **The same operator exists elsewhere.** PIT's invert-negatives mutator is
  active by default and applies to variables only, not to negative
  constants. StrykerJS and Stryker.NET turn `-a` into `+a`. gremlins v0.6.0
  enables its invert-negatives operator by default.
- **Sites are few, and cluster in numeric code.** The type checker counts
  these unary minus sites outside constants:

  | Package | Sites |
  |---|---|
  | google/btree v1.1.3 | 0 |
  | go-humanize v1.1.0 | 1 |
  | shopspring/decimal v1.4.0 | 34 |
  | bits-and-blooms/bitset v1.25.0 | 2 |
  | go-cmp v0.7.0, package `cmp` | 0 |

  decimal's sites negate exponents and precisions, such as `-places` and
  `-d.exp`, where a lost sign is a realistic defect.
- **Insertion is left out.** Google dropped absolute value insertion
  because negating times and counts gave unproductive mutants. Inserting a
  minus is that change.

### The kinds against Google's definitions

The classes are Google's. Four of them make fewer mutants per site than
Google's definitions:

| Class | Google's service | This catalogue |
|---|---|---|
| Arithmetic | `a + b` becomes `a`, `b`, `a - b`, `a * b`, `a / b` or `a % b` | One counterpart per operator, as StrykerJS defines it |
| Relational | `a > b` becomes `a < b`, `a <= b`, `a >= b`, `true` or `false` | `a >= b` and `false`. The other three are subsumed |
| Logical connector | `a && b` becomes `a`, `b`, `a \|\| b`, `true` or `false` | `a`, `b` and `false`. The other two are subsumed |
| Unary | `a` becomes `a++` or `a--` for a numeric operand, and `b` becomes `!b` | An existing `++` or `--` is swapped, a boolean operand outside a connector is negated, and a unary minus is removed |

Statement removal matches Google's. `sbr-zero` takes the place of deletion
for a `return` with results. The productivity figures in the motivation
come from Google's definitions. The relational and connector mutants left
out are subsumed, and nothing measures what the arithmetic and unary kinds
miss.

### The catalogue leaves literals alone

On btree, at least 17 of the 58 survivors read were equivalent mutants. All
17 were literal replacements, such as changing `len(n.children) > 0` to
`> 1` where a node has either no children or at least two. Google's service
has no literal operator either. The catalogue has none, and a run does not
catch a wrong constant.

### What is exempt from mutation

| Code | Reason |
|---|---|
| Code the build does not compile for the target platform | A mutant there cannot change any test's result. 172 of gremlins' 318 mutants on btree were of this kind |
| Test code | The run measures the tests. It does not change them |
| Generated code, by the language's own convention, unless the file contains the include directive or the run includes generated files | A survivor there points at the generator's input, which the developer edits instead of the file. Go marks such a file with a line that matches `^// Code generated .* DO NOT EDIT\.$`. The record lists each such file with the number of mutants that it would have, so a reader sees how much code a score leaves out |
| Expressions the compiler evaluates before the program runs | A runtime switch cannot be placed in a constant. Go's constants and array lengths, and Rust's `const` and `static` items, are of this kind |

An engine also skips any site that it cannot instrument for a reason of its
own, such as an operand whose type the engine cannot name. The run's record
lists every constant expression and every such site with its reason, so a
gap of the engine is visible next to the mutants it ran.

#### Generated files that are targets

A generator's output can be the product that a repository tests. A code
generator's conformance suite tests the code that the generator writes, and
a survivor in that code points at the generator or at a gap in the suite.
Such a generator writes the include directive into each file it generates,
as a line comment before the package clause:

```go
// Code generated by codecgen. DO NOT EDIT.
//dokimi:mutate-include

package wire
```

- The engine reads a file that contains the directive as the target's own
  code. Its mutants, its skipped sites and its annotations count as those
  of any other file.
- The directive is in the file, so every run of the target reads the same
  files, and two runs' scores count the same mutants.
- The overlay states where the directive is valid. In Go it is a line among
  the comments before the package clause, and a line anywhere else in the
  file has no effect.

The caller can also include every generated file of a run. The run then
measures generated code whose generator does not write the directive, such
as the output of a generator from outside the repository:

- The engine reads every generated file of the target as its own code, as
  if the file contained the directive.
- The record lists each generated file without the directive, with its
  mutants and whether the run included it. Two records' scores count the
  same mutants only when every generated file has the same inclusion in
  both.
- A file with the directive is the target's own code in every run, with or
  without the option. A generator whose output a repository tests writes
  the directive, so its scores compare across runs.

### Suppression

A suppressed mutant is created and listed in the record with the reason. It
is not run, and it does not count towards the score. A rule family or an
annotation suppresses it.

#### Rule families

The catalogue names five families of code whose mutants developers do not
act on. The overlay states each family's rules in its language: the APIs
whose calls are in the family, method rules, result rules, and argument
rules, which put one argument of a call into the family. A mutant is
suppressed when its site is a call that a rule puts into a family, or is
inside the arguments of one, or is an argument that a rule names.

| Family | APIs | Evidence |
|---|---|---|
| `logging` | Calls that write log records | Google's first rule family. Its rule marked 100 sampled nodes, and 99 were correct |
| `timing` | Calls that sleep, set a deadline or timeout, define a backoff, or wait for a service to become ready, and calls of the cancel function that such a call returns | Google's second family. Unit tests rarely test time, and use fake clocks when they do. A cancel function that is never called leaves its context live until the deadline, which only a check for leaks detects |
| `flags` | Calls that register a configuration flag | Google's third family |
| `capacity` | Arguments that only size an allocation: a capacity or a size hint | Google names memory reservation as an arid node. A mutant of a capacity survives, because the collection grows anyway |
| `helper` | Calls that mark a function as a test helper | The mark changes only the file and line that the test framework reports with a failure. A test sees that change only in the output of another test run, so the mutant survives every suite that does not read such output. Every assertion of an assertion library starts with such a call |

Google reports that its first three families raised the share of mutants
developers judged productive from about 15% to 80%. Its other rules took
the share to 89%.

An engine matches a call by the API that the type checker resolves it to.
It never matches a name pattern. `logger.Printf` is suppressed only when
`logger` has a type that the overlay lists. In a language without static
types, the overlay states how an import resolves a name.

A method rule is the one match by name. Libraries declare their own
interfaces with a test helper's mark, such as an interface `TB` with the
method `Helper()` of Go's `testing.TB`, and no list of APIs can name them
all. A method rule lists a family, a method, its signature, and the kind of
type that the call's receiver has. The type checker still resolves the
call:

- In Go, the rule `Helper` with the signature `func()` on an interface
  suppresses `tb.Helper()` for a `tb` of any interface type that declares
  that method.
- It does not suppress `m.Helper(1)`, whose signature differs, or
  `c.Helper()` on a value of a struct type.

The overlay lists each method rule beside the family's APIs.

A result rule matches a call by the value that the call calls. A deadline
API returns a function that ends the context before its deadline, and
deleting a call of that function moves the end of the context to the
deadline. A test of behaviour sees the change only when it waits on the
context after the call, or checks for a leaked goroutine or timer. A result
rule lists a family and a type. It puts a call of a variable into the
family when every value of the variable is a result of that type of the
family's APIs:

- In Go, the rule of the family `timing` with the type `context.CancelFunc`
  suppresses the deletion of `defer cancel()` and of `cancel()` after
  `ctx, cancel := context.WithTimeout(parent, d)`.
- It keeps the mutants of `cancel()` when `cancel` is the function of
  `context.WithCancel`, whose context has no deadline. It keeps them too
  when another assignment gives `cancel` a function of its own.
- The variable is declared inside a function's body, and no expression
  takes its address. The type checker then sees every value of the
  variable.

The overlay lists each result rule beside the method rules. It states which
assignments give a variable its values.

A compound statement whose bodies contain only calls that families
suppress, and whose header has no effect, is suppressed as a whole:

```go
if err != nil {
	log.Printf("warning: %v", err)
}
```

- Deleting the statement or changing its condition changes only whether
  the log record is written. The suppression of the call itself covers the
  same change.
- Every site inside the statement is suppressed, its own deletion and the
  mutants of its condition included. The record states the family of the
  statement's first such call as the rule.
- A body that also returns, assigns or calls an API outside the families
  keeps every mutant of the statement.
- A header that has an effect keeps the statement's deletion and the
  mutants of the parts of the header that have the effect. `if _, err :=
  w.Write(b); err != nil` writes, and deleting the statement removes the
  write.
- The condition of such an if, and the tag and the case expressions of
  such a switch, only choose which body runs. The family suppresses their
  mutants when they have no effect, so `err != nil` in that statement keeps
  none.
- A loop's condition decides how often its header runs, so a loop whose
  header has an effect keeps every mutant.

The rule is Google's: a compound node is arid when all its parts are
(Equation 1 of the paper). The catalogue adds the condition on the header,
and keeps the mutants of the parts of a header that have an effect.
The overlay states the language's compound statements, their bodies and
headers, and the expressions that have no effect.

A project cannot add, remove or narrow a family, so two projects' scores
count the same mutants.

#### Annotations

A line comment suppresses the mutants of the listed kinds on one line:

```go
if v > best { //dokimi:mutate-skip ror-boundary: assigning an equal value leaves best unchanged
	best = v
}
```

```go
//dokimi:mutate-skip sbr-delete: the lookup only saves time, and the value is the same without it
if v, ok := cache[key]; ok {
	return v
}
```

- On a line of its own, the annotation applies to the sites that start on
  the next line. After code, it applies to the sites on its own line.
- The kinds are a comma-separated list of kinds or classes, such as `ror` or
  `sbr-delete`, or the catalogue's keyword for every kind, `all`.
- The reason after the colon is required. An annotation without one fails
  the run.
- An annotation that does not suppress any mutant fails the run, so an
  annotation is removed when the code it excuses changes.
- An annotation covers one line. No annotation covers a function or a file.

The overlay states the comment syntax, such as `#dokimi:mutate-skip` in
Python. A suppressed mutant's record entry contains the reason.

### The key

The platform follows one survivor over time, and compares two runs'
verdicts, by the mutant's key. The engine computes the key from five
fields:

| Field | Value |
|---|---|
| `file` | The file's path relative to the module or project root, with `/` as the separator |
| `scope` | The innermost named declaration around the site, as the overlay spells it, such as `BTreeG.Delete` in Go |
| `kind` | The kind |
| `tokens` | The site's tokens, joined by single spaces. Comments and whitespace are left out |
| `occurrence` | The site's index, from 0 in source order, among the sites with the same `file`, `scope`, `kind` and `tokens` |

It takes the first 16 hexadecimal digits of the SHA-256 digest of these
bytes, with `occurrence` in decimal and NUL as the byte 0:

```text
"dokimi-mutate-key/1" NUL file NUL scope NUL kind NUL tokens NUL occurrence
```

| Change to the code | Effect on the key |
|---|---|
| Lines added or removed elsewhere in the file | None |
| Whitespace or comments changed inside the site | None |
| The site's tokens changed | A new key |
| The scope renamed, or the code moved to another file | A new key |
| An identical site added before it in the same scope | The occurrence and the key of each such site after it change |

With 64 bits, the chance that two mutants of one project share a key is
below one in a billion up to about 190,000 mutants.

### Versions

The catalogue and each overlay have a semantic version. Every run's record
states the catalogue's version in `catalogue`, and the version of the
overlay of the target's language in `overlay`.

| Version | The catalogue's | An overlay's |
|---|---|---|
| Major | Changes the meaning of a kind, or removes a kind | Changes the mutant that a kind makes in the language, or removes a kind from the language |
| Minor | Adds a kind, a rule family or a directive | Adds or removes sites of a kind, changes how a mutant's source is written, changes which mutants a rule family, a skip or the toolchain excludes, or changes how a key's scope or tokens are written |
| Patch | Changes wording only | Changes wording only |

A rule family's APIs, method rules and result rules are part of the
overlay, so a change of them is a minor version of the overlay. Two
records' scores are comparable only when both state the same catalogue
version and the same overlay version. A change of one language's overlay
does not change another language's version. The scores of the other
languages remain comparable across it.

## Alternatives considered

### gremlins' five default operators

Arithmetic, conditional boundary, conditional negation, increment and
decrement, and negation inversion, which gremlins v0.6.0 enables by default.

**Why not:** they leave out the logical connector and statement removal
classes, which developers at Google judged productive 83.2% and 82.7% of
the time. On btree, operators outside this set found a gap that every
gremlins mutant missed.

### Google's operator definitions in full

Make every mutant of Google's table: six per arithmetic site, five per
ordered comparison, and `a++` and `a--` for every numeric operand.

**Why not:** an arithmetic site would get six mutants instead of one, and
every numeric variable two more, and nothing measures what the extra
mutants find. The narrower kinds are the ones measured on three Go packages.

### The sufficient sets in full

Kaminski, Ammann and Offutt, and Just, Kapfhammer and Schweiggert, make
for each region a mutant that differs from the original in that region
only. Beyond the kept mutants, these are `a != b` for `a < b`, which
differs only where `a > b`, `a == b` for `a <= b`, `a <= b` and `a >= b`
for an `==` of ordered operands, and `a == b` for `a && b`. The Major
mutation framework makes these sets.

**Why not:** in Go, the operands of a comparison often never take a value
in the region of such a mutant. The mutant is then equivalent. The type
checker counts these comparisons in 17 packages of public code:
google/btree v1.1.3, go-humanize v1.1.0, shopspring/decimal v1.4.0,
bitset v1.25.0, go-cmp v0.7.0's `cmp`, and twelve packages of Go 1.27.1's
standard library:

| Comparison | Sites | The added mutant |
|---|---|---|
| `len`, `cap` or an unsigned value against 0, ordered | 116 | Equivalent: `len(s) != 0` for `len(s) > 0` |
| The same, with `==` or `!=` | 221 | Equivalent: `len(s) <= 0` for `len(s) == 0` |
| A signed value against 0 | 549 | Equivalent wherever the value is a count, which Go types as `int` |
| The condition of a `for` loop | 243 | Equivalent for a counter that starts below its bound: `i != n` for `i < n` |
| Two ordered comparisons of one operand, joined by a connector | 101 | Equivalent for a range check such as `lo <= x && x < hi`, whose operands cannot both be false |

Just, Kurtz and Ammann found `<` replaced by `!=` highly likely to be
equivalent in a loop's condition. Google's service suppresses the mutants
of a comparison of a length with zero for the same reason. A gate that
requires every mutant killed would need an annotation for each.

### A larger set with literal replacement

kanly applies 17 operators. PIT's inline constant mutators and StrykerJS's
string and boolean literal mutators replace literals.

**Why not:** every equivalent mutant read on btree came from a literal
replacement, and no test can kill an equivalent mutant. A gate that
requires every mutant killed would need an annotation for each.

### Each language's established tool's operators

Run PIT's mutators on Java, StrykerJS's on TypeScript, and so on, and map
their results onto one record.

**Why not:** no common definition exists to map onto. The same Go package
gave counts from 56 to 566 across four tools in one language.

### One mutant per line, as Google's service selects

Google generates at most one mutant per covered, changed line. That gave a
median of 7 mutants per change, against 820 for every mutant of the same
lines.

**Why not:** one mutant per line leaves the line's other mutants without a
verdict. A score needs a verdict for every mutant. A run for one change
bounds the cost by its selection instead: it keeps every mutant on the
lines that the change touched, and only those.

### A key from the position

`file:line:column`, or PIT's identity: class, method and descriptor, the
indexes of the mutated bytecode instructions, and the mutator.

**Why not:** a line added above the site changes a position key. An
instruction added earlier in the method changes PIT's. The platform would
see one survivor as a new one after every unrelated edit.

### Matching against the previous run with a diff

StrykerJS's incremental mode diffs the code and test files against the
previous report and matches mutants across the diff.

**Why not:** every consumer of the record would need the previous source and
a diff algorithm. A key compares in one equality check, and the platform
does not store source.

### Suppression that each project configures

StrykerJS accepts ignore plugins. Each plugin's `shouldIgnore` method
receives a syntax node and returns a reason to ignore its mutants.

**Why not:** two projects' scores would count different mutants. Fixed
families keep scores comparable, and annotations cover the cases a family
does not.

### No rule families, only annotations

**Why not:** before its rules, developers at Google marked 85% of the
mutants shown to them unproductive. One annotation per such line would be
the largest part of a project's mutation testing work.

### Generators named by the run

A run option or a project's configuration lists the generators whose files
are targets, and the engine matches each name in a file's generated header,
such as `codecgen` in `// Code generated by codecgen. DO NOT EDIT.`

**Why not:** the generator's name in a header is free text, which no
language defines. The run option that includes every generated file names
no generator, and the record states which files it included.

### Each library's helper interface in the overlay

List `TB` of every assertion library by its full name, beside the standard
library's `testing.TB`.

**Why not:** the list would always lag the libraries. A new library's
helper marks would make mutants until a minor version of the overlay listed
its interface.

### The cancel function of every context

Suppress the calls of the cancel function of `context.WithCancel` too. go
vet's lostcancel analyzer requires a call of every cancel function.

**Why not:** a context without a deadline ends only when its cancel
function runs or its parent ends. Deleting the call leaves the work under
the context running, which a test of behaviour detects, such as a test
that waits for a worker to stop.

### One version for the catalogue and every overlay

Raise the catalogue's version for every change of an overlay.

**Why not:** a change of one language's overlay would make the scores of
every language incomparable across it, also where the language's mutants
did not change.

### A compound statement suppressed by its bodies alone

Suppress a compound statement whose bodies contain only suppressed calls,
whatever its header does, as a reading of Google's Equation 1 that counts
only the bodies as the statement's parts.

**Why not:** a header can have behaviour of its own. Deleting
`if err := s.flush(); err != nil { log.Print(err) }` removes the flush, and
a mutant of an argument of `s.flush` changes what it writes. Neither change
is in a family.

### Annotations at the scope of a function or file

StrykerJS disables mutators for the rest of a file, mutmut for a block or a
range, and cargo-mutants for a whole function.

**Why not:** a reason written once for a whole function covers mutants that
nobody read. An annotation per line keeps each exclusion next to the code it
excuses, in the same review.

## Drawbacks

- **The rule families are unsound.** A call to a logging API whose arguments
  compute behaviour worth testing loses those mutants. A compound statement
  that only logs loses the mutants of its condition, which a test that
  reads the log could detect. A test that waits on a context after its
  cancel call, or checks for leaked timers, loses the deletion of that
  call. The record lists every suppressed mutant. How many of them a
  developer would act on is not measured.
- **Subsumption is proven for weak mutation only.** A test can detect a
  kept mutant and miss a subsumed one when no assertion observes the
  subsumed mutant's difference. A gap that only the subsumed mutant would
  show is then not reported.
- **An overlay per language.** Each maps 13 kinds onto its syntax and lists
  the APIs of five families. A new logging library in a language is a minor
  version of that language's overlay.
- **The helper family hides a test of the reported location.** A suite
  that checks the file and line a failure reports loses the mutants that
  would show a missing mark.
- **A marked or included generated file counts as hand-written code.** One
  change to a generator can add survivors in every file that it writes.
- **Including generated files changes what a score counts.** A run with
  the option and a run without it compare only through the record's list
  of generated files.
- **Scores compare only within a catalogue version and an overlay
  version.** Every minor version of either breaks the platform's trend for
  the projects it affects.
- **A run does not test constants.** Without literal replacement, a run
  cannot find a test that never checks a constant's value.
- **The kinds are narrower than Google's.** A suite whose tests all have
  `a == 0` kills `a + b` becoming `a - b`, and never sees `a + b` becoming
  `b`, a mutant that Google's arithmetic operator makes and this catalogue
  does not.
- **A rename restarts a mutant's history.** The key includes the scope and
  the file.
- **Per-line annotations are verbose.** A function with ten equivalent
  mutants on ten lines needs ten annotations.

## Open questions

- Does the `timing` family hide mutants that matter in code whose behaviour
  is time, such as a rate limiter? It suppresses only timing calls, the
  expressions written inside their arguments, and the calls of the cancel
  functions they return, so the limiter's own arithmetic keeps its
  mutants. Nobody has reviewed a sample of suppressed mutants.

## Unresolved and future work

- Equivalent-mutant detection by comparing compiled code, the trivial
  compiler equivalence technique, is not proposed. It would remove some
  equivalent mutants without an annotation.

## References

| What | Where |
|---|---|
| Practical Mutation Testing at Scale: A view from Google. Petrović, Ivanković, Fraser and Just, IEEE TSE 2021. The five classes and their definitions in Table I, productivity per class, absolute value insertion dropped, arid nodes and Equation 1, the 15% to 80% and 89% figures, the logging rule's 99 of 100, the collection size heuristic of Appendix A.1.20, one mutant per line | https://arxiv.org/abs/2102.11378 |
| Better predicate testing. Kaminski, Ammann and Offutt, AST 2011. The fault hierarchy of each relational operator: three of seven mutants are necessary | https://doi.org/10.1145/1982595.1982608 |
| Do redundant mutants affect the effectiveness and efficiency of mutation analysis? Just, Kapfhammer and Schweiggert, ICST 2012. The connector's sufficient set, which subsumes the negation of each operand | https://homes.cs.washington.edu/~rjust/publ/non_redundant_mutants_icst_2012.pdf |
| Higher accuracy and lower run time: efficient mutation analysis using non-redundant mutation operators. Just and Schweiggert, STVR 25(5-7), 2015. The hierarchies in composed expressions, and run time 22% lower on 10 programs | https://homes.cs.washington.edu/~rjust/publ/non_redundant_mutants_jstvr_2014.pdf |
| On strong mutation and the theory of subsuming logic-based mutants. Lindström and Márki, STVR 29(1-2), 2019. The hierarchies are not valid under strong mutation | https://doi.org/10.1002/stvr.1667 |
| Inferring mutant utility from program context. Just, Kurtz and Ammann, ISSTA 2017. `<` replaced by `!=` is likely equivalent in a loop's condition | https://homes.cs.washington.edu/~rjust/publ/customized_mutants_issta_2017.pdf |
| The Major mutation framework's mml: the sufficient sets as replacement lists | https://mutation-testing.org/tutorial.html |
| Mutation Testing Advances: An Analysis and Survey. Papadakis et al., Advances in Computers 112, 2019. The five-operator minimum standard, trivial compiler equivalence | https://mutationtesting.uni.lu/survey.pdf |
| Research-0001, mutation testing as a library from one build. The btree counts, the 126, the tools' defects, the 17 equivalent literal mutants, the gap at line 523 | `docs/research/0001-mutation-testing-as-a-library-from-one-build.md` |
| The unary minus sites of five Go packages, counted with the type checker | `docs/research/0001-mutation-testing-as-a-library-from-one-build/probes/negations/` |
| gremlins v0.6.0's operators enabled by default, including `InvertNegatives` | https://github.com/go-gremlins/gremlins/blob/v0.6.0/internal/configuration/mutantenabled.go |
| PIT's mutators: the inline constant mutators, and `INVERT_NEGS`, active by default | https://pitest.org/quickstart/mutators/ |
| StrykerJS's mutators: string and boolean literals, the unary operator, and the arithmetic operator's one counterpart per operator | https://stryker-mutator.io/docs/mutation-testing-elements/supported-mutators/ |
| PIT's `MutationIdentifier`: location, instruction indexes and mutator | https://github.com/hcoles/pitest/blob/master/pitest/src/main/java/org/pitest/mutationtest/engine/MutationIdentifier.java |
| StrykerJS incremental mode: a diff against the previous report | https://stryker-mutator.io/docs/stryker-js/incremental/ |
| StrykerJS disable comments and ignore plugins | https://stryker-mutator.io/docs/stryker-js/disable-mutants/ |
| mutmut's `# pragma: no mutate` | https://mutmut.readthedocs.io/en/latest/ |
| cargo-mutants' `#[mutants::skip]` | https://mutants.rs/attrs.html |
| Go's convention for generated files, `go help generate` | https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source |
| Go's `Helper` method, which marks the calling function as a test helper | https://pkg.go.dev/testing#T.Helper |
| go vet's lostcancel analyzer: the cancel function that `context.WithCancel`, `WithTimeout`, `WithDeadline` and their variants return must be called, or the context remains live until its parent is cancelled | https://pkg.go.dev/golang.org/x/tools/go/analysis/passes/lostcancel |
| Interfaces that libraries declare with the method `Helper()`: testify's `tHelper` in `assert/assertions.go` and `mock/mock.go`, and gotest.tools' `helperT` in `skip/skip.go` | https://github.com/stretchr/testify, https://github.com/gotestyourself/gotest.tools |
