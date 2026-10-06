// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package goref

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mutate-spec/tools/internal/spec"
)

func definition(t *testing.T) (spec.Catalogue, spec.Overlay) {
	t.Helper()
	var cat spec.Catalogue
	var ov spec.Overlay
	for path, v := range map[string]any{"../../../spec/catalogue.json": &cat, "../../../spec/overlays/go.json": &ov} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
	}
	return cat, ov
}

// fixture writes files into a new directory, with a go.mod at go 1.21
// unless files has one.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module fixture\n\ngo 1.21\n"
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func enumerate(t *testing.T, dir string, lines ...string) *Result {
	t.Helper()
	cat, ov := definition(t)
	r, err := Enumerate(dir, cat, ov, lines)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// listing writes one line per mutant whose kind starts with prefix: its
// identity, its source and its replacement, then its rule, its reason and
// whether it is viable.
func listing(r *Result, prefix string) string {
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
func statics(r *Result) string {
	var b strings.Builder
	for _, m := range r.Expect.Mutants {
		if v, ok := r.Static[m.Key]; ok {
			fmt.Fprintf(&b, "%s %s %d: %s\n", m.Scope, m.Kind, m.Nth, v)
		}
	}
	return b.String()
}

// skipped writes one line per skipped site: its source and the reason.
func skipped(r *Result, src string) string {
	var b strings.Builder
	for _, s := range r.Expect.Skipped {
		fmt.Fprintf(&b, "%s: %s\n", src[offset(src, s.Start):offset(src, s.End)], s.Reason)
	}
	return b.String()
}

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

func want(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// compiles type-checks the package in dir with one mutant's form applied.
func compiles(t *testing.T, dir string, form Form) error {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
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
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", nil)}
	_, err = conf.Check("fixture", fset, files, nil)
	return err
}

func TestKeyHashesTheFieldsSeparatedByNul(t *testing.T) {
	// Each digest was computed with printf and sha256sum, outside Go.
	cases := []struct {
		file, scope, kind, tokens string
		occurrence                int
		want                      string
	}{
		{"between.go", "between", "ror-boundary", "x < hi", 0, "ba74901e75daf19d"},
		{"steps.go", "steps", "ror-false", "n > 0", 1, "681ab23c877298d7"},
	}
	for _, c := range cases {
		if got := Key("dokimi-mutate-key/1", c.file, c.scope, c.kind, c.tokens, c.occurrence); got != c.want {
			t.Errorf("Key(%q, %q, %q, %q, %d) = %s, want %s", c.file, c.scope, c.kind, c.tokens, c.occurrence, got, c.want)
		}
	}
}

func TestTokensDropCommentsAndInsertedSemicolons(t *testing.T) {
	cases := map[string]string{
		"a /* note */ +\n\tb":          "a + b",
		"if x {\n\ty = 1 // set\n}":    "if x { y = 1 }",
		"for i := 0; i < n; i++ {\n}":  "for i := 0 ; i < n ; i ++ { }",
		"s == \"a  b\"":                "s == \"a  b\"",
		"x<-y":                         "x <- y",
		"return *new(int), errors.New": "return * new ( int ) , errors . New",
	}
	for src, tokens := range cases {
		if got := Tokens([]byte(src)); got != tokens {
			t.Errorf("Tokens(%q) = %q, want %q", src, got, tokens)
		}
	}
}

func TestCutKeepsAWindowAroundTheFirstDifference(t *testing.T) {
	a, b := strings.Repeat("a", 150), strings.Repeat("b", 200)
	cases := []struct{ original, replacement, wantOriginal, wantReplacement string }{
		{"if x {\n\t\ty = 1\n\t}", "x < 1\t\t&& y", "if x { y = 1 }", "x < 1 && y"},
		{strings.Repeat("é", 120), strings.Repeat("ü", 120), strings.Repeat("é", 120), strings.Repeat("ü", 120)},
		{strings.Repeat("é", 121), "", strings.Repeat("é", 119) + "…", ""},
		{"x < " + b, "x <= " + b, ("x < " + b)[:119] + "…", ("x <= " + b)[:119] + "…"},
		{a + " && ok", a, "…" + a[:40] + " && ok", "…" + a[:40]},
		{a + " + " + b, a + " - " + b, "…" + (a + " + " + b)[111:229] + "…", "…" + (a + " - " + b)[111:229] + "…"},
	}
	for _, c := range cases {
		original, replacement := Cut(c.original, c.replacement)
		if original != c.wantOriginal || replacement != c.wantReplacement {
			t.Errorf("Cut(%q, %q) = %q, %q, want %q, %q", c.original, c.replacement, original, replacement, c.wantOriginal, c.wantReplacement)
		}
	}
}

const kinds = `package fixture

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

func TestEnumerateMakesEveryKindInSourceOrder(t *testing.T) {
	dir := fixture(t, map[string]string{"kinds.go": kinds})
	r := enumerate(t, dir)
	want(t, listing(r, ""), `kinds aor 0: a + b -> "a - b"
kinds aor 1: total -= b -> "total += b"
kinds sbr-delete 0: total -= b -> ""
kinds sbr-delete 1: if a < b && ok { total++ } -> ""
kinds lcr-left 0: a < b && ok -> "a < b"
kinds lcr-right 0: a < b && ok -> "ok"
kinds lcr-false 0: a < b && ok -> "false"
kinds ror-boundary 0: a < b -> "a <= b"
kinds ror-false 0: a < b -> "false"
kinds uoi-incdec 0: total++ -> "total--"
kinds sbr-delete 2: if xs == nil || a >= b { return -a } -> ""
kinds lcr-left 1: xs == nil || a >= b -> "xs == nil"
kinds lcr-right 1: xs == nil || a >= b -> "a >= b"
kinds lcr-true 0: xs == nil || a >= b -> "true"
kinds ror-true 0: xs == nil -> "true"
kinds ror-false 1: xs == nil -> "false"
kinds ror-boundary 1: a >= b -> "a > b"
kinds ror-true 1: a >= b -> "true"
kinds sbr-zero 0: return -a -> "return 0"
kinds uoi-minus 0: -a -> "a"
kinds sbr-delete 3: if !ok { return total } -> ""
kinds uoi-not 0: !ok -> "ok"
kinds sbr-zero 1: return total -> "return 0"
`)
	if len(r.Expect.Skipped) != 0 || len(r.Expect.Errors) != 0 || len(r.Static) != 0 {
		t.Errorf("skipped %v, errors %v, static %v: want none", r.Expect.Skipped, r.Expect.Errors, r.Static)
	}
	m := r.Expect.Mutants[1]
	if m.File != "kinds.go" || m.Start != (spec.Position{Line: 5, Column: 2}) || m.End != (spec.Position{Line: 5, Column: 12}) {
		t.Errorf("aor 1 is at %s %v-%v, want kinds.go 5:2-5:12", m.File, m.Start, m.End)
	}
	for _, m := range r.Expect.Mutants {
		if err := compiles(t, dir, r.Forms[m.Key]); err != nil {
			t.Errorf("%s %s %d: the form does not compile: %v", m.Scope, m.Kind, m.Nth, err)
		}
	}
}

func TestEnumerateNamesScopes(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"scopes.go": `package fixture

var limit = 1 + compute(3)

func compute(n int) int { return n }

type box[T any] struct{ n int }

func (b *box[T]) grow(by int) int {
	f := func() int { return b.n + by }
	return f()
}
`}))
	want(t, listing(r, ""), `limit aor 0: 1 + compute(3) -> "1 - compute(3)"
compute sbr-zero 0: return n -> "return 0"
box.grow sbr-zero 0: return b.n + by -> "return 0"
box.grow aor 0: b.n + by -> "b.n - by"
box.grow sbr-zero 1: return f() -> "return 0"
`)
}

func TestEnumerateNamesTheScopeOfAReceiverInParentheses(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"t.go": `package fixture

type T struct{ x int }

func (p (*T)) Inc() int { return p.x + 1 }

func (v (T)) Dec() int { return v.x - 1 }

func (p *(T)) Double() int { return p.x * 2 }
`}))
	want(t, listing(r, "aor"), `T.Inc aor 0: p.x + 1 -> "p.x - 1"
T.Dec aor 0: v.x - 1 -> "v.x + 1"
T.Double aor 0: p.x * 2 -> "p.x / 2"
`)
}

const steps = `package fixture

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

func TestEnumerateKeysIdenticalSitesByOccurrence(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"steps.go": steps}))
	var falses []spec.ExpectMutant
	for _, m := range r.Expect.Mutants {
		if m.Kind == "ror-false" {
			falses = append(falses, m)
		}
	}
	if len(falses) != 2 {
		t.Fatalf("got %d ror-false mutants, want 2", len(falses))
	}
	for i, m := range falses {
		if m.Nth != i || m.Occurrence != i {
			t.Errorf("mutant %d has nth %d and occurrence %d, want %d and %d", i, m.Nth, m.Occurrence, i, i)
		}
	}
	if falses[0].Key == falses[1].Key {
		t.Errorf("both sites have key %s", falses[0].Key)
	}
	if falses[1].Key != "681ab23c877298d7" {
		t.Errorf("the second site's key is %s, want 681ab23c877298d7", falses[1].Key)
	}
}

func TestEnumerateKeepsKeysWhenCodeAboveMoves(t *testing.T) {
	before := enumerate(t, fixture(t, map[string]string{"steps.go": steps}))
	moved := strings.Replace(steps, "func steps", "func other() {}\n\n// steps counts down.\nfunc steps", 1)
	after := enumerate(t, fixture(t, map[string]string{"steps.go": moved}))
	if len(before.Expect.Mutants) != len(after.Expect.Mutants) {
		t.Fatalf("%d mutants before and %d after", len(before.Expect.Mutants), len(after.Expect.Mutants))
	}
	for i, m := range before.Expect.Mutants {
		a := after.Expect.Mutants[i]
		if m.Key != a.Key {
			t.Errorf("%s %s %d: key %s before and %s after", m.Scope, m.Kind, m.Nth, m.Key, a.Key)
		}
		if a.Start.Line != m.Start.Line+3 {
			t.Errorf("%s %s %d: line %d before and %d after, want 3 lines lower", m.Scope, m.Kind, m.Nth, m.Start.Line, a.Start.Line)
		}
	}
}

const skips = `package fixture

import "time"

const big = 4*1024 + 1

var buf [2 * 8]byte

type flag bool

func generic[T int | float64](a, b T) T {
	return a + b
}

func named(n int) flag {
	return n < 4
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

func index() int { return 0 }

func shift(d time.Duration, n uint) bool {
	return d < 1<<n
}
`

func TestEnumerateListsSkippedSitesWithTheirReasons(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"skips.go": skips}))
	want(t, skipped(r, skips), `4*1024 + 1: constant expression
2 * 8: constant expression
a + b: operand of type-parameter type
n < 4: named boolean result
n > 0 && n < 9: named boolean result
n > 0: named boolean result
n < 9: named boolean result
xs[index()] += 1: assignment target with side effects
d < 1<<n: untyped constant in a non-constant shift
`)
	want(t, listing(r, "lcr-"), `recovers lcr-left 0: len(xs) > 0 && recover() == nil -> "len(xs) > 0"
recovers lcr-right 0: len(xs) > 0 && recover() == nil -> "recover() == nil"
recovers lcr-false 0: len(xs) > 0 && recover() == nil -> "false"
`)
}

func TestEnumerateNegatesOnlyBooleanValues(t *testing.T) {
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
	r := enumerate(t, dir)
	want(t, listing(r, "uoi-not"), `positions uoi-not 0: v -> "!v"
positions uoi-not 1: present -> "!present"
positions uoi-not 2: *q -> "!*q"
positions uoi-not 3: !w -> "w"
`)
	for _, m := range r.Expect.Mutants {
		if err := compiles(t, dir, r.Forms[m.Key]); err != nil {
			t.Errorf("%s %s %d: the form does not compile: %v", m.Scope, m.Kind, m.Nth, err)
		}
	}
}

func TestEnumerateKeepsStatementsThatADeletionBreaks(t *testing.T) {
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
`}))
	want(t, listing(r, "sbr-"), `ends sbr-delete 0: if n > 0 { return 0, nil } -> ""
ends sbr-delete 1: go func() {}() -> ""
`)
}

func TestEnumerateComparesAnyTypeForEquality(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"eq.go": `package fixture

func eq(i any, xs []int, p *int, f func()) bool {
	return i == 3 && xs != nil && p == nil && f != nil
}
`}))
	want(t, listing(r, "ror-"), `eq ror-true 0: i == 3 -> "true"
eq ror-false 0: i == 3 -> "false"
eq ror-true 1: xs != nil -> "true"
eq ror-false 1: xs != nil -> "false"
eq ror-true 2: p == nil -> "true"
eq ror-false 2: p == nil -> "false"
eq ror-true 3: f != nil -> "true"
eq ror-false 3: f != nil -> "false"
`)
	if len(r.Expect.Skipped) != 0 {
		t.Errorf("skipped %v, want none", r.Expect.Skipped)
	}
}

func TestEnumerateSuppressesRuleFamilies(t *testing.T) {
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
`}))
	want(t, listing(r, ""), `logs sbr-delete 0: defer log.Println("done") -> "" [logging]
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
`)
}

// TestEnumerateRunsAMutantThatLeavesOutTheOnlyUseOfAName enumerates
// deletions, returns of zero values and connector mutants that leave out
// the only use of a variable, an import, a label or a type switch's symbol.
// None is not viable, because each form keeps the code that the mutant
// does not run behind a constant that skips it, and each form compiles.
func TestEnumerateRunsAMutantThatLeavesOutTheOnlyUseOfAName(t *testing.T) {
	cases := []struct{ file, src, prefix, want string }{
		{"unused.go", `package fixture

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
`, "sbr-delete", `keep sbr-delete 0: defer cancel() -> ""
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
`},
		{"switch.go", `package fixture

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
`, "sbr-", `size sbr-delete 0: switch x := v.(type) { case string: n = len(x) case []int: n = len(x) } -> ""
size sbr-delete 1: n = len(x) -> ""
size sbr-delete 2: n = len(x) -> ""
size sbr-zero 0: return n -> "return 0"
only sbr-delete 0: switch x := v.(type) { case string: n = len(x) case int: n = 1 } -> ""
only sbr-delete 1: n = len(x) -> ""
only sbr-delete 2: n = 1 -> ""
only sbr-zero 0: return n -> "return 0"
`},
		{"labels.go", `package fixture

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
`, "sbr-", `first sbr-delete 0: if xs[i] < 0 { break scan } -> ""
first sbr-zero 0: return i -> "return 0"
lit sbr-zero 0: return func() int { done: for range xs { break done } return len(xs) } -> "return nil"
lit sbr-zero 1: return len(xs) -> "return 0"
`},
		{"names.go", `package fixture

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
`, "", `positive sbr-delete 0: if v, ok := m[key]; ok && v > 0 { return true } -> ""
positive lcr-left 0: ok && v > 0 -> "ok"
positive lcr-right 0: ok && v > 0 -> "v > 0"
positive lcr-false 0: ok && v > 0 -> "false"
positive ror-boundary 0: v > 0 -> "v >= 0"
positive ror-false 0: v > 0 -> "false"
positive sbr-zero 0: return true -> "return false"
size sbr-zero 0: return n -> "return 0"
upper sbr-zero 0: return strings.ToUpper(s) -> "return \"\""
`},
	}
	for _, c := range cases {
		dir := fixture(t, map[string]string{c.file: c.src})
		r := enumerate(t, dir)
		want(t, listing(r, c.prefix), c.want)
		if s := statics(r); s != "" {
			t.Errorf("%s gives the verdicts\n%s\nwant none", c.file, s)
		}
		for _, m := range r.Expect.Mutants {
			if err := compiles(t, dir, r.Forms[m.Key]); err != nil {
				t.Errorf("%s %s %d: the form does not compile: %v", m.Scope, m.Kind, m.Nth, err)
			}
		}
	}
}

// TestEnumerateMarksADivisionOfAnIntegerByAConstantZeroNotViable checks
// the rule against the type checker: the form of a mutant fails to compile
// exactly when the mutant is not viable.
func TestEnumerateMarksADivisionOfAnIntegerByAConstantZeroNotViable(t *testing.T) {
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
	r := enumerate(t, dir)
	want(t, listing(r, "aor"), `scale aor 0: x * 0 -> "x / 0" not-viable
scale aor 1: x * off -> "x / off" not-viable
scale aor 2: x * y -> "x / y"
scale aor 3: x *= 0 -> "x /= 0" not-viable
scale aor 4: f *= 0 -> "f /= 0"
scale aor 5: a + b + c -> "a + b - c"
scale aor 6: a + b -> "a - b"
scale aor 7: f * 0 -> "f / 0"
`)
	for _, m := range r.Expect.Mutants {
		err := compiles(t, dir, r.Forms[m.Key])
		if m.NotViable != (err != nil) || err != nil && !strings.Contains(err.Error(), "division by zero") {
			t.Errorf("%s %s %d: not viable %v, and the form's type check gives %v", m.Scope, m.Kind, m.Nth, m.NotViable, err)
		}
	}
}

func TestEnumerateSuppressesTheMarksOfTestHelpers(t *testing.T) {
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
`}))
	want(t, listing(r, "sbr-delete"), `viaTB sbr-delete 0: tb.Helper() -> "" [helper]
viaT sbr-delete 0: t.Helper() -> "" [helper]
viaF sbr-delete 0: f.Helper() -> "" [helper]
viaOwn sbr-delete 0: tb.Helper() -> "" [helper]
viaOther sbr-delete 0: m.Helper(1) -> ""
viaOther sbr-delete 1: c.Helper() -> ""
`)
}

// TestEnumerateSuppressesTheCallsOfACancelFunctionOfADeadline enumerates
// calls of variables that hold the cancel function of a context with a
// deadline, and of variables that hold other values too, or whose address,
// declaration or scope lets other code change them.
func TestEnumerateSuppressesTheCallsOfACancelFunctionOfADeadline(t *testing.T) {
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
`}))
	want(t, listing(r, "sbr-delete"), `stop sbr-delete 0: (*f)() -> ""
declared sbr-delete 0: defer cancel() -> "" [timing]
assigned sbr-delete 0: parent, cancel = context.WithDeadline(parent, d) -> ""
assigned sbr-delete 1: cancel() -> "" [timing]
assigned sbr-delete 2: go cancel() -> "" [timing]
assigned sbr-delete 3: defer func() { cancel() }() -> ""
assigned sbr-delete 4: cancel() -> "" [timing]
assigned sbr-delete 5: if cancel != nil { cancel() } -> "" [timing]
assigned sbr-delete 6: cancel() -> "" [timing]
untyped sbr-delete 0: _, f = context.WithTimeout(parent, time.Second) -> ""
untyped sbr-delete 1: f() -> "" [timing]
cancelled sbr-delete 0: defer cancel() -> ""
mixed sbr-delete 0: if bound { _, cancel = context.WithTimeout(parent, time.Second) } -> ""
mixed sbr-delete 1: _, cancel = context.WithTimeout(parent, time.Second) -> ""
mixed sbr-delete 2: cancel() -> ""
addressed sbr-delete 0: stop(&cancel) -> ""
addressed sbr-delete 1: cancel() -> ""
parameter sbr-delete 0: cancel() -> ""
named sbr-delete 0: ctx, cancel = context.WithTimeout(context.Background(), time.Second) -> ""
named sbr-delete 1: cancel() -> ""
shared sbr-delete 0: _, global = context.WithTimeout(parent, time.Second) -> ""
shared sbr-delete 1: global() -> ""
`)
}

func TestEnumerateSuppressesAnnotatedKindsOnOneLine(t *testing.T) {
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
`}))
	var b strings.Builder
	for _, m := range r.Expect.Mutants {
		if m.Reason != "" {
			fmt.Fprintf(&b, "%s %s %d: %s\n", m.Scope, m.Kind, m.Nth, m.Reason)
		}
	}
	want(t, b.String(), `best ror-boundary 0: an equal value leaves top unchanged
clamp ror-boundary 0: the caller checks the bound
clamp ror-false 0: the caller checks the bound
`)
	if len(r.Expect.Errors) != 0 {
		t.Errorf("errors %v, want none", r.Expect.Errors)
	}
}

func TestEnumerateReportsAnnotationErrors(t *testing.T) {
	cases := map[string]struct {
		line   string
		errors string
	}{
		"no reason":   {"if x > 1 { //dokimi:mutate-skip ror-boundary", "[annotation-without-reason]"},
		"empty":       {"if x > 1 { //dokimi:mutate-skip ror-boundary:  ", "[annotation-without-reason]"},
		"stale":       {"if x > 1 { //dokimi:mutate-skip aor: no arithmetic here", "[stale-annotation]"},
		"unknown":     {"if x > 1 { //dokimi:mutate-skip ror-bound: a misspelt kind", "[stale-annotation]"},
		"not a skip":  {"if x > 1 { //dokimi:mutate-skipped ror-boundary: another directive", "[]"},
		"applies":     {"if x > 1 { //dokimi:mutate-skip ror-boundary, sbr: two kinds", "[]"},
		"every kind":  {"if x > 1 { //dokimi:mutate-skip all: every kind", "[]"},
		"next line":   {"//dokimi:mutate-skip ror-boundary: on the line below\n\tif x > 1 {", "[]"},
		"line before": {"//dokimi:mutate-skip ror-boundary: two lines below\n\n\tif x > 1 {", "[stale-annotation]"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			src := "package fixture\n\nfunc f(x int) int {\n\t" + c.line + "\n\t\treturn 1\n\t}\n\treturn x\n}\n"
			r := enumerate(t, fixture(t, map[string]string{"f.go": src}))
			if got := fmt.Sprint(r.Expect.Errors); got != c.errors {
				t.Errorf("errors %s, want %s", got, c.errors)
			}
		})
	}
}

func TestEnumerateReadsOnlyTheBuildsOwnSource(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{
		"a.go":       "package fixture\n\nfunc used(x int) int { return x + 1 }\n",
		"gen.go":     "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc generated(x int) int { return x * 2 }\n",
		"ignored.go": "//go:build ignore\n\npackage other\n\nfunc ignored(x int) int { return x - 3 }\n",
		"a_test.go":  "package fixture\n\nfunc helper(x int) int { return x / 4 }\n",
	}))
	want(t, listing(r, ""), `used sbr-zero 0: return x + 1 -> "return 0"
used aor 0: x + 1 -> "x - 1"
`)
}

func TestEnumerateReadsAGeneratedFileThatIncludesItself(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{
		"included.go": "// Code generated by kanon. DO NOT EDIT.\n//dokimi:mutate-include\n\npackage fixture\n\n" +
			"func included(x int) int { return x * 2 }\n",
		"late.go": "// Code generated by kanon. DO NOT EDIT.\n\npackage fixture\n\n//dokimi:mutate-include\n" +
			"func late(x int) int { return x * 3 }\n",
		"exempt.go": "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc exempt(x int) int { return x * 4 }\n",
	}))
	want(t, listing(r, "aor"), `included aor 0: x * 2 -> "x / 2"
`)
	// The generated files without the directive are left out, each with the
	// two mutants of its function: a return of zero values and an aor.
	if got := fmt.Sprint(r.Expect.Generated); got != "[{exempt.go 2} {late.go 2}]" {
		t.Errorf("generated %s, want exempt.go and late.go with 2 mutants each", got)
	}
}

func TestEnumerateLeavesAReturnOfZeroValuesAlone(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"zero.go": `package fixture

type point struct{ x, y int }

func structs(ok bool) (point, bool) {
	if ok {
		return point{x: 0}, false
	}
	return point{}, false
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
`}))
	want(t, listing(r, "sbr-zero"), `values sbr-zero 0: return []int{}, map[int]int{}, point{x: 1} -> "return nil, nil, point{}"
`)
}

func TestEnumerateReturnsZeroForTheValueThatNewOfAnExpressionPointsTo(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{
		"go.mod": "module fixture\n\ngo 1.26\n",
		"new.go": "package fixture\n\nfunc pointed(x int) (int, int) {\n\treturn *new(x), *new(int)\n}\n",
	}))
	want(t, listing(r, "sbr-zero"), `pointed sbr-zero 0: return *new(x), *new(int) -> "return 0, 0"`+"\n")
}

func TestEnumerateWritesZeroValuesAsGoCodeDoes(t *testing.T) {
	dir := fixture(t, map[string]string{"spell.go": `package fixture

import (
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
`})
	r := enumerate(t, dir)
	want(t, listing(r, "sbr-zero"), `all sbr-zero 0: return n, f, c, s, ok, d, nm -> "return 0, 0, 0, \"\", false, 0, \"\""
refs sbr-zero 0: return p, xs, m, ch, fn, e, u -> "return nil, nil, nil, nil, nil, nil, nil"
composites sbr-zero 0: return p, a, s -> "return pair{}, [3]int{}, struct{ x int }{}"
generic sbr-zero 0: return v, nil -> "return *new(T), nil"
`)
	for _, m := range r.Expect.Mutants {
		if err := compiles(t, dir, r.Forms[m.Key]); err != nil {
			t.Errorf("%s %s %d: the form does not compile: %v", m.Scope, m.Kind, m.Nth, err)
		}
	}
}

const clamp = `package fixture

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

func TestEnumerateMarksMutantsOutsideTheSelection(t *testing.T) {
	r := enumerate(t, fixture(t, map[string]string{"clamp.go": clamp}), "clamp.go:7-9")
	want(t, statics(r), `clamp sbr-delete 0: not-selected
clamp ror-boundary 0: not-selected
clamp ror-false 0: not-selected
clamp sbr-zero 0: not-selected
clamp sbr-zero 2: not-selected
`)
}

func TestEnumerateRanksNotSelectedThenSuppressedThenNotViable(t *testing.T) {
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
`}), "weight.go:10-18")
	want(t, listing(r, "aor"), `outside aor 0: x * weight -> "x / weight"
outside aor 1: y + 1 -> "y - 1"
inside aor 0: x * weight -> "x / weight" (a test of the ranking) not-viable
inside aor 1: y + 1 -> "y - 1"
plain aor 0: x * weight -> "x / weight" not-viable
plain aor 1: y + 1 -> "y - 1"
`)
	want(t, statics(r), `outside aor 0: not-selected
outside sbr-zero 0: not-selected
outside aor 1: not-selected
inside aor 0: suppressed
plain aor 0: not-viable
`)
	if len(r.Expect.Errors) != 0 {
		t.Errorf("errors %v, want none: an annotation outside the selection still applies", r.Expect.Errors)
	}
}

func TestEnumerateSuppressesACompoundStatementWhoseBodiesTheFamiliesSuppress(t *testing.T) {
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
	r := enumerate(t, dir)
	want(t, listing(r, ""), `warn sbr-delete 0: if err != nil { log.Printf("warning: %v", err) } -> "" [logging]
warn ror-true 0: err != nil -> "true" [logging]
warn ror-false 0: err != nil -> "false" [logging]
warn sbr-delete 1: log.Printf("warning: %v", err) -> "" [logging]
fail sbr-delete 0: if err != nil { log.Printf("failure: %v", err) return fmt.Errorf("fail: %w", err) } -> ""
fail ror-true 0: err != nil -> "true"
fail ror-false 0: err != nil -> "false"
fail sbr-delete 1: log.Printf("failure: %v", err) -> "" [logging]
fail sbr-zero 0: return fmt.Errorf("fail: %w", err) -> "return nil"
each sbr-delete 0: for _, x := range xs { log.Printf("value: %d", x) } -> "" [logging]
each sbr-delete 1: log.Printf("value: %d", x) -> "" [logging]
drain sbr-delete 0: for x := range ch { log.Printf("value: %d", x) } -> ""
drain sbr-delete 1: log.Printf("value: %d", x) -> "" [logging]
flush sbr-delete 0: if _, err := w.Write(buf); err != nil { log.Printf("flush: %v", err) } -> ""
flush ror-true 0: err != nil -> "true" [logging]
flush ror-false 0: err != nil -> "false" [logging]
flush sbr-delete 1: log.Printf("flush: %v", err) -> "" [logging]
probe sbr-delete 0: if _, err := w.Write(nil); err != nil && ready() { log.Print("not ready") } -> ""
probe lcr-left 0: err != nil && ready() -> "err != nil"
probe lcr-right 0: err != nil && ready() -> "ready()"
probe lcr-false 0: err != nil && ready() -> "false"
probe ror-true 0: err != nil -> "true"
probe ror-false 0: err != nil -> "false"
probe sbr-delete 1: log.Print("not ready") -> "" [logging]
level sbr-delete 0: switch err := check(); { case err == nil: log.Print("ok") default: log.Printf("failed: %v", err) } -> ""
level ror-true 0: err == nil -> "true" [logging]
level ror-false 0: err == nil -> "false" [logging]
level sbr-delete 1: log.Print("ok") -> "" [logging]
level sbr-delete 2: log.Printf("failed: %v", err) -> "" [logging]
sign sbr-delete 0: switch { case n > 0: log.Print("positive") case n < 0: log.Print("negative") default: log.Print("zero") } -> "" [logging]
sign ror-boundary 0: n > 0 -> "n >= 0" [logging]
sign ror-false 0: n > 0 -> "false" [logging]
sign sbr-delete 1: log.Print("positive") -> "" [logging]
sign ror-boundary 1: n < 0 -> "n <= 0" [logging]
sign ror-false 1: n < 0 -> "false" [logging]
sign sbr-delete 2: log.Print("negative") -> "" [logging]
sign sbr-delete 3: log.Print("zero") -> "" [logging]
report sbr-delete 0: if ok { log.Print("ok") } else if d > 0 { time.Sleep(d) } -> "" [logging]
report uoi-not 0: ok -> "!ok" [logging]
report sbr-delete 1: log.Print("ok") -> "" [logging]
report ror-boundary 0: d > 0 -> "d >= 0" [logging]
report ror-false 0: d > 0 -> "false" [logging]
report sbr-delete 2: time.Sleep(d) -> "" [timing]
count sbr-delete 0: for i := 0; i < len(xs); i++ { log.Print(xs[i]) } -> "" [logging]
count ror-boundary 0: i < len(xs) -> "i <= len(xs)" [logging]
count ror-false 0: i < len(xs) -> "false" [logging]
count uoi-incdec 0: i++ -> "i--" [logging]
count sbr-delete 1: log.Print(xs[i]) -> "" [logging]
count sbr-delete 2: for n = 0; n < len(xs); n++ { log.Print(xs[n]) } -> ""
count ror-boundary 1: n < len(xs) -> "n <= len(xs)"
count ror-false 1: n < len(xs) -> "false"
count uoi-incdec 1: n++ -> "n--"
count sbr-delete 3: log.Print(xs[n]) -> "" [logging]
count sbr-zero 0: return n -> "return 0"
nothing sbr-delete 0: if ok { } -> ""
nothing uoi-not 0: ok -> "!ok"
`)
	for _, m := range r.Expect.Mutants {
		if err := compiles(t, dir, r.Forms[m.Key]); err != nil {
			t.Errorf("%s %s %d: the form does not compile: %v", m.Scope, m.Kind, m.Nth, err)
		}
	}
}
