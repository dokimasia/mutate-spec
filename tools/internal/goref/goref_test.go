// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package goref_test

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"go.dokimi.dev/assert"
	"go.dokimi.dev/assert/expect"
	"go.dokimi.dev/assert/golden"
	"go.dokimi.dev/assert/prop"

	"mutate-spec/tools/internal/goref"
	"mutate-spec/tools/internal/spec"
)

// repository is the root of the checkout whose definition the tests read.
const repository = "../../.."

// The files of a fixture.
const (
	// goMod is the name of the module file.
	goMod = "go.mod"
	// module is the module file that fixture writes where a test states none.
	module = "module fixture\n\ngo 1.21\n"
	// goSuffix ends the name of a Go file, and testSuffix of a test file.
	goSuffix   = ".go"
	testSuffix = "_test.go"
)

// compiler names the compiler whose export data the importer of compiles
// reads, and fixturePath is the import path of the package that it checks.
const (
	compiler    = "gc"
	fixturePath = "fixture"
)

// The run of a fixture's tests under a mutant's form: the go command that
// runs them, the case of the corpus whose fixture they test, and the text
// of go test's output for a failed test and for a build that fails.
const (
	goCommand    = "go"
	shadowedCase = "shadowed"
	testFailed   = "--- FAIL"
	buildFailed  = "[build failed]"
)

// The mode of the files that the tests write, and of a file that a test
// makes unreadable.
const (
	fileMode     = 0o644
	noAccessMode = 0o000
)

// The fixtures of more than one case.
const (
	kinds = `package fixture

func kinds(a, b int, ok bool, xs []int) int {
	total := a + b
	total -= b
	if a < b && ok {
		total++
	}
	if xs == nil || a >= b {
		return -a
	}
	if !ok {
		return total
	}
	return 0
}
`

	steps = `package fixture

func steps(n int) int {
	if n > 0 {
		n--
	}
	if n > 0 {
		n--
	}
	return n
}
`

	clamp = `package fixture

func clamp(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
`
)

// keyPattern is a key as the record writes it.
const keyPattern = `^[0-9a-f]{16}$`

// keyFields are the generated fields of a key.
type keyFields struct {
	Prefix, File, Scope, Kind, Tokens string
	Occurrence                        int
}

func TestGoref(t *testing.T) {
	t.Parallel()

	t.Run("Enumerate", func(t *testing.T) {
		t.Parallel()

		t.Run("makes every kind of the catalogue in source order", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"kinds.go": kinds})
			r := enumerate(t, dir, goref.Options{})
			golden.Match(t, "kinds.txt", []byte(listing(r, "")), golden.ShouldUpdate())
			expect.That(t, r.Expect.Skipped).Empty("no site is skipped")
			expect.Empty(t, r.Expect.Errors, "no annotation fails")
			expect.Empty(t, r.Static, "every mutant runs")
			m := r.Expect.Mutants[1]
			expect.Equal(t, m.File, "kinds.go", "the compound assignment is in kinds.go")
			expect.Equal(t, m.Start, spec.Position{Line: 5, Column: 2}, "and starts at its target")
			expect.Equal(t, m.End, spec.Position{Line: 5, Column: 12}, "and ends after its value")
			formsCompile(t, dir, r)
		})

		t.Run("names the scope of a function, a method and a package-level variable", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"scopes.go": `package fixture

var limit = 1 + compute(3)

func compute(n int) int { return n }

type box[T any] struct{ n int }

func (b *box[T]) grow(by int) int {
	f := func() int { return b.n + by }
	return f()
}
`}), goref.Options{})
			assert.Equal(t, listing(r, ""), `limit aor 0: 1 + compute(3) -> "1 - compute(3)"
compute sbr-zero 0: return n -> "return 0"
box.grow sbr-zero 0: return b.n + by -> "return 0"
box.grow aor 0: b.n + by -> "b.n - by"
box.grow sbr-zero 1: return f() -> "return 0"
`, "each site has the scope of its declaration")
		})

		t.Run("names the scope of a receiver whose type is in parentheses", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"t.go": `package fixture

type T struct{ x int }

func (p (*T)) Inc() int { return p.x + 1 }

func (v (T)) Dec() int { return v.x - 1 }

func (p *(T)) Double() int { return p.x * 2 }
`}), goref.Options{})
			assert.Equal(t, listing(r, spec.AOR), `T.Inc aor 0: p.x + 1 -> "p.x - 1"
T.Dec aor 0: v.x - 1 -> "v.x + 1"
T.Double aor 0: p.x * 2 -> "p.x / 2"
`, "the scope reads the receiver's type without its parentheses")
		})

		t.Run("keys identical sites by their occurrence", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"steps.go": steps}), goref.Options{})
			var falses []spec.ExpectMutant
			for _, m := range r.Expect.Mutants {
				if m.Kind == spec.RORFalse {
					falses = append(falses, m)
				}
			}
			assert.Length(t, falses, 2, "both comparisons have a ror-false mutant")
			for i, m := range falses {
				expect.Equal(t, m.Nth, i, "the nth counts the sites of the scope and the kind")
				expect.Equal(t, m.Occurrence, i, "and the occurrence counts the sites of one key's fields")
			}
			expect.NotEqual(t, falses[0].Key, falses[1].Key, "the two sites have two keys")
			expect.Equal(t, falses[1].Key, "681ab23c877298d7", "and the second key is the digest of its fields")
		})

		t.Run("keeps the keys of the sites when code above them moves", func(t *testing.T) {
			t.Parallel()
			before := enumerate(t, fixture(t, map[string]string{"steps.go": steps}), goref.Options{})
			moved := strings.Replace(steps, "func steps", "func other() {}\n\n// steps counts down.\nfunc steps", 1)
			after := enumerate(t, fixture(t, map[string]string{"steps.go": moved}), goref.Options{})
			assert.Length(t, after.Expect.Mutants, len(before.Expect.Mutants), "the move adds no mutant")
			for i, m := range before.Expect.Mutants {
				a := after.Expect.Mutants[i]
				expect.Equal(t, a.Key, m.Key, "a site keeps its key")
				expect.Equal(t, a.Start.Line, m.Start.Line+3, "and moves three lines down")
			}
		})

		t.Run("lists each skipped site with its reason", func(t *testing.T) {
			t.Parallel()
			const skips = `package fixture

import (
	"math"
	"time"
)

const big = 4*1024 + 1

const debug = big > 4096 && big < 8192

const flags = 1 << 3

const one = 1

var buf [2 * 8]byte

type flag bool

type count int

type integer interface{ ~int | ~int64 }

type number interface{ integer | ~float64 }

func generic[T int | float64](a, b T) T {
	return a + b
}

func less[T int | float64](a, b T) bool {
	return a < b
}

func add[T int | float64](a, b T) T {
	a += b
	return a
}

func neg[T int | float64](x T) T {
	return -x
}

func upto[T int | float64](n T) {
	for i := T(0); i < n; i++ {
	}
}

func single[T int](a, b T) T {
	return a * b
}

func nested[T number](a, b T) T {
	return a - b
}

func named(n int) flag {
	return n < 4
}

func same(a, b int) flag {
	return a == b
}

func both(n int) flag {
	return n > 0 && n < 9
}

func recovers(xs []int) bool {
	return len(xs) > 0 && recover() == nil
}

func target(xs []int) {
	xs[index()] += 1
}

func head(xs []int) {
	for xs[0] = 0; xs[0] < 3; xs[0]++ {
	}
}

func index() int { return 0 }

func shift(d time.Duration, n uint) bool {
	return d < 1<<n
}

func later(d time.Duration, n uint) time.Duration {
	return d + one<<n
}

func limits(d time.Duration, n uint) (bool, bool, bool, bool) {
	return d < math.MaxInt8<<n, d > -1<<n, d >= (1+2)<<n, d <= min(1, 2)<<n
}

func typed(d time.Duration, n uint) (bool, bool) {
	return d < time.Duration(1)<<n, d < d<<n
}

func over(c count, n uint) bool {
	return c > 1<<n
}
`
			r := enumerate(t, fixture(t, map[string]string{"skips.go": skips}), goref.Options{})
			expect.Equal(t, skipped(r, skips), `4*1024 + 1: constant expression
big > 4096 && big < 8192: constant expression
2 * 8: constant expression
a + b: operand of type-parameter type
a < b: operand of type-parameter type
a += b: operand of type-parameter type
-x: operand of type-parameter type
i < n: operand of type-parameter type
i++: operand of type-parameter type
a * b: operand of type-parameter type
a - b: operand of type-parameter type
n < 4: named boolean result
a == b: named boolean result
n > 0 && n < 9: named boolean result
n > 0: named boolean result
n < 9: named boolean result
xs[index()] += 1: assignment target with side effects
xs[0]++: assignment target with side effects
d < 1<<n: untyped constant in a non-constant shift
d + one<<n: untyped constant in a non-constant shift
d < math.MaxInt8<<n: untyped constant in a non-constant shift
d > -1<<n: untyped constant in a non-constant shift
d >= (1+2)<<n: untyped constant in a non-constant shift
1+2: constant expression
d <= min(1, 2)<<n: untyped constant in a non-constant shift
`, "each skipped site states the overlay's reason")
			expect.Equal(t, listing(r, spec.RORBoundary), `recovers ror-boundary 0: len(xs) > 0 -> "len(xs) >= 0"
head ror-boundary 0: xs[0] < 3 -> "xs[0] <= 3"
typed ror-boundary 0: d < time.Duration(1)<<n -> "d <= time.Duration(1)<<n"
typed ror-boundary 1: d < d<<n -> "d <= d<<n"
over ror-boundary 0: c > 1<<n -> "c >= 1<<n"
`, "a shift of a typed operand, or under a type that the package names, keeps its comparison")
			expect.Equal(t, listing(r, "lcr-"), `recovers lcr-left 0: len(xs) > 0 && recover() == nil -> "len(xs) > 0"
recovers lcr-right 0: len(xs) > 0 && recover() == nil -> "recover() == nil"
recovers lcr-false 0: len(xs) > 0 && recover() == nil -> "false"
`, "a connector of plain booleans keeps its mutants")
		})

		t.Run("lists the sites of several files in file order", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{
				"a.go": `package fixture

import "log"

func first(xs []int) {
	log.Printf("%d %d %d", xs[0], xs[1], xs[2])
	xs[at()] += 1
}

func at() int { return 0 }
`,
				// The sites of second are at the offsets of the logging call in
				// a.go, which suppresses nothing in another file.
				"b.go": `package fixture

func second(xs []int) (int, error) {
	xs[at()] -= 1
	return xs[0] + 1, nil
}
`,
			}), goref.Options{})
			expect.Equal(t, listing(r, ""), `first sbr-delete 0: log.Printf("%d %d %d", xs[0], xs[1], xs[2]) -> "" [logging]
first sbr-delete 1: xs[at()] += 1 -> ""
second sbr-delete 0: xs[at()] -= 1 -> ""
second sbr-zero 0: return xs[0] + 1, nil -> "return 0, nil"
second aor 0: xs[0] + 1 -> "xs[0] - 1"
`, "the mutants of a.go precede those of b.go, whose sites no rule of a.go suppresses")
			expect.Equal(t, r.Expect.Skipped, []spec.Skip{
				{File: "a.go", Start: spec.Position{Line: 7, Column: 2}, End: spec.Position{Line: 7, Column: 15}, Reason: spec.SkipSideEffects},
				{File: "b.go", Start: spec.Position{Line: 4, Column: 2}, End: spec.Position{Line: 4, Column: 15}, Reason: spec.SkipSideEffects},
			}, "the skipped sites of a.go precede those of b.go")
		})

		t.Run("negates only boolean values", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"positions.go": `package fixture

type pair struct{ ok bool }

func positions(m map[string]bool, p *pair, ch chan bool, f func() bool) bool {
	v, found := m["k"]
	var w, present = m["j"]
	p.ok = v
	q := &p.ok
	_ = map[bool]int{found: 1}
	_ = pair{ok: present}
	f()
	ch <- *q
	defer f()
	return !w
}
`})
			r := enumerate(t, dir, goref.Options{})
			assert.Equal(t, listing(r, spec.UOINot), `positions uoi-not 0: v -> "!v"
positions uoi-not 1: present -> "!present"
positions uoi-not 2: *q -> "!*q"
positions uoi-not 3: !w -> "w"
`, "a value of bool is negated where the negation compiles and changes the program")
			formsCompile(t, dir, r)
		})

		t.Run("negates no operand in parentheses where the operand without them has no negation", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"parens.go": `package fixture

func parens(b *bool, ok bool) (*bool, bool) {
	(*b) = true
	p := &(ok)
	return p, !(ok)
}
`})
			r := enumerate(t, dir, goref.Options{})
			assert.Equal(t, listing(r, spec.UOINot), `parens uoi-not 0: !(ok) -> "(ok)"
`, "the target, the operand of & and the operand of ! keep their position in parentheses")
			formsCompile(t, dir, r)
		})

		t.Run("makes no mutant of arithmetic on strings or complex numbers", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"text.go": `package fixture

func greet(s string) string {
	s += "!"
	return s + "?"
}

func step(c complex128) complex128 {
	c++
	return c * 2i
}
`}), goref.Options{})
			expect.Equal(t, listing(r, ""), `greet sbr-delete 0: s += "!" -> ""
greet sbr-zero 0: return s + "?" -> "return \"\""
step sbr-zero 0: return c * 2i -> "return 0"
`, "only the statements and the returns have mutants")
		})

		t.Run("mutates an increment only in a statement list or a for loop's post statement", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"bump.go": `package fixture

func bump(n int) int {
	if n++; n > 1 {
		n--
	}
	for n++; n < 10; n += 2 {
	}
	return n
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, ""), `bump sbr-delete 0: if n++; n > 1 { n-- } -> ""
bump ror-boundary 0: n > 1 -> "n >= 1"
bump ror-false 0: n > 1 -> "false"
bump uoi-incdec 0: n-- -> "n++"
bump sbr-delete 1: for n++; n < 10; n += 2 { } -> ""
bump ror-boundary 1: n < 10 -> "n <= 10"
bump ror-false 1: n < 10 -> "false"
bump aor 0: n += 2 -> "n -= 2"
bump sbr-zero 0: return n -> "return 0"
`, "the increments of the init statements have no mutant")
			formsCompile(t, dir, r)
		})

		t.Run("mutates a compound assignment to a field of a variable", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"counter.go": `package fixture

type counter struct{ n int }

func (c *counter) add(by int) {
	c.n += by
}
`}), goref.Options{})
			expect.Equal(t, listing(r, ""), `counter.add aor 0: c.n += by -> "c.n -= by"
counter.add sbr-delete 0: c.n += by -> ""
`, "a chain of selectors on an identifier has no side effect")
			expect.Empty(t, r.Expect.Skipped, "and the assignment is not skipped")
		})

		t.Run("keeps the statements that a deletion breaks", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"ends.go": `package fixture

func ends(n int) (int, error) {
	if n > 0 {
		return 0, nil
	}
	if n > 1 {
	loop:
		for {
			break loop
		}
	}
	go func() {}()
	panic("unreachable")
}
`}), goref.Options{})
			assert.Equal(t, listing(r, "sbr-"), `ends sbr-delete 0: if n > 0 { return 0, nil } -> ""
ends sbr-delete 1: go func() {}() -> ""
`, "a statement with a label and a terminating last statement are kept")
		})

		t.Run("keeps the final statement of a list only when it is terminating", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"final.go": `package fixture

func loop(ch chan int) {
	for {
		if <-ch == 0 {
			break
		}
	}
}

func forever(ch chan int) int {
	for {
		<-ch
	}
}

func classify(n int) {
	switch {
	case n > 0:
		panic("positive")
	}
}

func fail(err error) {
	if err != nil {
		(panic(err))
	}
}

func cases(n int) int {
	switch n {
	case 0:
		fallthrough
	default:
		return n
	}
}

func shape(v any) int {
	switch v.(type) {
	case int:
		return 1
	default:
		return 0
	}
}

func drain(ch chan int) int {
	for {
		select {
		case v := <-ch:
			if v < 0 {
				break
			}
			return v
		}
	}
}

func retry(n int) int {
again:
	n--
	if n > 0 {
		goto again
	} else {
		return n
	}
}

func block() int {
	{
		return 1
		;
	}
}

func empty() {
	{
	}
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, ""), `loop sbr-delete 0: for { if <-ch == 0 { break } } -> ""
loop sbr-delete 1: if <-ch == 0 { break } -> ""
loop ror-true 0: <-ch == 0 -> "true"
loop ror-false 0: <-ch == 0 -> "false"
forever sbr-delete 0: <-ch -> ""
classify sbr-delete 0: switch { case n > 0: panic("positive") } -> ""
classify ror-boundary 0: n > 0 -> "n >= 0"
classify ror-false 0: n > 0 -> "false"
fail sbr-delete 0: if err != nil { (panic(err)) } -> ""
fail ror-true 0: err != nil -> "true"
fail ror-false 0: err != nil -> "false"
cases sbr-zero 0: return n -> "return 0"
shape sbr-zero 0: return 1 -> "return 0"
drain sbr-delete 0: select { case v := <-ch: if v < 0 { break } return v } -> ""
drain sbr-delete 1: if v < 0 { break } -> ""
drain ror-boundary 0: v < 0 -> "v <= 0"
drain ror-false 0: v < 0 -> "false"
drain sbr-zero 0: return v -> "return 0"
retry uoi-incdec 0: n-- -> "n++"
retry ror-boundary 0: n > 0 -> "n >= 0"
retry ror-false 0: n > 0 -> "false"
retry sbr-zero 0: return n -> "return 0"
block sbr-zero 0: return 1 -> "return 0"
empty sbr-delete 0: { } -> ""
`, "a final loop with a break, a switch without a default and a select with a break are deleted")
			formsCompile(t, dir, r)
		})

		t.Run("deletes the statements of a select statement's clauses", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"wait.go": `package fixture

import "log"

func wait(ch chan int, done chan struct{}) int {
	select {
	case v := <-ch:
		log.Print(v)
		return v
	case <-done:
		return 0
	}
}

func poll(ch chan int) {
	select {
	case ch <- 1:
		log.Print("sent")
	default:
	}
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, ""), `wait sbr-delete 0: log.Print(v) -> "" [logging]
wait sbr-zero 0: return v -> "return 0"
poll sbr-delete 0: select { case ch <- 1: log.Print("sent") default: } -> ""
poll sbr-delete 1: log.Print("sent") -> "" [logging]
`, "a clause's communication is no statement of its list")
			formsCompile(t, dir, r)
		})

		t.Run("compares operands of any type for equality", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"eq.go": `package fixture

func eq(i any, xs []int, p *int, f func()) bool {
	return i == 3 && xs != nil && p == nil && f != nil
}
`}), goref.Options{})
			expect.Equal(t, listing(r, "ror-"), `eq ror-true 0: i == 3 -> "true"
eq ror-false 0: i == 3 -> "false"
eq ror-true 1: xs != nil -> "true"
eq ror-false 1: xs != nil -> "false"
eq ror-true 2: p == nil -> "true"
eq ror-false 2: p == nil -> "false"
eq ror-true 3: f != nil -> "true"
eq ror-false 3: f != nil -> "false"
`, "every equality has its two mutants")
			expect.Empty(t, r.Expect.Skipped, "and no equality is skipped")
		})

		t.Run("suppresses the calls and the arguments of each rule family", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"rules.go": `package fixture

import (
	"log"
	"strings"
	"time"
)

func logs(n int) int {
	defer log.Println("done")
	log.Printf("n=%d", n+1)
	return n
}

func waits(t *time.Timer, d time.Duration) bool {
	time.Sleep(d * 2)
	return t.Reset(d)
}

func sizes(n int) ([]int, map[int]int, string) {
	s := make([]int, n-1, n*2)
	m := make(map[int]int, n+1)
	var b strings.Builder
	b.Grow(n * 3)
	return s, m, b.String()
}

func pipe(n int) chan int {
	return make(chan int, n+1)
}
`}), goref.Options{})
			assert.Equal(t, listing(r, ""), `logs sbr-delete 0: defer log.Println("done") -> "" [logging]
logs sbr-delete 1: log.Printf("n=%d", n+1) -> "" [logging]
logs aor 0: n+1 -> "n-1" [logging]
logs sbr-zero 0: return n -> "return 0"
waits sbr-delete 0: time.Sleep(d * 2) -> "" [timing]
waits aor 0: d * 2 -> "d / 2" [timing]
waits sbr-zero 0: return t.Reset(d) -> "return false"
waits uoi-not 0: t.Reset(d) -> "!t.Reset(d)" [timing]
sizes aor 0: n-1 -> "n+1"
sizes aor 1: n*2 -> "n/2" [capacity]
sizes aor 2: n+1 -> "n-1" [capacity]
sizes sbr-delete 0: b.Grow(n * 3) -> ""
sizes aor 3: n * 3 -> "n / 3" [capacity]
sizes sbr-zero 0: return s, m, b.String() -> "return nil, nil, \"\""
pipe sbr-zero 0: return make(chan int, n+1) -> "return nil"
pipe aor 0: n+1 -> "n-1"
`, "each family suppresses what its rules name")
		})

		t.Run("suppresses the argument of a method expression's call after the receiver", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"expr.go": `package fixture

import "bytes"

func grown(n int) *bytes.Buffer {
	var b bytes.Buffer
	(*bytes.Buffer).Grow(&b, n+1)
	return &b
}
`}), goref.Options{})
			assert.Equal(t, listing(r, ""), `grown sbr-delete 0: (*bytes.Buffer).Grow(&b, n+1) -> ""
grown aor 0: n+1 -> "n-1" [capacity]
grown sbr-zero 0: return &b -> "return nil"
`, "the call passes the receiver first, so the capacity is its second argument")
		})

		t.Run("suppresses the arguments of an API that a call instantiates explicitly", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"grow.go": `package fixture

import "slices"

type pair[K comparable, V any] struct {
	k K
	v V
}

func (p pair[K, V]) grow(s []V, n int) []V {
	return slices.Grow[[]V](s, n+1)
}

func widen(s []int, n int) []int {
	return slices.Grow[[]int, int](s, n*2)
}
`}), goref.Options{})
			expect.Equal(t, listing(r, ""), `pair.grow sbr-zero 0: return slices.Grow[[]V](s, n+1) -> "return nil"
pair.grow aor 0: n+1 -> "n-1" [capacity]
widen sbr-zero 0: return slices.Grow[[]int, int](s, n*2) -> "return nil"
widen aor 0: n*2 -> "n/2" [capacity]
`, "the capacity rule names the instantiated function")
		})

		leftOut := []struct {
			name, file, src, prefix, want string
		}{
			{
				name: "runs the deletions that leave out the only use of a variable, an import or a label",
				file: "unused.go", src: `package fixture

import (
	"context"
	"os"
	"sort"
	"time"
)

var log []string

func keep(s string, d time.Duration) (n int) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	os.Args = nil
	_ = os.Getpid()
	total := 0
	total += len(s)
	log = append(log, s)
	sort.Strings(log)
	f := func() { total++ }
	f()
	var last string
	last = s
	_ = last
	for _, x := range log {
		n += len(x)
	}
	var y string
	for _, y = range log {
	}
	println(y)
	<-ctx.Done()
	return total + n
}
`, prefix: spec.SBRDelete, want: `keep sbr-delete 0: defer cancel() -> ""
keep sbr-delete 1: os.Args = nil -> ""
keep sbr-delete 2: total += len(s) -> ""
keep sbr-delete 3: log = append(log, s) -> ""
keep sbr-delete 4: sort.Strings(log) -> ""
keep sbr-delete 5: f() -> ""
keep sbr-delete 6: last = s -> ""
keep sbr-delete 7: for _, x := range log { n += len(x) } -> ""
keep sbr-delete 8: n += len(x) -> ""
keep sbr-delete 9: for _, y = range log { } -> ""
keep sbr-delete 10: println(y) -> ""
keep sbr-delete 11: <-ctx.Done() -> ""
`,
			},
			{
				name: "runs the deletions that leave out the only read of a type switch's symbol",
				file: "switch.go", src: `package fixture

func size(v any) int {
	n := -1
	switch x := v.(type) {
	case string:
		n = len(x)
	case []int:
		n = len(x)
	}
	return n
}

func only(v any) int {
	n := 0
	switch x := v.(type) {
	case string:
		n = len(x)
	case int:
		n = 1
	}
	return n
}
`, prefix: "sbr-", want: `size sbr-delete 0: switch x := v.(type) { case string: n = len(x) case []int: n = len(x) } -> ""
size sbr-delete 1: n = len(x) -> ""
size sbr-delete 2: n = len(x) -> ""
size sbr-zero 0: return n -> "return 0"
only sbr-delete 0: switch x := v.(type) { case string: n = len(x) case int: n = 1 } -> ""
only sbr-delete 1: n = len(x) -> ""
only sbr-delete 2: n = 1 -> ""
only sbr-zero 0: return n -> "return 0"
`,
			},
			{
				name: "runs the mutants that leave out the only branch to a label",
				file: "labels.go", src: `package fixture

func first(xs []int) int {
	i := 0
scan:
	for ; i < len(xs); i++ {
		if xs[i] < 0 {
			break scan
		}
	}
	return i
}

func lit(xs []int) func() int {
	return func() int {
	done:
		for range xs {
			break done
		}
		return len(xs)
	}
}
`, prefix: "sbr-", want: `first sbr-delete 0: if xs[i] < 0 { break scan } -> ""
first sbr-zero 0: return i -> "return 0"
lit sbr-zero 0: return func() int { done: for range xs { break done } return len(xs) } -> "return nil"
lit sbr-zero 1: return len(xs) -> "return 0"
`,
			},
			{
				name: "runs the returns and the connectors that leave out the only use of a name",
				file: "names.go", src: `package fixture

import "strings"

func positive(m map[string]int, key string) bool {
	if v, ok := m[key]; ok && v > 0 {
		return true
	}
	return false
}

func size(s string) int {
	n := len(s)
	return n
}

func upper(s string) string {
	return strings.ToUpper(s)
}
`, prefix: "", want: `positive sbr-delete 0: if v, ok := m[key]; ok && v > 0 { return true } -> ""
positive lcr-left 0: ok && v > 0 -> "ok"
positive lcr-right 0: ok && v > 0 -> "v > 0"
positive lcr-false 0: ok && v > 0 -> "false"
positive ror-boundary 0: v > 0 -> "v >= 0"
positive ror-false 0: v > 0 -> "false"
positive sbr-zero 0: return true -> "return false"
size sbr-zero 0: return n -> "return 0"
upper sbr-zero 0: return strings.ToUpper(s) -> "return \"\""
`,
			},
		}
		for _, tt := range leftOut {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				dir := fixture(t, map[string]string{tt.file: tt.src})
				r := enumerate(t, dir, goref.Options{})
				expect.Equal(t, listing(r, tt.prefix), tt.want, "every mutant of the left-out use is made")
				expect.Empty(t, statics(r), "and runs")
				formsCompile(t, dir, r)
			})
		}

		t.Run("marks a division of an integer by a constant zero not viable", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"scale.go": `package fixture

const off = 0

func scale(x, y int, f float64) (int, int, float64) {
	a := x * 0
	b := x * off
	c := x * y
	x *= 0
	f *= 0
	return a + b + c, x, f * 0
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, spec.AOR), `scale aor 0: x * 0 -> "x / 0" not-viable
scale aor 1: x * off -> "x / off" not-viable
scale aor 2: x * y -> "x / y"
scale aor 3: x *= 0 -> "x /= 0" not-viable
scale aor 4: f *= 0 -> "f /= 0"
scale aor 5: a + b + c -> "a + b - c"
scale aor 6: a + b -> "a - b"
scale aor 7: f * 0 -> "f / 0"
`, "an integer division by a constant 0 is not viable")
			for _, m := range r.Expect.Mutants {
				err := compiles(dir, r.Forms[m.Key])
				expect.Equal(t, err != nil, m.NotViable, "the form fails to compile exactly when the mutant is not viable")
				if err != nil {
					expect.Contains(t, err.Error(), "division by zero", "and the type checker states the division")
				}
			}
		})

		t.Run("suppresses the marks of test helpers", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"helpers.go": `package fixture

import "testing"

// TB is a library's own interface with the method of testing.TB.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// marker has a method Helper of its own, which is no test helper's mark.
type marker interface {
	Helper(depth int)
}

type concrete struct{}

func (concrete) Helper() {}

func viaTB(tb testing.TB) {
	tb.Helper()
}

func viaT(t *testing.T) {
	t.Helper()
}

func viaF(f *testing.F) {
	f.Helper()
}

func viaOwn(tb TB) {
	tb.Helper()
}

func viaOther(m marker, c concrete) {
	m.Helper(1)
	c.Helper()
}
`}), goref.Options{})
			assert.Equal(t, listing(r, spec.SBRDelete), `viaTB sbr-delete 0: tb.Helper() -> "" [helper]
viaT sbr-delete 0: t.Helper() -> "" [helper]
viaF sbr-delete 0: f.Helper() -> "" [helper]
viaOwn sbr-delete 0: tb.Helper() -> "" [helper]
viaOther sbr-delete 0: m.Helper(1) -> ""
viaOther sbr-delete 1: c.Helper() -> ""
`, "a mark through an API or an interface's method is suppressed, and another method is not")
		})

		t.Run("suppresses the calls of a cancel function of a deadline", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"cancel.go": `package fixture

import (
	"context"
	"time"
)

var global context.CancelFunc

func stop(f *context.CancelFunc) { (*f)() }

func declared(parent context.Context) context.Context {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	return ctx
}

func assigned(parent context.Context, d time.Time) {
	var cancel context.CancelFunc
	parent, cancel = context.WithDeadline(parent, d)
	cancel()
	go cancel()
	defer func() {
		cancel()
	}()
	if cancel != nil {
		cancel()
	}
	_ = parent
}

func untyped(parent context.Context) {
	var f func()
	_, f = context.WithTimeout(parent, time.Second)
	f()
}

func cancelled(parent context.Context) {
	_, cancel := context.WithCancel(parent)
	defer cancel()
}

func mixed(parent context.Context, bound bool) {
	cancel := context.CancelFunc(func() {})
	if bound {
		_, cancel = context.WithTimeout(parent, time.Second)
	}
	cancel()
}

func addressed(parent context.Context) {
	_, cancel := context.WithTimeout(parent, time.Second)
	stop(&cancel)
	cancel()
}

func parameter(cancel context.CancelFunc) {
	cancel()
}

func named() (ctx context.Context, cancel context.CancelFunc) {
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	cancel()
	return
}

func shared(parent context.Context) {
	_, global = context.WithTimeout(parent, time.Second)
	global()
}
`}), goref.Options{})
			golden.Match(t, "cancel.txt", []byte(listing(r, spec.SBRDelete)), golden.ShouldUpdate())
		})

		t.Run("suppresses the annotated kinds of one line", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"notes.go": `package fixture

func best(xs []int) int {
	top := 0
	for _, v := range xs {
		if v > top { //dokimi:mutate-skip ror-boundary: an equal value leaves top unchanged
			top = v
		}
	}
	return top
}

func clamp(x int) int {
	//dokimi:mutate-skip ror: the caller checks the bound
	if x > 10 {
		return 10
	}
	return x
}
`}), goref.Options{})
			var b strings.Builder
			for _, m := range r.Expect.Mutants {
				if m.Reason != "" {
					fmt.Fprintf(&b, "%s %s %d: %s\n", m.Scope, m.Kind, m.Nth, m.Reason)
				}
			}
			expect.Equal(t, b.String(), `best ror-boundary 0: an equal value leaves top unchanged
clamp ror-boundary 0: the caller checks the bound
clamp ror-false 0: the caller checks the bound
`, "an annotation suppresses its kinds on its line")
			expect.Empty(t, r.Expect.Errors, "and no annotation fails")
		})

		annotations := []struct {
			name, line string
			want       []string
		}{
			{
				name: "reports an annotation without a reason",
				line: "if x > 1 { //dokimi:mutate-skip ror-boundary",
				want: []string{spec.ErrorWithoutReason},
			},
			{
				name: "reports an annotation with an empty reason",
				line: "if x > 1 { //dokimi:mutate-skip ror-boundary:  ",
				want: []string{spec.ErrorWithoutReason},
			},
			{
				name: "reports an annotation that suppresses no mutant",
				line: "if x > 1 { //dokimi:mutate-skip aor: no arithmetic here",
				want: []string{spec.ErrorStale},
			},
			{
				name: "reports an annotation of an unknown kind as stale",
				line: "if x > 1 { //dokimi:mutate-skip ror-bound: a misspelt kind",
				want: []string{spec.ErrorStale},
			},
			{
				name: "reports nothing for another directive",
				line: "if x > 1 { //dokimi:mutate-skipped ror-boundary: another directive",
				want: []string{},
			},
			{
				name: "reports nothing for an annotation of two kinds",
				line: "if x > 1 { //dokimi:mutate-skip ror-boundary, sbr: two kinds",
				want: []string{},
			},
			{
				name: "reports nothing for an annotation of every kind",
				line: "if x > 1 { //dokimi:mutate-skip all: every kind",
				want: []string{},
			},
			{
				name: "reports nothing for an annotation on the line before its site",
				line: "//dokimi:mutate-skip ror-boundary: on the line below\n\tif x > 1 {",
				want: []string{},
			},
			{
				name: "reports an annotation two lines before its site as stale",
				line: "//dokimi:mutate-skip ror-boundary: two lines below\n\n\tif x > 1 {",
				want: []string{spec.ErrorStale},
			},
		}
		for _, tt := range annotations {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				src := "package fixture\n\nfunc f(x int) int {\n\t" + tt.line + "\n\t\treturn 1\n\t}\n\treturn x\n}\n"
				r := enumerate(t, fixture(t, map[string]string{"f.go": src}), goref.Options{})
				assert.Equal(t, r.Expect.Errors, tt.want, "the enumeration states the annotation's run errors")
			})
		}

		t.Run("reads only the files of the build", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{
				"a.go":       "package fixture\n\nfunc used(x int) int { return x + 1 }\n",
				"gen.go":     "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc generated(x int) int { return x * 2 }\n",
				"ignored.go": "//go:build ignore\n\npackage other\n\nfunc ignored(x int) int { return x - 3 }\n",
				"a_test.go":  "package fixture\n\nfunc helper(x int) int { return x / 4 }\n",
				"notes.txt":  "no Go file",
			}), goref.Options{})
			assert.Equal(t, listing(r, ""), `used sbr-zero 0: return x + 1 -> "return 0"
used aor 0: x + 1 -> "x - 1"
`, "the mutants are in the build's own source alone")
		})

		t.Run("reads a generated file with the include directive as a target", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{
				"included.go": "// Code generated by kanon. DO NOT EDIT.\n//dokimi:mutate-include\n\npackage fixture\n\n" +
					"func included(x int) int { return x * 2 }\n",
				"late.go": "// Code generated by kanon. DO NOT EDIT.\n\npackage fixture\n\n//dokimi:mutate-include\n" +
					"func late(x int) int { return x * 3 }\n",
				"exempt.go": "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc exempt(x int) int { return x * 4 }\n",
			}), goref.Options{})
			expect.Equal(t, listing(r, spec.AOR), "included aor 0: x * 2 -> \"x / 2\"\n",
				"only the file with the directive before its package clause is a target")
			expect.Equal(t, r.Expect.Generated, []spec.Generated{
				{File: "exempt.go", Mutants: 2},
				{File: "late.go", Mutants: 2},
			}, "the files without it are left out, each with the two mutants of its function")
		})

		t.Run("reads every generated file as a target under IncludeGenerated", func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"a.go":   "package fixture\n\nfunc used(x int) int { return x + 1 }\n",
				"gen.go": "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc generated(x int) int { return x * 2 }\n",
			}
			r := enumerate(t, fixture(t, files), goref.Options{IncludeGenerated: true})
			expect.Equal(t, listing(r, ""), `used sbr-zero 0: return x + 1 -> "return 0"
used aor 0: x + 1 -> "x - 1"
generated sbr-zero 0: return x * 2 -> "return 0"
generated aor 0: x * 2 -> "x / 2"
`, "the generated file's mutants are the target's own")
			expect.Equal(t, r.Expect.Generated, []spec.Generated{{File: "gen.go", Mutants: 2, Included: true}},
				"and the file is listed as included")
		})

		t.Run("leaves a return of zero values alone", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"zero.go": `package fixture

type point struct{ x, y int }

func structs(ok bool) (point, bool) {
	if ok {
		return point{x: 0}, false
	}
	return point{0, 0}, false
}

func arrays() ([2]point, *int) {
	return [2]point{{}, {y: 0}}, nil
}

func news[T any]() (T, string) {
	return *new(T), ""
}

func values() ([]int, map[int]int, point) {
	return []int{}, map[int]int{}, point{x: 1}
}

func forward() (point, bool) {
	return structs(true)
}
`}), goref.Options{})
			assert.Equal(t, listing(r, spec.SBRZero),
				"values sbr-zero 0: return []int{}, map[int]int{}, point{x: 1} -> \"return nil, nil, point{}\"\n"+
					"forward sbr-zero 0: return structs(true) -> \"return point{}, false\"\n",
				"only a return of a value that is no zero value has the mutant, such as the results of a call")
		})

		t.Run("returns nil for a value in an interface that is not nil", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"boxed.go": `package fixture

type pair struct {
	label any
	n     int
}

func boxed() (any, error, any) {
	return 0, nil, *new(error)
}

func labelled() pair {
	return pair{label: "", n: 0}
}

func untouched() (any, pair) {
	return nil, pair{}
}
`}), goref.Options{})
			assert.Equal(t, listing(r, spec.SBRZero), `boxed sbr-zero 0: return 0, nil, *new(error) -> "return nil, nil, nil"
labelled sbr-zero 0: return pair{label: "", n: 0} -> "return pair{}"
`, "a 0 or an empty string in an interface is not nil")
		})

		t.Run("leaves a return of a variable that no use writes alone", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"unwritten.go": `package fixture

import "strconv"

type point struct{ x, y int }

func (p *point) shift() { p.x++ }

func (p point) sum() int { return p.x + p.y }

func index[T any](s []T, i int) (T, bool) {
	if i < len(s) {
		return s[i], true
	}
	var zero T
	return zero, false
}

func values() (point, error, [2]int) {
	var (
		p   point
		err error
		a   [2]int
	)
	_, _, _ = p.sum(), len(a), a[0]
	return p, err, a
}

func boxed() any {
	var n int
	return n
}

var global int

func pkg() int { return global }

func assign() int { var n int; n = 1; return n }

func compound() int { var n int; n += 1; return n }

func paren() int { var n int; (n) = 1; return n }

func field() point { var p point; p.x = 1; return p }

func element() [2]int { var a [2]int; a[0] = 1; return a }

func declare(s string) (int, error) { var n int; n, err := strconv.Atoi(s); return n, err }

func increment() int { var n int; n++; return n }

func ranged(xs []int) int { var x int; for _, x = range xs {}; return x }

func address() int { var n int; p := &n; *p = 1; return n }

func slice() [2]byte { var a [2]byte; copy(a[:], "ab"); return a }

func method() point { var p point; p.shift(); return p }

func value() point { var p point; f := p.shift; f(); return p }

func pointer() *int { var p *int; *p = 1; return p }

func closure() int { var n int; func() { n = 1 }(); return n }

func later(xs []int) int {
	var x int
	for i, v := range xs {
		if i > 0 {
			return x
		}
		x = v
	}
	return 0
}

func fresh() *point { return &point{} }
`}), goref.Options{})
			assert.Equal(t, listing(r, spec.SBRZero), `point.sum sbr-zero 0: return p.x + p.y -> "return 0"
index sbr-zero 0: return s[i], true -> "return *new(T), false"
boxed sbr-zero 0: return n -> "return nil"
pkg sbr-zero 0: return global -> "return 0"
assign sbr-zero 0: return n -> "return 0"
compound sbr-zero 0: return n -> "return 0"
paren sbr-zero 0: return n -> "return 0"
field sbr-zero 0: return p -> "return point{}"
element sbr-zero 0: return a -> "return [2]int{}"
declare sbr-zero 0: return n, err -> "return 0, nil"
increment sbr-zero 0: return n -> "return 0"
ranged sbr-zero 0: return x -> "return 0"
address sbr-zero 0: return n -> "return 0"
slice sbr-zero 0: return a -> "return [2]byte{}"
method sbr-zero 0: return p -> "return point{}"
value sbr-zero 0: return p -> "return point{}"
pointer sbr-zero 0: return p -> "return nil"
closure sbr-zero 0: return n -> "return 0"
later sbr-zero 0: return x -> "return 0"
fresh sbr-zero 0: return &point{} -> "return nil"
`, "a return of a local variable that a use writes is a site, and one of a variable that no use writes is not")
		})

		t.Run("returns zero for the value that new of an expression points to", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{
				goMod:    "module fixture\n\ngo 1.26\n",
				"new.go": "package fixture\n\nfunc pointed(x int) (int, int) {\n\treturn *new(x), *new(int)\n}\n",
			}), goref.Options{})
			assert.Equal(t, listing(r, spec.SBRZero), "pointed sbr-zero 0: return *new(x), *new(int) -> \"return 0, 0\"\n",
				"*new(x) of an expression is the value of x, which is no zero value")
		})

		t.Run("writes zero values as Go code writes them", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"spell.go": `package fixture

import (
	"strings"
	"time"
	"unsafe"
)

type name string

type pair struct{ a, b int }

func all(n int, f float64, c complex128, s string, ok bool, d time.Duration, nm name) (int, float64, complex128, string, bool, time.Duration, name) {
	return n, f, c, s, ok, d, nm
}

func refs(p *int, xs []int, m map[int]int, ch chan int, fn func(), e error, u unsafe.Pointer) (*int, []int, map[int]int, chan int, func(), error, unsafe.Pointer) {
	return p, xs, m, ch, fn, e, u
}

func composites(p pair, a [3]int, s struct{ x int }) (pair, ([3]int), struct{ x int }) {
	return p, a, s
}

func generic[T any](v T) (T, []T) {
	return v, nil
}

func deref(p *pair) (pair, int) {
	return *p, 0
}

func reader() strings.Reader {
	return *strings.NewReader("")
}

func origin() pair {
	return pair{a: 1}
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, spec.SBRZero), `all sbr-zero 0: return n, f, c, s, ok, d, nm -> "return 0, 0, 0, \"\", false, 0, \"\""
refs sbr-zero 0: return p, xs, m, ch, fn, e, u -> "return nil, nil, nil, nil, nil, nil, nil"
composites sbr-zero 0: return p, a, s -> "return pair{}, [3]int{}, struct{ x int }{}"
generic sbr-zero 0: return v, nil -> "return *new(T), nil"
deref sbr-zero 0: return *p, 0 -> "return pair{}, 0"
reader sbr-zero 0: return *strings.NewReader("") -> "return strings.Reader{}"
origin sbr-zero 0: return pair{a: 1} -> "return pair{}"
`, "each zero value reads as Go code")
			formsCompile(t, dir, r)
		})

		t.Run("names the variables of a zero return apart from every identifier of the target", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"next.go": `package fixture

var _mutateZero0, _mutate1Step = 1, 2

func next(n int) int {
	return n + _mutateZero0*_mutate1Step
}
`})
			r := enumerate(t, dir, goref.Options{})
			var forms []string
			for _, m := range r.Expect.Mutants {
				if m.Kind == spec.SBRZero {
					forms = append(forms, r.Forms[m.Key].Text)
				}
			}
			assert.Equal(t, forms, []string{
				"(_mutate2Zero0 int) {\n\tif (0 == 0) {\nreturn _mutate2Zero0\n}\nreturn n + _mutateZero0*_mutate1Step",
			}, "the variable's name starts with the first prefix that no identifier of the target starts with")
			formsCompile(t, dir, r)
		})

		t.Run("marks the mutants outside the selection", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"clamp.go": clamp}), goref.Options{Lines: []string{"clamp.go:7-9"}})
			assert.Equal(t, statics(r), `clamp sbr-delete 0: not-selected
clamp ror-boundary 0: not-selected
clamp ror-false 0: not-selected
clamp sbr-zero 0: not-selected
clamp sbr-zero 2: not-selected
`, "every mutant outside the lines is not-selected")
		})

		t.Run("ranks not-selected before suppressed before not-viable", func(t *testing.T) {
			t.Parallel()
			r := enumerate(t, fixture(t, map[string]string{"weight.go": `package fixture

const weight = 0

func outside(x int) int {
	y := x * weight //dokimi:mutate-skip aor: a test of the ranking
	return y + 1
}

func inside(x int) int {
	y := x * weight //dokimi:mutate-skip aor: a test of the ranking
	return y + 1
}

func plain(x int) int {
	y := x * weight
	return y + 1
}
`}), goref.Options{Lines: []string{"weight.go:10-18"}})
			expect.Equal(t, listing(r, spec.AOR), `outside aor 0: x * weight -> "x / weight"
outside aor 1: y + 1 -> "y - 1"
inside aor 0: x * weight -> "x / weight" (a test of the ranking) not-viable
inside aor 1: y + 1 -> "y - 1"
plain aor 0: x * weight -> "x / weight" not-viable
plain aor 1: y + 1 -> "y - 1"
`, "a mutant outside the selection states no rule, reason or rejection")
			expect.Equal(t, statics(r), `outside aor 0: not-selected
outside sbr-zero 0: not-selected
outside aor 1: not-selected
inside aor 0: suppressed
plain aor 0: not-viable
`, "and the first verdict that applies is the mutant's")
			expect.Empty(t, r.Expect.Errors, "an annotation outside the selection still applies")
		})

		t.Run("suppresses a compound statement whose bodies the families suppress", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"quiet.go": `package fixture

import (
	"fmt"
	"io"
	"log"
	"time"
)

func warn(err error) {
	if err != nil {
		log.Printf("warning: %v", err)
	}
}

func fail(err error) error {
	if err != nil {
		log.Printf("failure: %v", err)
		return fmt.Errorf("fail: %w", err)
	}
	return nil
}

func each(xs []int) {
	for _, x := range xs {
		log.Printf("value: %d", x)
	}
}

func drain(ch chan int) {
	for x := range ch {
		log.Printf("value: %d", x)
	}
}

func flush(w io.Writer, buf []byte) {
	if _, err := w.Write(buf); err != nil {
		log.Printf("flush: %v", err)
	}
}

func probe(w io.Writer, ready func() bool) {
	if _, err := w.Write(nil); err != nil && ready() {
		log.Print("not ready")
	}
}

func level(check func() error) {
	switch err := check(); {
	case err == nil:
		log.Print("ok")
	default:
		log.Printf("failed: %v", err)
	}
}

func sign(n int) {
	switch {
	case n > 0:
		log.Print("positive")
	case n < 0:
		log.Print("negative")
	default:
		log.Print("zero")
	}
}

func report(ok bool, d time.Duration) {
	if ok {
		log.Print("ok")
	} else if d > 0 {
		time.Sleep(d)
	}
}

func count(xs []int) (n int) {
	for i := 0; i < len(xs); i++ {
		log.Print(xs[i])
	}
	for n = 0; n < len(xs); n++ {
		log.Print(xs[n])
	}
	return n
}

func nothing(ok bool) {
	if ok {
	}
}
`})
			r := enumerate(t, dir, goref.Options{})
			golden.Match(t, "quiet.txt", []byte(listing(r, "")), golden.ShouldUpdate())
			formsCompile(t, dir, r)
		})

		t.Run("suppresses a statement whose header has no effect", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"free.go": `package fixture

import "log"

func shape(v any) {
	switch v.(type) {
	case int:
		log.Print("int")
	default:
		log.Print("other")
	}
}

func skip(ok bool) {
	if ok {
		log.Print("ok")
		;
	}
}

func each(n int) {
	for _, f := range []func(){func() {}} {
		log.Print(f != nil)
	}
	if int64(n) > 0 {
		log.Print("positive")
	}
}

func pairs(n int) {
	for i := 0; i < n; i += 2 {
		log.Print(i)
	}
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, ""), `shape sbr-delete 0: switch v.(type) { case int: log.Print("int") default: log.Print("other") } -> "" [logging]
shape sbr-delete 1: log.Print("int") -> "" [logging]
shape sbr-delete 2: log.Print("other") -> "" [logging]
skip sbr-delete 0: if ok { log.Print("ok") ; } -> "" [logging]
skip uoi-not 0: ok -> "!ok" [logging]
skip sbr-delete 1: log.Print("ok") -> "" [logging]
each sbr-delete 0: for _, f := range []func(){func() {}} { log.Print(f != nil) } -> "" [logging]
each sbr-delete 1: log.Print(f != nil) -> "" [logging]
each ror-true 0: f != nil -> "true" [logging]
each ror-false 0: f != nil -> "false" [logging]
each sbr-delete 2: if int64(n) > 0 { log.Print("positive") } -> "" [logging]
each ror-boundary 0: int64(n) > 0 -> "int64(n) >= 0" [logging]
each ror-false 1: int64(n) > 0 -> "false" [logging]
each sbr-delete 3: log.Print("positive") -> "" [logging]
pairs sbr-delete 0: for i := 0; i < n; i += 2 { log.Print(i) } -> "" [logging]
pairs ror-boundary 0: i < n -> "i <= n" [logging]
pairs ror-false 0: i < n -> "false" [logging]
pairs aor 0: i += 2 -> "i -= 2" [logging]
pairs sbr-delete 1: log.Print(i) -> "" [logging]
`, "a type switch, an empty statement, a function literal, a conversion and a post assignment of the loop's variable have no effect")
			formsCompile(t, dir, r)
		})

		t.Run("keeps the deletion of a statement whose header has an effect", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"effects.go": `package fixture

import "log"

func pick(next func() int, x int) {
	switch n := next(); x + n {
	case 1:
		log.Print("one")
	default:
		log.Print("other")
	}
}

func choose(ready func() bool, n int) {
	switch {
	case ready():
		log.Print("ready")
	case n > 0:
		log.Print("positive")
	}
}

func tally(n int, total *int) {
	switch n {
	case 1:
		log.Print("one")
	case 2:
		*total += n
	}
}

func probe(next func() any) {
	switch next().(type) {
	case int:
		log.Print("int")
	}
}

func pairs(n int, advance func(*int)) {
	j := 0
	for i := 0; i < n; i, j = i+1, j+1 {
		log.Print(i)
	}
	for i := 0; i < n; advance(&i) {
		log.Print(i)
	}
}
`})
			r := enumerate(t, dir, goref.Options{})
			expect.Equal(t, listing(r, ""), `pick sbr-delete 0: switch n := next(); x + n { case 1: log.Print("one") default: log.Print("other") } -> ""
pick aor 0: x + n -> "x - n" [logging]
pick sbr-delete 1: log.Print("one") -> "" [logging]
pick sbr-delete 2: log.Print("other") -> "" [logging]
choose sbr-delete 0: switch { case ready(): log.Print("ready") case n > 0: log.Print("positive") } -> ""
choose uoi-not 0: ready() -> "!ready()"
choose sbr-delete 1: log.Print("ready") -> "" [logging]
choose ror-boundary 0: n > 0 -> "n >= 0" [logging]
choose ror-false 0: n > 0 -> "false" [logging]
choose sbr-delete 2: log.Print("positive") -> "" [logging]
tally sbr-delete 0: switch n { case 1: log.Print("one") case 2: *total += n } -> ""
tally sbr-delete 1: log.Print("one") -> "" [logging]
tally sbr-delete 2: *total += n -> ""
probe sbr-delete 0: switch next().(type) { case int: log.Print("int") } -> ""
probe sbr-delete 1: log.Print("int") -> "" [logging]
pairs sbr-delete 0: for i := 0; i < n; i, j = i+1, j+1 { log.Print(i) } -> ""
pairs ror-boundary 0: i < n -> "i <= n"
pairs ror-false 0: i < n -> "false"
pairs aor 0: i+1 -> "i-1"
pairs aor 1: j+1 -> "j-1"
pairs sbr-delete 1: log.Print(i) -> "" [logging]
pairs sbr-delete 2: for i := 0; i < n; advance(&i) { log.Print(i) } -> ""
pairs ror-boundary 1: i < n -> "i <= n"
pairs ror-false 1: i < n -> "false"
pairs sbr-delete 3: log.Print(i) -> "" [logging]
`, "the parts of a switch's header without an effect are suppressed, and the statement's deletion is kept")
			formsCompile(t, dir, r)
		})

		t.Run("returns the expectations of every case of the corpus", func(t *testing.T) {
			t.Parallel()
			s, err := spec.Load(repository)
			assert.NoError(t, err, "the repository's definition and corpus load")
			for name, c := range s.Cases {
				want, ok := s.Expects[name][goref.Language]
				if !ok {
					continue
				}
				r, err := goref.Enumerate(s.Fixture(name, goref.Language), s.Catalogue, s.Overlays[goref.Language], goref.Options{
					Lines:            want.Lines,
					IncludeGenerated: c.IncludeGenerated,
				})
				assert.NoError(t, err, "Enumerate reads the fixture of "+name)
				got := r.Expect
				got.Suite = want.Suite
				if len(got.Mutants) == len(want.Mutants) {
					// The tests and the covering tests are written by hand.
					for i := range got.Mutants {
						got.Mutants[i].Tests, got.Mutants[i].CoveredBy = want.Mutants[i].Tests, want.Mutants[i].CoveredBy
					}
				}
				expect.Equal(t, got, want, "the enumeration of "+name+" is its expect.json", assert.EquateEmpty())
			}
		})

		t.Run("writes forms whose tests fail exactly where the case shadowed states a kill", func(t *testing.T) {
			t.Parallel()
			s, err := spec.Load(repository)
			assert.NoError(t, err, "the repository's definition and corpus load")
			dir := s.Fixture(shadowedCase, goref.Language)
			r, err := goref.Enumerate(dir, s.Catalogue, s.Overlays[goref.Language], goref.Options{})
			assert.NoError(t, err, "Enumerate reads the fixture")
			verdicts := map[string]string{}
			for _, m := range s.Cases[shadowedCase].Mutants {
				verdicts[m.ID()] = m.Verdict
			}
			env := ordinary(s.Protocol)
			for _, m := range r.Expect.Mutants {
				t.Run(m.ID(), func(t *testing.T) {
					t.Parallel()
					out := testForm(t, dir, r.Forms[m.Key], env)
					assert.NotContains(t, out, buildFailed, "the ordinary build of the mutant compiles")
					expect.Equal(t, strings.Contains(out, testFailed), verdicts[m.ID()] == spec.Killed,
						"a test fails exactly where the case states killed: "+out)
				})
			}
		})

		t.Run("returns the same result on every call", func(t *testing.T) {
			t.Parallel()
			cat, ov := definition(t)
			dir := fixture(t, map[string]string{"kinds.go": kinds, "steps.go": steps, "clamp.go": clamp})
			assert.Deterministic(t, func(dir string) (*goref.Result, error) {
				return goref.Enumerate(dir, cat, ov, goref.Options{})
			}, dir, "Enumerate orders every list that it builds from maps")
		})

		t.Run("reads the language version of the module's go line", func(t *testing.T) {
			t.Parallel()
			src := "package fixture\n\nfunc each(n int) int {\n\tt := 0\n\tfor i := range n {\n\t\tt += i\n\t}\n\treturn t\n}\n"
			cat, ov := definition(t)
			_, err := goref.Enumerate(fixture(t, map[string]string{goMod: module, "each.go": src}), cat, ov, goref.Options{})
			assert.HasError(t, err, "a range over an integer needs go 1.22")
			for name, mod := range map[string]string{"go 1.22": "module fixture\n\ngo 1.22\n", "no go line": "module fixture\n"} {
				_, err := goref.Enumerate(fixture(t, map[string]string{goMod: mod, "each.go": src}), cat, ov, goref.Options{})
				expect.NoError(t, err, "the range compiles with "+name)
			}
		})

		t.Run("reads a package without a module file at the toolchain's language version", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "f.go"), "package fixture\n\nfunc f(n int) int { return n + 1 }\n")
			r := enumerate(t, dir, goref.Options{})
			assert.Length(t, r.Expect.Mutants, 2, "the function's two mutants are made")
		})

		failures := []struct {
			name  string
			files map[string]string
			want  string
		}{
			{
				name:  "returns an error for a file that does not parse",
				files: map[string]string{"f.go": "package fixture\n\nfunc {\n"},
				want:  "expected",
			},
			{
				name:  "returns an error for a directory without a file of the build",
				files: map[string]string{"f_test.go": "package fixture\n"},
				want:  "no Go file that the build compiles",
			},
			{
				name:  "returns an error for a package that does not type-check",
				files: map[string]string{"f.go": "package fixture\n\nfunc f() int { return missing }\n"},
				want:  "undefined: missing",
			},
			{
				name:  "returns an error for a file of an invalid build constraint",
				files: map[string]string{"f.go": "//go:build (\n\npackage fixture\n"},
				want:  "go:build",
			},
		}
		for _, tt := range failures {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				cat, ov := definition(t)
				_, err := goref.Enumerate(fixture(t, tt.files), cat, ov, goref.Options{})
				assert.HasError(t, err, "Enumerate refuses the fixture")
				assert.Contains(t, err.Error(), tt.want, "and states why")
			})
		}

		t.Run("returns an error for a file that imports C", func(t *testing.T) {
			t.Parallel()
			if !build.Default.CgoEnabled {
				t.Skip("without cgo, the build compiles no file that imports C")
			}
			cat, ov := definition(t)
			_, err := goref.Enumerate(fixture(t, map[string]string{"f.go": "package fixture\n\nimport \"C\"\n"}), cat, ov, goref.Options{})
			assert.HasError(t, err, "Enumerate refuses a cgo file")
			assert.Contains(t, err.Error(), "imports C", "and states why")
		})

		t.Run("returns an error for a directory that does not exist", func(t *testing.T) {
			t.Parallel()
			cat, ov := definition(t)
			_, err := goref.Enumerate(filepath.Join(t.TempDir(), "missing"), cat, ov, goref.Options{})
			assert.ErrorIs(t, err, fs.ErrNotExist, "Enumerate reports the missing directory")
		})

		t.Run("returns an error for a file that does not read", func(t *testing.T) {
			t.Parallel()
			dir := fixture(t, map[string]string{"f.go": "package fixture\n"})
			assert.NoError(t, os.Chmod(filepath.Join(dir, "f.go"), noAccessMode), "the file loses its permissions")
			cat, ov := definition(t)
			_, err := goref.Enumerate(dir, cat, ov, goref.Options{})
			assert.ErrorIs(t, err, fs.ErrPermission, "Enumerate reports the file that it cannot read")
		})
	})

	t.Run("Key", func(t *testing.T) {
		t.Parallel()

		// Each digest was computed with printf and sha256sum, outside Go.
		tests := []struct {
			name string
			give keyFields
			want string
		}{
			{
				name: "returns the digest of the fields of a first occurrence",
				give: keyFields{Prefix: "dokimi-mutate-key/1", File: "between.go", Scope: "between", Kind: spec.RORBoundary, Tokens: "x < hi"},
				want: "ba74901e75daf19d",
			},
			{
				name: "returns the digest of the fields of a second occurrence",
				give: keyFields{Prefix: "dokimi-mutate-key/1", File: "steps.go", Scope: "steps", Kind: spec.RORFalse, Tokens: "n > 0", Occurrence: 1},
				want: "681ab23c877298d7",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				g := tt.give
				assert.Equal(t, goref.Key(g.Prefix, g.File, g.Scope, g.Kind, g.Tokens, g.Occurrence), tt.want,
					"Key is the start of the SHA-256 digest of the fields separated by NUL")
			})
		}

		t.Run("returns 16 hexadecimal digits for any fields", func(t *testing.T) {
			t.Parallel()
			prop.Matches(t, func(g keyFields) string {
				return goref.Key(g.Prefix, g.File, g.Scope, g.Kind, g.Tokens, g.Occurrence)
			}, keyPattern, "a key is 16 hexadecimal digits")
		})

		t.Run("returns two keys for two fields that join to one text", func(t *testing.T) {
			t.Parallel()
			assert.NotEqual(t, goref.Key("p", "ab", "c", spec.AOR, "x", 0), goref.Key("p", "a", "bc", spec.AOR, "x", 0),
				"a separator between the fields keeps their boundaries")
		})
	})

	t.Run("Tokens", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name, give, want string
		}{
			{name: "leaves out a comment", give: "a /* note */ +\n\tb", want: "a + b"},
			{name: "leaves out a line comment and the inserted semicolons", give: "if x {\n\ty = 1 // set\n}", want: "if x { y = 1 }"},
			{name: "keeps the semicolons that the source writes", give: "for i := 0; i < n; i++ {\n}", want: "for i := 0 ; i < n ; i ++ { }"},
			{name: "keeps the text of a string literal", give: "s == \"a  b\"", want: "s == \"a  b\""},
			{name: "separates an operator from its operands", give: "x<-y", want: "x <- y"},
			{name: "writes a keyword and a selector token by token", give: "return *new(int), errors.New", want: "return * new ( int ) , errors . New"},
		}
		for _, tt := range tests {
			t.Run("returns the tokens of a site that "+tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, goref.Tokens([]byte(tt.give)), tt.want, "Tokens joins the scanner's tokens by single spaces")
			})
		}

		t.Run("returns the same tokens whatever separates them", func(t *testing.T) {
			t.Parallel()
			token := prop.SampledFrom("a", "b1", "_x", "42", "0x1F", "3.5", `"s  t"`, "'c'", "+", "-", "*", "/", "%",
				"<<", "&^", "&&", "||", "<-", "++", "==", "!=", "<=", ":=", "...", "(", ")", "[", "]", "{", "}", ",", ";", ".")
			separator := prop.SampledFrom(" ", "\t", "  ", "\n", " /* c */ ", " // c\n")
			prop.ForAll(t, "whitespace and comments between tokens do not change a key's tokens", func(c *prop.Case) {
				tokens := c.Draw(prop.List(token, prop.MinSize(1), prop.MaxSize(12)), "tokens")
				var src strings.Builder
				for i, tok := range tokens {
					if i > 0 {
						src.WriteString(c.Draw(separator, "separator"))
					}
					src.WriteString(tok)
				}
				assert.Equal(c, goref.Tokens([]byte(src.String())), strings.Join(tokens, " "), "Tokens returns the tokens alone")
			})
		})
	})

	t.Run("Cut", func(t *testing.T) {
		t.Parallel()

		a, b := strings.Repeat("a", 150), strings.Repeat("b", 200)
		tests := []struct {
			name                          string
			original, replacement         string
			wantOriginal, wantReplacement string
		}{
			{
				name:     "returns both fields with each run of whitespace as one space",
				original: "if x {\n\t\ty = 1\n\t}", replacement: "x < 1\t\t&& y",
				wantOriginal: "if x { y = 1 }", wantReplacement: "x < 1 && y",
			},
			{
				name:     "returns two fields of 120 characters as they are",
				original: strings.Repeat("é", 120), replacement: strings.Repeat("ü", 120),
				wantOriginal: strings.Repeat("é", 120), wantReplacement: strings.Repeat("ü", 120),
			},
			{
				name:     "returns the first 119 characters of a deleted site of 121",
				original: strings.Repeat("é", 121), replacement: "",
				wantOriginal: strings.Repeat("é", 119) + "…", wantReplacement: "",
			},
			{
				name:     "returns the start of two fields that differ in their first 40 characters",
				original: "x < " + b, replacement: "x <= " + b,
				wantOriginal: ("x < " + b)[:119] + "…", wantReplacement: ("x <= " + b)[:119] + "…",
			},
			{
				name:     "returns the 40 characters before a difference at the end",
				original: a + " && ok", replacement: a,
				wantOriginal: "…" + a[:40] + " && ok", wantReplacement: "…" + a[:40],
			},
			{
				name:     "returns one window around a difference in the middle",
				original: a + " + " + b, replacement: a + " - " + b,
				wantOriginal: "…" + (a + " + " + b)[111:229] + "…", wantReplacement: "…" + (a + " - " + b)[111:229] + "…",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				original, replacement := goref.Cut(tt.original, tt.replacement)
				expect.Equal(t, original, tt.wantOriginal, "the original is cut")
				expect.Equal(t, replacement, tt.wantReplacement, "and the replacement keeps the same window")
			})
		}

		text := prop.String(prop.Alphabet("ab \t\n"), prop.MaxSize(300))

		t.Run("returns fields of at most 120 characters", func(t *testing.T) {
			t.Parallel()
			prop.ForAll(t, "a cut field fits the record", func(c *prop.Case) {
				original, replacement := goref.Cut(c.Draw(text, "original"), c.Draw(text, "replacement"))
				assert.InRange(c, utf8.RuneCountInString(original), 0, 120, "the original fits")
				assert.InRange(c, utf8.RuneCountInString(replacement), 0, 120, "and so does the replacement")
			})
		})

		t.Run("returns two different fields for two different sites", func(t *testing.T) {
			t.Parallel()
			prop.ForAll(t, "a cut never hides the change of a mutant", func(c *prop.Case) {
				o, r := c.Draw(text, "original"), c.Draw(text, "replacement")
				c.Assume(strings.Join(strings.Fields(o), " ") != strings.Join(strings.Fields(r), " "))
				original, replacement := goref.Cut(o, r)
				assert.NotEqual(c, original, replacement, "the two fields differ where the sites differ")
			})
		})
	})
}

// definition returns the repository's catalogue and Go overlay.
func definition(t *testing.T) (spec.Catalogue, spec.Overlay) {
	t.Helper()
	s, err := spec.LoadDefinition(repository)
	assert.NoError(t, err, "the repository's definition loads")
	return s.Catalogue, s.Overlays[goref.Language]
}

// fixture writes files into a new directory, with the module file module
// unless files has one, and returns the directory.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files[goMod]; !ok {
		writeFile(t, filepath.Join(dir, goMod), module)
	}
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	return dir
}

// writeFile writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	assert.NoError(t, os.WriteFile(path, []byte(content), fileMode), "the file is written")
}

// enumerate returns the enumeration of the fixture in dir under the
// repository's definition and opts.
func enumerate(t *testing.T, dir string, opts goref.Options) *goref.Result {
	t.Helper()
	cat, ov := definition(t)
	r, err := goref.Enumerate(dir, cat, ov, opts)
	assert.NoError(t, err, "Enumerate reads the fixture")
	return r
}

// listing writes one line per mutant whose kind starts with prefix: its
// identity, its source and its replacement, then its rule, its reason and
// whether it is viable.
func listing(r *goref.Result, prefix string) string {
	var b strings.Builder
	for _, m := range r.Expect.Mutants {
		if !strings.HasPrefix(m.Kind, prefix) {
			continue
		}
		fmt.Fprintf(&b, "%s %s %d: %s -> %q", m.Scope, m.Kind, m.Nth, m.Original, m.Replacement)
		if m.Rule != "" {
			fmt.Fprintf(&b, " [%s]", m.Rule)
		}
		if m.Reason != "" {
			fmt.Fprintf(&b, " (%s)", m.Reason)
		}
		if m.NotViable {
			b.WriteString(" not-viable")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// statics writes one line per mutant that the enumeration gives a verdict.
func statics(r *goref.Result) string {
	var b strings.Builder
	for _, m := range r.Expect.Mutants {
		if v, ok := r.Static[m.Key]; ok {
			fmt.Fprintf(&b, "%s %s %d: %s\n", m.Scope, m.Kind, m.Nth, v)
		}
	}
	return b.String()
}

// skipped writes one line per skipped site: its source and the reason.
func skipped(r *goref.Result, src string) string {
	var b strings.Builder
	for _, s := range r.Expect.Skipped {
		fmt.Fprintf(&b, "%s: %s\n", src[offset(src, s.Start):offset(src, s.End)], s.Reason)
	}
	return b.String()
}

// offset returns the byte offset of p in src.
func offset(src string, p spec.Position) int {
	line := 1
	for i := range len(src) {
		if line == p.Line {
			return i + p.Column - 1
		}
		if src[i] == '\n' {
			line++
		}
	}
	return len(src)
}

// formsCompile checks that the form of every mutant of r compiles in the
// fixture in dir.
func formsCompile(t *testing.T, dir string, r *goref.Result) {
	t.Helper()
	for _, m := range r.Expect.Mutants {
		expect.NoError(t, compiles(dir, r.Forms[m.Key]), "the form of "+m.ID()+" compiles")
	}
}

// compiles type-checks the package in dir with one mutant's form applied,
// and returns the first error of the parser or the type checker.
func compiles(dir string, form goref.Form) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, goSuffix) || strings.HasSuffix(name, testSuffix) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if name == form.File {
			src = []byte(string(src[:form.Start]) + form.Text + string(src[form.End:]))
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			return err
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, compiler, nil)}
	_, err = conf.Check(fixturePath, fset, files, nil)
	return err
}

// ordinary returns the environment of the test process without the
// protocol's variables, so a fixture's tests run as in an ordinary build.
func ordinary(p spec.Protocol) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != p.Variable && name != p.Instrumented {
			env = append(env, kv)
		}
	}
	return env
}

// testForm copies the fixture in dir with one mutant's form applied, runs
// its tests with go test in the environment env, and returns the command's
// output.
func testForm(t *testing.T, dir string, form goref.Form, env []string) string {
	t.Helper()
	work := t.TempDir()
	entries, err := os.ReadDir(dir)
	assert.NoError(t, err, "the fixture lists")
	for _, entry := range entries {
		src, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		assert.NoError(t, err, entry.Name()+" reads")
		if entry.Name() == form.File {
			src = []byte(string(src[:form.Start]) + form.Text + string(src[form.End:]))
		}
		writeFile(t, filepath.Join(work, entry.Name()), string(src))
	}
	cmd := exec.CommandContext(t.Context(), goCommand, "test", "-count=1", ".")
	cmd.Dir, cmd.Env = work, env
	// A test that fails exits with status 1, which the output states.
	out, _ := cmd.CombinedOutput()
	return string(out)
}
