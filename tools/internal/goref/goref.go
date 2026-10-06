// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

// Package goref enumerates a Go fixture's mutants by the Go overlay's
// rules: every site, its kinds, scope, key and source position, the
// mutants a rule family or an annotation suppresses, the sites the rules
// skip, and the source that applies each mutant alone.
//
// It is the reference the corpus is generated from. An engine is checked
// against the corpus, never against this package.
package goref

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"mutate-spec/tools/internal/spec"
)

// Language is the language that goref enumerates: the name of its overlay,
// and of the directory of a case's fixture in it.
const Language = "go"

// Form is the source that applies one mutant alone: Text replaces the
// bytes from Start to End of File. A form evaluates the site's operands as
// the mutated expression does, and keeps the code that the mutant does not
// run behind a constant that skips it, so every name that the site uses
// remains in use, and the mutated file compiles wherever the original does
// and the mutant is viable.
type Form struct {
	File       string
	Start, End int
	Text       string
}

// Result is a fixture's enumeration: the expectations in the corpus's
// form, the form of each mutant by key, and the verdicts that the
// enumeration alone decides.
type Result struct {
	Expect spec.Expect
	Forms  map[string]Form
	// Static maps a key to not-selected, suppressed or not-viable, in that
	// order of priority. A mutant without an entry runs.
	Static map[string]string
}

// Options are the settings of one enumeration that a case states: Lines is
// the run's selection, as file:first-last entries, and IncludeGenerated
// makes every generated file a target.
type Options struct {
	Lines            []string
	IncludeGenerated bool
}

// Enumerate reads the Go package in dir and returns its mutants under the
// catalogue and the Go overlay, with the selection and the generated files
// that opts states.
func Enumerate(dir string, cat spec.Catalogue, ov spec.Overlay, opts Options) (*Result, error) {
	pkg, err := load(dir, ov.Comment+cat.Include)
	if err != nil {
		return nil, err
	}
	e := &enumerator{
		pkg: pkg, cat: cat, ov: ov, families: callFamilies(ov), classes: classes(cat),
		calls: map[*ast.CallExpr]string{}, operands: map[ast.Expr]bool{},
	}
	e.results, e.unwritten = e.resultVariables(), e.unwrittenVariables()
	for _, f := range pkg.files {
		if !f.generated || opts.IncludeGenerated {
			e.file(f)
		}
	}
	r := e.result(opts.Lines)
	r.Expect.Generated = e.generated(opts.IncludeGenerated)
	return r, nil
}

// generated returns each generated file of the package in file order, with
// the number of mutants that the kinds make at its sites, and whether the
// run includes it. The mutants of an included file are the enumeration's
// own. A second enumerator walks a file that the run leaves out, so the
// result contains none of its sites, annotations and suppressions.
func (e *enumerator) generated(include bool) []spec.Generated {
	g := &enumerator{
		pkg: e.pkg, cat: e.cat, ov: e.ov, families: e.families, classes: e.classes,
		calls: map[*ast.CallExpr]string{}, operands: map[ast.Expr]bool{}, results: e.results, unwritten: e.unwritten,
	}
	out := []spec.Generated{}
	for _, f := range e.pkg.files {
		if !f.generated {
			continue
		}
		sites := e.sites
		if !include {
			before := len(g.sites)
			g.file(f)
			sites = g.sites[before:]
		}
		mutants := 0
		for _, s := range sites {
			if s.f == f {
				mutants += len(s.mutants)
			}
		}
		out = append(out, spec.Generated{File: f.name, Mutants: mutants, Included: include})
	}
	return out
}

type file struct {
	name      string
	src       []byte
	ast       *ast.File
	tf        *token.File
	generated bool
}

type pkgInfo struct {
	fset  *token.FileSet
	files []*file
	info  *types.Info
	types *types.Package
}

// The names that load reads in a fixture's directory.
const (
	// goSuffix and testSuffix end the names of Go files and of test files.
	goSuffix   = ".go"
	testSuffix = "_test.go"
	// cgoImport is the import path of cgo, as an import spec quotes it.
	cgoImport = `"C"`
	// compiler names the toolchain whose export data the importer reads.
	compiler = "gc"
	// fixturePath is the import path of the package that load type-checks.
	fixturePath = "fixture"
	// goMod is the module file whose go line sets the language version.
	goMod = "go.mod"
	// goLine starts the go line of a module file, and languagePrefix a
	// language version as go/types spells it, such as go1.21.
	goLine         = "go "
	languagePrefix = "go"
)

// load parses and type-checks the files that the build compiles, without
// test files and without the files that build constraints exclude. A file
// is generated when go/ast.IsGenerated reports it and its header does not
// contain the line include. load reads each file once, and the build's
// constraints read the same bytes.
func load(dir, include string) (*pkgInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	p := &pkgInfo{fset: token.NewFileSet()}
	var syntax []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, goSuffix) || strings.HasSuffix(name, testSuffix) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		ctxt := build.Default
		ctxt.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(src)), nil }
		ok, err := ctxt.MatchFile(dir, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		af, err := parser.ParseFile(p.fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, imp := range af.Imports {
			if imp.Path.Value == cgoImport {
				return nil, fmt.Errorf("%s: imports C, and the reference enumerator reads no cgo file", name)
			}
		}
		syntax = append(syntax, af)
		generated := ast.IsGenerated(af) && !includes(af, include)
		p.files = append(p.files, &file{name: name, src: src, ast: af, tf: p.fset.File(af.Pos()), generated: generated})
	}
	if len(p.files) == 0 {
		return nil, fmt.Errorf("%s: no Go file that the build compiles", dir)
	}
	p.info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(p.fset, compiler, nil), GoVersion: goVersion(dir)}
	if p.types, err = conf.Check(fixturePath, p.fset, syntax, p.info); err != nil {
		return nil, err
	}
	return p, nil
}

// includes reports whether a comment before f's package clause is the line
// directive.
func includes(f *ast.File, directive string) bool {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if c.Text == directive {
				return true
			}
		}
	}
	return false
}

// goVersion returns the go line of dir's go.mod as go/types spells a
// language version, or "" when there is none.
func goVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, goMod))
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), goLine); ok {
			return languagePrefix + strings.TrimSpace(v)
		}
	}
	return ""
}

// classes maps each kind to its class, for annotations that name a class.
func classes(cat spec.Catalogue) map[string]string {
	m := map[string]string{}
	for _, k := range cat.Kinds {
		m[k.ID] = k.Class
	}
	return m
}

// callFamilies maps each API that a family lists to the family.
func callFamilies(ov spec.Overlay) map[string]string {
	m := map[string]string{}
	for family, rules := range ov.Families {
		for _, name := range rules.APIs {
			m[name] = family
		}
	}
	return m
}

// site is one place that one or more mutants change.
type site struct {
	f          *file
	scope      string
	start, end int // byte offsets in f.src, end exclusive
	stmt       ast.Stmt
	mutants    []*mutant
}

type mutant struct {
	site        *site
	kind        string
	replacement string
	form        string
	notViable   bool
	rule        string
	reason      string
	key         string
	occurrence  int
	nth         int
}

type skip struct {
	f          *file
	start, end int
	reason     string
}

// annotation is one dokimi:mutate-skip comment and the line it covers.
type annotation struct {
	f      *file
	line   int
	kinds  map[string]bool
	reason string
	used   bool
}

// suppression is the code that one call to an API of a rule family
// suppresses. For a call family, call is the call: the call itself, a
// statement that makes it and every site inside its parentheses are
// suppressed. For capacity, call is nil and the range is the argument.
type suppression struct {
	f          *file
	start, end int
	family     string
	call       *ast.CallExpr
}

type enumerator struct {
	pkg          *pkgInfo
	cat          spec.Catalogue
	ov           spec.Overlay
	families     map[string]string
	classes      map[string]string
	sites        []*site
	skips        []skip
	annotations  []*annotation
	suppressions []suppression
	errors       []string
	// calls maps each call that a family which lists calls suppresses to the
	// family. operands contains each operand, without its parentheses, of a
	// connector that is a site. results maps each variable whose calls a
	// result rule puts into a family to the family. unwritten contains each
	// unwritten variable of sbr-zero's rule.
	calls     map[*ast.CallExpr]string
	operands  map[ast.Expr]bool
	results   map[*types.Var]string
	unwritten map[*types.Var]bool
}

func (e *enumerator) off(pos token.Pos) int { return e.pkg.fset.Position(pos).Offset }

func (e *enumerator) text(f *file, n ast.Node) string {
	return string(f.src[e.off(n.Pos()):e.off(n.End())])
}

func (e *enumerator) file(f *file) {
	e.parseAnnotations(f)
	for _, decl := range f.ast.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			e.walk(f, d, scopeOf(d), d.Type)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch s := sp.(type) {
				case *ast.ValueSpec:
					if s.Type != nil {
						e.walk(f, s.Type, s.Names[0].Name, nil)
					}
					for i, v := range s.Values {
						e.walk(f, v, s.Names[min(i, len(s.Names)-1)].Name, nil)
					}
				case *ast.TypeSpec:
					e.walk(f, s.Type, s.Name.Name, nil)
				}
			}
		}
	}
	e.quietStatements(f)
}

// quietStatements suppresses each compound statement of f that a family
// which lists calls suppresses as a whole, with every site inside it, and
// the parts of an if or a switch statement that quietSelectors suppresses.
// A candidate is an element of a statement list, a labelled statement's
// statement, or an if statement that is the else branch of another. The
// walk stops at a suppressed statement, which contains the others.
func (e *enumerator) quietStatements(f *file) {
	listed := map[ast.Stmt]bool{}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.BlockStmt:
			for _, st := range s.List {
				listed[st] = true
			}
		case *ast.CaseClause:
			for _, st := range s.Body {
				listed[st] = true
			}
		case *ast.CommClause:
			for _, st := range s.Body {
				listed[st] = true
			}
		case *ast.LabeledStmt:
			listed[s.Stmt] = true
		case *ast.IfStmt:
			if elif, ok := s.Else.(*ast.IfStmt); ok {
				listed[elif] = true
			}
		}
		st, ok := n.(ast.Stmt)
		if !ok || !listed[st] || !isCompound(st) {
			return true
		}
		if !e.quiet(st) {
			e.quietSelectors(f, st)
			return true
		}
		family := e.firstFamily(st)
		if family == "" {
			return true
		}
		e.suppressions = append(e.suppressions, suppression{f: f, start: e.off(st.Pos()), end: e.off(st.End()), family: family})
		return false
	})
}

// quietSelectors suppresses the condition, the tag and the case expressions
// without an effect of an if or a switch statement whose bodies are quiet,
// with a call that a family suppresses, while a part of its header has an
// effect. The family is the bodies' first such call's.
func (e *enumerator) quietSelectors(f *file, st ast.Stmt) {
	var parts []ast.Expr
	var bodies []ast.Stmt
	switch s := st.(type) {
	case *ast.IfStmt:
		parts, bodies = []ast.Expr{s.Cond}, []ast.Stmt{s.Body}
		if s.Else != nil {
			bodies = append(bodies, s.Else)
		}
	case *ast.SwitchStmt:
		if s.Tag != nil {
			parts = append(parts, s.Tag)
		}
		for _, c := range s.Body.List {
			clause := c.(*ast.CaseClause)
			parts, bodies = append(parts, clause.List...), append(bodies, clause.Body...)
		}
	}
	family := ""
	for _, b := range bodies {
		if !e.quiet(b) {
			return
		}
		if family == "" {
			family = e.firstFamily(b)
		}
	}
	if family == "" {
		return
	}
	for _, x := range parts {
		if e.effectFree(x) {
			e.suppressions = append(e.suppressions, suppression{f: f, start: e.off(x.Pos()), end: e.off(x.End()), family: family})
		}
	}
}

// isCompound reports whether st is an if, for, range, switch, type switch,
// block or labelled statement.
func isCompound(st ast.Stmt) bool {
	switch st.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.BlockStmt, *ast.LabeledStmt:
		return true
	}
	return false
}

// quiet reports whether a family that lists calls suppresses st: an
// expression, defer or go statement of such a call, an empty statement, or
// a compound statement whose header has no effect and whose bodies contain
// only quiet statements.
func (e *enumerator) quiet(st ast.Stmt) bool {
	switch s := st.(type) {
	case *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt:
		call := callOf(st)
		return call != nil && e.calls[call] != ""
	case *ast.EmptyStmt:
		return true
	case *ast.LabeledStmt:
		return e.quiet(s.Stmt)
	case *ast.BlockStmt:
		return e.allQuiet(s.List)
	case *ast.IfStmt:
		return e.initFree(s.Init) && e.effectFree(s.Cond) && e.quiet(s.Body) && (s.Else == nil || e.quiet(s.Else))
	case *ast.ForStmt:
		return e.initFree(s.Init) && (s.Cond == nil || e.effectFree(s.Cond)) && e.postFree(s.Post, s.Init) && e.quiet(s.Body)
	case *ast.RangeStmt:
		return s.Tok != token.ASSIGN && e.rangeFree(s.X) && e.quiet(s.Body)
	case *ast.SwitchStmt:
		if !e.initFree(s.Init) || s.Tag != nil && !e.effectFree(s.Tag) {
			return false
		}
		for _, c := range s.Body.List {
			clause := c.(*ast.CaseClause)
			for _, x := range clause.List {
				if !e.effectFree(x) {
					return false
				}
			}
			if !e.allQuiet(clause.Body) {
				return false
			}
		}
		return true
	case *ast.TypeSwitchStmt:
		if !e.initFree(s.Init) || !e.effectFree(s.Assign) {
			return false
		}
		for _, c := range s.Body.List {
			if !e.allQuiet(c.(*ast.CaseClause).Body) {
				return false
			}
		}
		return true
	}
	return false
}

func (e *enumerator) allQuiet(list []ast.Stmt) bool {
	for _, st := range list {
		if !e.quiet(st) {
			return false
		}
	}
	return true
}

// effectFree reports whether evaluating n calls nothing but conversions,
// the builtins len, cap, min, max, real, imag and complex, and the APIs of
// the families that list calls, and receives from no channel. A function
// literal is a value, and its body runs only when a call calls it.
func (e *enumerator) effectFree(n ast.Node) bool {
	free := true
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.UnaryExpr:
			free = free && x.Op != token.ARROW
		case *ast.CallExpr:
			free = free && e.pure(x)
		}
		return free
	})
	return free
}

// pure reports whether call is a conversion, a call of a builtin without an
// effect, or a call that a family which lists calls suppresses.
func (e *enumerator) pure(call *ast.CallExpr) bool {
	if e.calls[call] != "" || e.pkg.info.Types[call.Fun].IsType() {
		return true
	}
	b, ok := e.callee(call).(*types.Builtin)
	return ok && effectFreeBuiltins[b.Name()]
}

// effectFreeBuiltins lists the builtins whose calls the overlay's compound
// rule counts as calls without an effect.
var effectFreeBuiltins = map[string]bool{
	"len": true, "cap": true, "min": true, "max": true, "real": true, "imag": true, "complex": true,
}

// The builtins whose calls the zero-value and the terminating-statement
// rules read.
const (
	builtinNew   = "new"
	builtinPanic = "panic"
)

// initFree reports whether an init statement has no effect: it is absent,
// or a short variable declaration whose values have none.
func (e *enumerator) initFree(st ast.Stmt) bool {
	if st == nil {
		return true
	}
	a, ok := st.(*ast.AssignStmt)
	return ok && a.Tok == token.DEFINE && e.effectFree(a)
}

// postFree reports whether a for loop's post statement has no effect
// outside the loop: it is absent, or an increment, a decrement or an
// assignment of variables that the loop's init statement declares.
func (e *enumerator) postFree(post, init ast.Stmt) bool {
	if post == nil {
		return true
	}
	declared := map[types.Object]bool{}
	if a, ok := init.(*ast.AssignStmt); ok {
		for _, x := range a.Lhs {
			if id, ok := x.(*ast.Ident); ok {
				declared[e.pkg.info.Defs[id]] = true
			}
		}
	}
	local := func(x ast.Expr) bool {
		id, ok := ast.Unparen(x).(*ast.Ident)
		return ok && declared[e.pkg.info.Uses[id]]
	}
	switch s := post.(type) {
	case *ast.IncDecStmt:
		return local(s.X)
	case *ast.AssignStmt:
		for _, x := range s.Lhs {
			if !local(x) {
				return false
			}
		}
		return e.effectFree(s)
	}
	return false
}

// rangeFree reports whether a range clause over x has no effect: x has
// none, and x is a slice, an array, a pointer to an array, a map, a string
// or an integer. A range over a channel receives, and a range over a
// function calls it.
func (e *enumerator) rangeFree(x ast.Expr) bool {
	switch e.pkg.info.TypeOf(x).Underlying().(type) {
	case *types.Slice, *types.Array, *types.Pointer, *types.Map, *types.Basic:
		return e.effectFree(x)
	}
	return false
}

// firstFamily returns the family of the first call in st, in source order,
// that a family which lists calls suppresses, or "" when st has none.
func (e *enumerator) firstFamily(st ast.Stmt) string {
	family := ""
	ast.Inspect(st, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && family == "" {
			family = e.calls[call]
		}
		return family == ""
	})
	return family
}

// scopeOf names a function F as F, and a method M of T or *T as T.M, with
// the receiver's type in parentheses or not. The package type-checks, so a
// method's receiver names a type that the package declares, possibly with
// type parameters.
func scopeOf(d *ast.FuncDecl) string {
	if d.Recv == nil {
		return d.Name.Name
	}
	t := ast.Unparen(d.Recv.List[0].Type)
	if star, ok := t.(*ast.StarExpr); ok {
		t = ast.Unparen(star.X)
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	return t.(*ast.Ident).Name + "." + d.Name.Name
}

// frame is one node on the path from the walk's root to the visited node,
// with the innermost function type around it.
type frame struct {
	node ast.Node
	fn   *ast.FuncType
}

// walk visits root and every node below it. scope is the scope of every
// site it finds, and fn the function around root, or nil.
func (e *enumerator) walk(f *file, root ast.Node, scope string, fn *ast.FuncType) {
	var stack []frame
	constant := 0
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if e.constantSite(top.node) {
				constant--
			}
			return false
		}
		var parent ast.Node
		inner := fn
		if len(stack) > 0 {
			parent = stack[len(stack)-1].node
			inner = stack[len(stack)-1].fn
		}
		// outer is the outermost parenthesized expression around n, or n,
		// and around the closest ancestor that is no parenthesized
		// expression. A negation reads the position of n by them, so
		// parentheses do not change it.
		outer, around := n, parent
		for i := len(stack) - 1; i >= 0; i-- {
			if _, ok := stack[i].node.(*ast.ParenExpr); !ok {
				around = stack[i].node
				break
			}
			outer = stack[i].node
		}
		if lit, ok := n.(*ast.FuncLit); ok {
			inner = lit.Type
		}
		if call, ok := n.(*ast.CallExpr); ok {
			e.family(f, call)
		}
		if e.constantSite(n) {
			if constant == 0 {
				e.addSkip(f, n, spec.SkipConstant)
			}
			constant++
		}
		if constant == 0 {
			e.visit(f, n, parent, outer, around, scope, inner)
		}
		stack = append(stack, frame{node: n, fn: inner})
		return true
	})
}

// constantSite reports whether n is a constant expression of a catalogue
// class: a comparison, a connector, or arithmetic on numbers.
func (e *enumerator) constantSite(n ast.Node) bool {
	b, ok := n.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	tv := e.pkg.info.Types[b]
	if tv.Value == nil {
		return false
	}
	switch b.Op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ, token.LAND, token.LOR:
		return true
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		return isNumber(tv.Type)
	}
	return false
}

// visit makes the sites of n, whose parent is parent. outer and around are
// the position of n without its parentheses, as walk states them.
func (e *enumerator) visit(f *file, n, parent, outer, around ast.Node, scope string, fn *ast.FuncType) {
	switch x := n.(type) {
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND, token.LOR:
			e.connector(f, x, scope)
		case token.EQL, token.NEQ:
			e.equality(f, x, scope)
		case token.LSS, token.LEQ, token.GTR, token.GEQ:
			e.ordered(f, x, scope)
		case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
			e.arithmetic(f, x, scope)
		}
	case *ast.AssignStmt:
		e.compound(f, x, scope)
		e.delete(f, x, parent, scope)
	case *ast.IncDecStmt:
		e.incdec(f, x, parent, scope)
	case *ast.ReturnStmt:
		e.zero(f, x, fn, scope)
	case ast.Stmt:
		e.delete(f, x, parent, scope)
	case *ast.UnaryExpr:
		if x.Op == token.SUB {
			e.minus(f, x, scope)
		}
		e.not(f, x, outer.(ast.Expr), around, scope)
	case ast.Expr:
		e.not(f, x, outer.(ast.Expr), around, scope)
	}
}

func (e *enumerator) newSite(f *file, n ast.Node, scope string) *site {
	s := &site{f: f, scope: scope, start: e.off(n.Pos()), end: e.off(n.End())}
	e.sites = append(e.sites, s)
	return s
}

func (e *enumerator) addSkip(f *file, n ast.Node, reason string) {
	e.skips = append(e.skips, skip{f: f, start: e.off(n.Pos()), end: e.off(n.End()), reason: reason})
}

func (s *site) add(kind, replacement, form string) *mutant {
	m := &mutant{site: s, kind: kind, replacement: replacement, form: form}
	s.mutants = append(s.mutants, m)
	return m
}

var (
	arith    = map[token.Token]token.Token{token.ADD: token.SUB, token.SUB: token.ADD, token.MUL: token.QUO, token.QUO: token.MUL, token.REM: token.MUL}
	boundary = map[token.Token]token.Token{token.LSS: token.LEQ, token.LEQ: token.LSS, token.GTR: token.GEQ, token.GEQ: token.GTR}
	assign   = map[token.Token]token.Token{token.ADD_ASSIGN: token.SUB_ASSIGN, token.SUB_ASSIGN: token.ADD_ASSIGN, token.MUL_ASSIGN: token.QUO_ASSIGN, token.QUO_ASSIGN: token.MUL_ASSIGN, token.REM_ASSIGN: token.MUL_ASSIGN}
)

// swapped returns the site's source with the operator at opPos replaced by
// to. The form pads the new operator with spaces, so that it cannot merge
// with a neighbouring token, as < followed by - would.
func (e *enumerator) swapped(f *file, s *site, opPos token.Pos, op token.Token, to token.Token, pad bool) string {
	at := e.off(opPos)
	t := to.String()
	if pad {
		t = " " + t + " "
	}
	return string(f.src[s.start:at]) + t + string(f.src[at+len(op.String()):s.end])
}

// equality makes the mutants of == and !=: true and false. Every such
// comparison is a site, whatever its operands' types, because each of its
// kinds needs only the comparison's result. The walk skips a constant
// comparison.
func (e *enumerator) equality(f *file, n *ast.BinaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if !isPlainBool(tv.Type) {
		e.addSkip(f, n, spec.SkipNamedBool)
		return
	}
	s := e.newSite(f, n, scope)
	whole := e.text(f, n)
	s.add(spec.RORTrue, "true", "("+whole+" || true)")
	s.add(spec.RORFalse, "false", "("+whole+" && false)")
}

// ordered makes the mutants of <, <=, > and >=: the boundary, and false for
// < and > or true for <= and >=. The walk skips a constant comparison.
func (e *enumerator) ordered(f *file, n *ast.BinaryExpr, scope string) {
	info := e.pkg.info
	tv := info.Types[n]
	operand := operandType(info, n)
	switch {
	case !isPlainBool(tv.Type):
		e.addSkip(f, n, spec.SkipNamedBool)
		return
	case isTypeParam(operand):
		e.addSkip(f, n, spec.SkipTypeParameter)
		return
	case e.contextShift(operand, n):
		e.addSkip(f, n, spec.SkipContextShift)
		return
	}
	s := e.newSite(f, n, scope)
	whole := e.text(f, n)
	s.add(spec.RORBoundary, e.swapped(f, s, n.OpPos, n.Op, boundary[n.Op], false), e.swapped(f, s, n.OpPos, n.Op, boundary[n.Op], true))
	if n.Op == token.LEQ || n.Op == token.GEQ {
		s.add(spec.RORTrue, "true", "("+whole+" || true)")
	} else {
		s.add(spec.RORFalse, "false", "("+whole+" && false)")
	}
}

// arithmetic makes the mutant of +, -, *, / and % on numbers. The package
// type-checks, so both operands and the result have one type. A mutant that
// divides an integer by a constant 0 is not viable, because the compiler
// rejects the division. The walk skips constant arithmetic on numbers.
func (e *enumerator) arithmetic(f *file, n *ast.BinaryExpr, scope string) {
	info := e.pkg.info
	operand := operandType(info, n)
	if !isNumber(operand) {
		return
	}
	switch {
	case isTypeParam(operand):
		e.addSkip(f, n, spec.SkipTypeParameter)
		return
	case e.contextShift(operand, n):
		e.addSkip(f, n, spec.SkipContextShift)
		return
	}
	s := e.newSite(f, n, scope)
	m := s.add(spec.AOR, e.swapped(f, s, n.OpPos, n.Op, arith[n.Op], false), e.swapped(f, s, n.OpPos, n.Op, arith[n.Op], true))
	m.notViable = arith[n.Op] == token.QUO && zeroDivisor(info, operand, n.Y)
}

// zeroDivisor reports whether y is a constant whose value is 0 and t an
// integer type, so that a division by y is one that the compiler rejects.
func zeroDivisor(info *types.Info, t types.Type, y ast.Expr) bool {
	b, ok := t.Underlying().(*types.Basic)
	v := info.Types[y].Value
	return ok && b.Info()&types.IsInteger != 0 && v != nil && constant.Sign(v) == 0
}

// operandType returns the type of a binary expression's operands. The type
// checker records an untyped constant operand with the type it converts to,
// so the left operand's type is the type of both.
func operandType(info *types.Info, n *ast.BinaryExpr) types.Type {
	return info.TypeOf(n.X)
}

// contextShift reports whether an operand of n contains a non-constant shift
// of an untyped constant while the operands' type has no name that resolves
// in the package. Such a constant takes its type from the expression around
// it.
func (e *enumerator) contextShift(operand types.Type, n *ast.BinaryExpr) bool {
	return !e.resolves(operand) && (e.hasContextShift(n.X) || e.hasContextShift(n.Y))
}

// connector makes the mutants of && and ||: each operand alone, and false
// for && or true for ||. The form of a mutant keeps each operand that it
// does not evaluate behind a constant that skips it. Each operand of the
// site is recorded, so that no negation of it is made. The walk skips a
// constant connector.
func (e *enumerator) connector(f *file, n *ast.BinaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if !isPlainBool(tv.Type) {
		e.addSkip(f, n, spec.SkipNamedBool)
		return
	}
	e.operands[ast.Unparen(n.X)] = true
	e.operands[ast.Unparen(n.Y)] = true
	s := e.newSite(f, n, scope)
	a, b := e.text(f, n.X), e.text(f, n.Y)
	op, pa, pb := n.Op.String(), "("+a+")", "("+b+")"
	s.add(spec.LCRLeft, a, "("+pa+" || false && "+pb+")")
	s.add(spec.LCRRight, b, "(false && "+pa+" || "+pb+")")
	if n.Op == token.LOR {
		s.add(spec.LCRTrue, "true", "(true || "+pa+" "+op+" "+pb+")")
	} else {
		s.add(spec.LCRFalse, "false", "(false && ("+pa+" "+op+" "+pb+"))")
	}
}

// compound makes the mutant of +=, -=, *=, /= and %= on one number. A
// mutant that divides an integer by a constant 0 is not viable.
func (e *enumerator) compound(f *file, n *ast.AssignStmt, scope string) {
	to, ok := assign[n.Tok]
	if !ok {
		return
	}
	t := e.pkg.info.TypeOf(n.Lhs[0])
	if !isNumber(t) {
		return
	}
	switch {
	case isTypeParam(t):
		e.addSkip(f, n, spec.SkipTypeParameter)
		return
	case !sideEffectFree(n.Lhs[0]):
		e.addSkip(f, n, spec.SkipSideEffects)
		return
	}
	s := e.newSite(f, n, scope)
	m := s.add(spec.AOR, e.swapped(f, s, n.TokPos, n.Tok, to, false), e.swapped(f, s, n.TokPos, n.Tok, to, true))
	m.notViable = to == token.QUO_ASSIGN && zeroDivisor(e.pkg.info, t, n.Rhs[0])
}

// incdec makes the mutant of an increment or decrement statement in a
// statement list or in a for loop's post statement.
func (e *enumerator) incdec(f *file, n *ast.IncDecStmt, parent ast.Node, scope string) {
	t := e.pkg.info.TypeOf(n.X)
	if !isNumber(t) {
		return
	}
	switch p := parent.(type) {
	case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause, *ast.LabeledStmt:
	case *ast.ForStmt:
		if p.Post != n {
			return
		}
		switch {
		case isTypeParam(t):
			e.addSkip(f, n, spec.SkipTypeParameter)
			return
		case !sideEffectFree(n.X):
			e.addSkip(f, n, spec.SkipSideEffects)
			return
		}
	default:
		return
	}
	to := token.DEC
	if n.Tok == token.DEC {
		to = token.INC
	}
	s := e.newSite(f, n, scope)
	text := e.swapped(f, s, n.TokPos, n.Tok, to, false)
	s.add(spec.UOIIncDec, text, text)
}

// not negates a boolean operand: an identifier, a selector, a call, an
// index, a type assertion, a dereference, or a !x, whose mutant is x. An
// operand of a connector that is a site has no negation: the connector's
// mutants subsume it. outer is x with its parentheses, and around the node
// around them, so an operand in parentheses has the position of the operand
// without them.
func (e *enumerator) not(f *file, x, outer ast.Expr, around ast.Node, scope string) {
	if e.operands[x] {
		return
	}
	var operand ast.Expr
	switch n := x.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.TypeAssertExpr, *ast.StarExpr:
	case *ast.UnaryExpr:
		if n.Op != token.NOT {
			return
		}
		operand = n.X
	default:
		return
	}
	// The type checker records the source of a comma-ok assignment as a
	// tuple, so the bool test below excludes it.
	tv, ok := e.pkg.info.Types[x]
	if !ok || tv.Value != nil || !tv.IsValue() || !isPlainBool(tv.Type) {
		return
	}
	switch p := around.(type) {
	case *ast.AssignStmt:
		if contains(p.Lhs, outer) {
			return
		}
	case *ast.UnaryExpr:
		if p.Op == token.AND || p.Op == token.NOT {
			return
		}
	case *ast.KeyValueExpr:
		if p.Key == outer {
			return
		}
	case *ast.IncDecStmt, *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt, *ast.RangeStmt:
		return
	}
	s := e.newSite(f, x, scope)
	text := e.text(f, x)
	replacement := "!" + text
	if operand != nil {
		replacement = e.text(f, operand)
	}
	s.add(spec.UOINot, replacement, "("+text+" != true)")
}

// minus makes the mutant of a unary minus whose value is not a constant.
func (e *enumerator) minus(f *file, n *ast.UnaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if tv.Value != nil || !isNumber(tv.Type) {
		return
	}
	if isTypeParam(tv.Type) {
		e.addSkip(f, n, spec.SkipTypeParameter)
		return
	}
	s := e.newSite(f, n, scope)
	operand := e.text(f, n.X)
	s.add(spec.UOIMinus, operand, "("+operand+")")
}

// delete removes one statement of a statement list. It keeps declarations,
// branches, returns, increments and decrements, every statement that
// contains a label, and the list's final statement that is not empty when
// that statement is terminating.
func (e *enumerator) delete(f *file, st ast.Stmt, parent ast.Node, scope string) {
	var list []ast.Stmt
	switch p := parent.(type) {
	case *ast.BlockStmt:
		list = p.List
	case *ast.CaseClause:
		list = p.Body
	case *ast.CommClause:
		list = p.Body
	default:
		return
	}
	if !containsStmt(list, st) {
		return
	}
	switch s := st.(type) {
	case *ast.ExprStmt, *ast.SendStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
		*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.BlockStmt, *ast.DeferStmt, *ast.GoStmt:
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE || allBlank(s.Lhs) {
			return
		}
	default:
		return
	}
	if hasLabel(st) || final(list) == st && e.isTerminating(st) {
		return
	}
	s := e.newSite(f, st, scope)
	s.stmt = st
	s.add(spec.SBRDelete, "", "if false {\n"+e.text(f, st)+"\n}")
}

// zero makes the mutant that returns the zero value of every result type
// before a return statement whose results are not all zero already. The
// mutant does not evaluate the results, and its form keeps the original
// return after the new one, where it never runs.
func (e *enumerator) zero(f *file, n *ast.ReturnStmt, fn *ast.FuncType, scope string) {
	if fn == nil || fn.Results == nil || len(n.Results) == 0 {
		return
	}
	var zeros []string
	var dests []types.Type
	for _, field := range fn.Results.List {
		zero, dest := e.zeroOf(f, field.Type), e.pkg.info.TypeOf(field.Type)
		for range max(len(field.Names), 1) {
			zeros, dests = append(zeros, zero), append(dests, dest)
		}
	}
	if e.allZero(n.Results, dests) {
		return
	}
	ret := "return " + strings.Join(zeros, ", ")
	s := e.newSite(f, n, scope)
	s.add(spec.SBRZero, ret, "if true {\n"+ret+"\n}\n"+e.text(f, n))
}

// callee returns the object that call calls, as the type checker resolves
// the call's function without its parentheses and its type arguments: a
// function, a method, a builtin or a variable. It returns nil for a function
// that is no name, such as a function literal.
func (e *enumerator) callee(call *ast.CallExpr) types.Object {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		fun = x.X
	case *ast.IndexListExpr:
		fun = x.X
	}
	switch x := fun.(type) {
	case *ast.Ident:
		return e.pkg.info.Uses[x]
	case *ast.SelectorExpr:
		return e.pkg.info.Uses[x.Sel]
	}
	return nil
}

// apiName returns the name of obj as the overlay spells an API: a function's
// or a method's full name, or a builtin's name. It returns "" for any other
// object, such as a variable.
func apiName(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Func:
		return o.FullName()
	case *types.Builtin:
		return o.Name()
	}
	return ""
}

// family records what a call to an API of a rule family suppresses. The
// type checker resolves the API: a function or method by its full name, a
// method by a method rule, a variable by a result rule, and the builtin make
// by its name and the allocated type. Only a local variable that an
// identifier names can match a result rule.
func (e *enumerator) family(f *file, call *ast.CallExpr) {
	obj := e.callee(call)
	name := apiName(obj)
	family, ok := e.families[name]
	if fn, isFunc := obj.(*types.Func); !ok && isFunc {
		family, ok = e.method(fn)
	}
	if v, isVar := obj.(*types.Var); !ok && isVar {
		family, ok = e.results[v]
	}
	if ok {
		e.calls[call] = family
		e.suppressions = append(e.suppressions, suppression{f: f, start: e.off(call.Pos()), end: e.off(call.End()), family: family, call: call})
		return
	}
	info := e.pkg.info
	// A call of a method expression passes the receiver first, so an
	// argument rule's parameter is one argument later.
	receiver := 0
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && info.Selections[sel] != nil &&
		info.Selections[sel].Kind() == types.MethodExpr {
		receiver = 1
	}
	for family, rules := range e.ov.Families {
		for _, a := range rules.Arguments {
			at := a.Argument + receiver
			if a.Func != name || at >= len(call.Args) {
				continue
			}
			if name == spec.Make && allocated(info.TypeOf(call.Args[0])) != a.Of {
				continue
			}
			arg := call.Args[at]
			e.suppressions = append(e.suppressions, suppression{f: f, start: e.off(arg.Pos()), end: e.off(arg.End()), family: family})
		}
	}
}

// method returns the family of a method rule of the overlay that fn
// matches: a method of an interface with the rule's name and signature. The
// signature is the method's type as go/types writes it, without the
// receiver and without parameter names.
func (e *enumerator) method(fn *types.Func) (string, bool) {
	sig, _ := fn.Type().(*types.Signature)
	if sig == nil || sig.Recv() == nil {
		return "", false
	}
	if _, onInterface := sig.Recv().Type().Underlying().(*types.Interface); !onInterface {
		return "", false
	}
	bare := types.NewSignatureType(nil, nil, nil, unnamed(sig.Params()), unnamed(sig.Results()), sig.Variadic())
	for family, rules := range e.ov.Families {
		for _, m := range rules.Methods {
			if m.On == spec.OnInterface && m.Name == fn.Name() && m.Signature == bare.String() {
				return family, true
			}
		}
	}
	return "", false
}

// resultVariables returns, by variable, the family of each variable whose
// calls a result rule of the overlay puts into the family: a variable
// declared inside a function's body that an assignment gives the result of
// the rule's type of a call of the family's API, that every other assignment
// gives such a result of the same family, and whose address no expression
// takes. A declaration without values assigns nothing.
func (e *enumerator) resultVariables() map[*types.Var]string {
	info := e.pkg.info
	// result returns the family of a rule that the result at index k of
	// call matches, or "". The package type-checks, so the called function
	// of a listed API has a signature with a result at index k.
	result := func(call *ast.CallExpr, k int) string {
		family := ""
		if fn, isFunc := e.callee(call).(*types.Func); isFunc {
			family = e.families[fn.FullName()]
		}
		if family == "" {
			return ""
		}
		results := info.TypeOf(call.Fun).(*types.Signature).Results()
		named := types.TypeString(results.At(k).Type(), nil)
		for _, r := range e.ov.Families[family].Results {
			if r.Type == named {
				return family
			}
		}
		return ""
	}
	families := map[*types.Var]string{}
	other := map[*types.Var]bool{}
	// assign notes that x, when it is a variable, gets a value: a result of
	// family, or any other value when family is "".
	assign := func(x ast.Expr, family string) {
		id, ok := ast.Unparen(x).(*ast.Ident)
		if !ok {
			return
		}
		v, ok := info.ObjectOf(id).(*types.Var)
		switch {
		case !ok:
		case family == "" || families[v] != "" && families[v] != family:
			other[v] = true
		default:
			families[v] = family
		}
	}
	// values notes the assignment of values to targets: the results of one
	// call, or one value per target.
	values := func(targets, values []ast.Expr) {
		for k, x := range targets {
			value, at := values[0], k
			if len(values) == len(targets) {
				value, at = values[k], 0
			}
			family := ""
			if call, ok := ast.Unparen(value).(*ast.CallExpr); ok {
				family = result(call, at)
			}
			assign(x, family)
		}
	}
	params := map[types.Object]bool{}
	declare := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				params[info.Defs[name]] = true
			}
		}
	}
	for _, f := range e.pkg.files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				declare(x.Recv)
			case *ast.FuncType:
				declare(x.Params)
				declare(x.Results)
			case *ast.AssignStmt:
				if x.Tok == token.ASSIGN || x.Tok == token.DEFINE {
					values(x.Lhs, x.Rhs)
				} else {
					assign(x.Lhs[0], "")
				}
			case *ast.ValueSpec:
				if len(x.Values) > 0 {
					names := make([]ast.Expr, len(x.Names))
					for i, name := range x.Names {
						names[i] = name
					}
					values(names, x.Values)
				}
			case *ast.RangeStmt:
				for _, target := range []ast.Expr{x.Key, x.Value} {
					if target != nil {
						assign(target, "")
					}
				}
			case *ast.UnaryExpr:
				if x.Op == token.AND {
					assign(x.X, "")
				}
			}
			return true
		})
	}
	for v := range families {
		if other[v] || params[v] || v.Parent() == e.pkg.types.Scope() {
			delete(families, v)
		}
	}
	return families
}

// unwrittenVariables returns each unwritten variable of the package, as
// sbr-zero's rule states it: a variable that a variable declaration without
// values declares inside a function's body, and that no use writes. A use
// writes the variable when the variable, alone or under selectors, indexes,
// dereferences and parentheses, is the target of an assignment, an
// increment, a decrement or a range clause, the operand of & or of a slice
// expression, or the receiver of a method with a pointer receiver.
func (e *enumerator) unwrittenVariables() map[*types.Var]bool {
	info := e.pkg.info
	declared := map[*types.Var]bool{}
	written := map[types.Object]bool{}
	// write notes a write of the variable that x is, alone or under
	// selectors, indexes, dereferences and parentheses.
	write := func(x ast.Expr) {
		for {
			switch n := x.(type) {
			case *ast.ParenExpr:
				x = n.X
			case *ast.SelectorExpr:
				x = n.X
			case *ast.IndexExpr:
				x = n.X
			case *ast.StarExpr:
				x = n.X
			case *ast.Ident:
				written[info.ObjectOf(n)] = true
				return
			default:
				return
			}
		}
	}
	for _, f := range e.pkg.files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.ValueSpec:
				for _, name := range x.Names {
					if v, ok := info.Defs[name].(*types.Var); ok && len(x.Values) == 0 && v.Parent() != e.pkg.types.Scope() {
						declared[v] = true
					}
				}
			case *ast.AssignStmt:
				for _, target := range x.Lhs {
					write(target)
				}
			case *ast.IncDecStmt:
				write(x.X)
			case *ast.RangeStmt:
				for _, target := range []ast.Expr{x.Key, x.Value} {
					if target != nil {
						write(target)
					}
				}
			case *ast.UnaryExpr:
				if x.Op == token.AND {
					write(x.X)
				}
			case *ast.SliceExpr:
				write(x.X)
			case *ast.SelectorExpr:
				if sel := info.Selections[x]; sel != nil && sel.Kind() == types.MethodVal {
					if _, pointer := sel.Obj().Type().(*types.Signature).Recv().Type().(*types.Pointer); pointer {
						write(x.X)
					}
				}
			}
			return true
		})
	}
	for v := range declared {
		if written[v] {
			delete(declared, v)
		}
	}
	return declared
}

// unnamed returns the types of t's variables without their names.
func unnamed(t *types.Tuple) *types.Tuple {
	vars := make([]*types.Var, t.Len())
	for i := range vars {
		vars[i] = types.NewParam(token.NoPos, nil, "", t.At(i).Type())
	}
	return types.NewTuple(vars...)
}

// allocated names the kind of collection that make allocates for t, as an
// argument rule names it, or "" for a channel, which no rule names.
func allocated(t types.Type) string {
	switch t.Underlying().(type) {
	case *types.Slice:
		return spec.OfSlice
	case *types.Map:
		return spec.OfMap
	}
	return ""
}

// parseAnnotations reads the file's dokimi:mutate-skip comments. A comment
// on a line of its own covers the next line, and one after code covers its
// own line.
func (e *enumerator) parseAnnotations(f *file) {
	directive := e.ov.Comment + e.cat.Annotation
	for _, group := range f.ast.Comments {
		for _, c := range group.List {
			rest, ok := strings.CutPrefix(c.Text, directive)
			if !ok || rest != "" && rest[0] != ' ' {
				continue
			}
			pos := e.pkg.fset.Position(c.Pos())
			line := pos.Line
			if strings.TrimSpace(string(f.src[pos.Offset-(pos.Column-1):pos.Offset])) == "" {
				line++
			}
			list, reason, ok := strings.Cut(rest, ":")
			reason = strings.TrimSpace(reason)
			if !ok || reason == "" {
				e.errors = append(e.errors, spec.ErrorWithoutReason)
				continue
			}
			kinds := map[string]bool{}
			for _, k := range strings.Split(list, ",") {
				kinds[strings.TrimSpace(k)] = true
			}
			e.annotations = append(e.annotations, &annotation{f: f, line: line, kinds: kinds, reason: reason})
		}
	}
}

func (e *enumerator) result(lines []string) *Result {
	order := map[*file]int{}
	for i, f := range e.pkg.files {
		order[f] = i
	}
	sort.SliceStable(e.sites, func(i, j int) bool {
		a, b := e.sites[i], e.sites[j]
		if a.f != b.f {
			return order[a.f] < order[b.f]
		}
		if a.start != b.start {
			return a.start < b.start
		}
		return a.end > b.end
	})
	rank := map[string]int{}
	for i, k := range e.cat.Kinds {
		rank[k.ID] = i
	}
	var all []*mutant
	for _, s := range e.sites {
		sort.SliceStable(s.mutants, func(i, j int) bool { return rank[s.mutants[i].kind] < rank[s.mutants[j].kind] })
		all = append(all, s.mutants...)
	}
	occurrences := map[string]int{}
	nths := map[string]int{}
	for _, m := range all {
		s := m.site
		tokens := Tokens(s.f.src[s.start:s.end])
		group := strings.Join([]string{s.f.name, s.scope, m.kind, tokens}, "\x00")
		m.occurrence = occurrences[group]
		occurrences[group]++
		m.nth = nths[s.scope+"\x00"+m.kind]
		nths[s.scope+"\x00"+m.kind]++
		m.key = Key(e.cat.Key, s.f.name, s.scope, m.kind, tokens, m.occurrence)
		e.suppress(m)
	}
	for _, a := range e.annotations {
		if !a.used {
			e.errors = append(e.errors, spec.ErrorStale)
		}
	}
	r := &Result{Forms: map[string]Form{}, Static: map[string]string{}}
	r.Expect = spec.Expect{Language: e.ov.Language, Lines: lines, Mutants: []spec.ExpectMutant{}, Skipped: []spec.Skip{}, Errors: dedupe(e.errors)}
	for _, m := range all {
		s := m.site
		start, end := e.pos(s.f, s.start), e.pos(s.f, s.end)
		// A mutant outside the selection is not-selected, whatever else
		// excludes it, and states no rule, reason or rejection.
		selected := spec.Selected(lines, s.f.name, start.Line)
		rule, reason, notViable := m.rule, m.reason, m.notViable
		if !selected {
			rule, reason, notViable = "", "", false
		}
		original, replacement := Cut(string(s.f.src[s.start:s.end]), m.replacement)
		r.Expect.Mutants = append(r.Expect.Mutants, spec.ExpectMutant{
			Scope: s.scope, Kind: m.kind, Nth: m.nth, Occurrence: m.occurrence, Key: m.key, File: s.f.name,
			Start: start, End: end, Original: original, Replacement: replacement,
			Rule: rule, Reason: reason, NotViable: notViable,
		})
		r.Forms[m.key] = Form{File: s.f.name, Start: s.start, End: s.end, Text: m.form}
		switch {
		case !selected:
			r.Static[m.key] = spec.NotSelected
		case rule != "" || reason != "":
			r.Static[m.key] = spec.Suppressed
		case notViable:
			r.Static[m.key] = spec.NotViable
		}
	}
	sort.SliceStable(e.skips, func(i, j int) bool {
		a, b := e.skips[i], e.skips[j]
		if a.f != b.f {
			return order[a.f] < order[b.f]
		}
		return a.start < b.start
	})
	for _, sk := range e.skips {
		r.Expect.Skipped = append(r.Expect.Skipped, spec.Skip{File: sk.f.name, Start: e.pos(sk.f, sk.start), End: e.pos(sk.f, sk.end), Reason: sk.reason})
	}
	return r
}

// suppress applies the rule families, then the annotations, to m.
func (e *enumerator) suppress(m *mutant) {
	s := m.site
	for _, sup := range e.suppressions {
		if sup.f != s.f {
			continue
		}
		inside := sup.start <= s.start && s.end <= sup.end
		if sup.call != nil {
			args := e.off(sup.call.Lparen) < s.start && s.end <= e.off(sup.call.Rparen)
			whole := s.start == sup.start && s.end == sup.end
			inside = args || whole || s.stmt != nil && callOf(s.stmt) == sup.call
		}
		if inside {
			m.rule = sup.family
			return
		}
	}
	line := e.pos(s.f, s.start).Line
	for _, a := range e.annotations {
		if a.f == s.f && a.line == line && (a.kinds[m.kind] || a.kinds[e.classes[m.kind]] || a.kinds[e.cat.Every]) {
			m.reason = a.reason
			a.used = true
			return
		}
	}
}

// callOf returns the call that an expression, defer or go statement makes.
func callOf(st ast.Stmt) *ast.CallExpr {
	switch s := st.(type) {
	case *ast.ExprStmt:
		call, _ := ast.Unparen(s.X).(*ast.CallExpr)
		return call
	case *ast.DeferStmt:
		return s.Call
	case *ast.GoStmt:
		return s.Call
	}
	return nil
}

func (e *enumerator) pos(f *file, offset int) spec.Position {
	p := f.tf.PositionFor(f.tf.Pos(offset), false)
	return spec.Position{Line: p.Line, Column: p.Column}
}

// Key returns a mutant's key: the first 16 hexadecimal digits of the
// SHA-256 digest of the prefix, the file, the scope, the kind, the tokens
// and the occurrence in decimal, separated by NUL bytes.
func Key(prefix, file, scope, kind, tokens string, occurrence int) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{prefix, file, scope, kind, tokens, strconv.Itoa(occurrence)}, keySeparator)))
	return hex.EncodeToString(sum[:])[:keyDigits]
}

// keySeparator separates the fields of a key's digest, and keyDigits is the
// number of the digest's hexadecimal digits that the key keeps.
const (
	keySeparator = "\x00"
	keyDigits    = 16
)

// Tokens returns the tokens of src joined by single spaces. Comments and
// the semicolons that the scanner inserts at line ends are left out. An
// operator is written as Go spells it, and any other token as its text.
func Tokens(src []byte) string {
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", fset.Base(), len(src)), src, nil, 0)
	var out []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		if lit == "" {
			lit = tok.String()
		}
		out = append(out, lit)
	}
	return strings.Join(out, " ")
}

// Cut writes a mutant's original and replacement as a record states them:
// each run of whitespace replaced by one space, and at most 120 characters
// each. When either is longer, both keep the same window, which starts 40
// characters before the first character where they differ, or at the first
// character when fewer precede it. An ellipsis replaces the characters
// before the window, and another the characters after it.
func Cut(original, replacement string) (string, string) {
	o := []rune(strings.Join(strings.Fields(original), " "))
	r := []rune(strings.Join(strings.Fields(replacement), " "))
	if len(o) <= maxText && len(r) <= maxText {
		return string(o), string(r)
	}
	same := 0
	for same < len(o) && same < len(r) && o[same] == r[same] {
		same++
	}
	start := max(0, same-lead)
	return window(o, start), window(r, start)
}

// The limits of a mutant's original and replacement as a record states
// them: at most maxText characters each, with the window of a cut field
// starting lead characters before the first difference, and an ellipsis in
// place of the characters that the window leaves out.
const (
	maxText  = 120
	lead     = 40
	ellipsis = "…"
)

// window returns the characters of text from start, with an ellipsis in
// place of the characters before start, and of those past maxText
// characters.
func window(text []rune, start int) string {
	head, room := "", maxText
	if start > 0 {
		head, room = ellipsis, maxText-1
	}
	if len(text)-start <= room {
		return head + string(text[start:])
	}
	return head + string(text[start:start+room-1]) + ellipsis
}

func dedupe(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []ast.Expr, x ast.Expr) bool {
	for _, e := range list {
		if e == x {
			return true
		}
	}
	return false
}

func containsStmt(list []ast.Stmt, st ast.Stmt) bool {
	for _, s := range list {
		if s == st {
			return true
		}
	}
	return false
}

// zeroOf returns the zero value of the type that the expression t writes,
// as Go code writes it: 0, "", false, nil, the type followed by {} for a
// struct or an array, and *new(T) for a type parameter T.
func (e *enumerator) zeroOf(f *file, t ast.Expr) string {
	typ := e.pkg.info.TypeOf(t)
	text := e.text(f, ast.Unparen(t))
	if isTypeParam(typ) {
		return "*new(" + text + ")"
	}
	switch u := typ.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsNumeric != 0:
			return "0"
		case u.Info()&types.IsString != 0:
			return `""`
		case u.Info()&types.IsBoolean != 0:
			return "false"
		}
	case *types.Struct, *types.Array:
		return text + "{}"
	}
	return "nil"
}

// allZero reports whether every result is the zero value of its result
// type, of the types in order. A return of the results of one call has
// fewer results than types, and is no zero value.
func (e *enumerator) allZero(results []ast.Expr, dests []types.Type) bool {
	if len(results) != len(dests) {
		return false
	}
	for i, x := range results {
		if !e.isZero(x, dests[i]) {
			return false
		}
	}
	return true
}

// isInterface reports whether t is an interface type and no type parameter,
// whose type set can contain types that are no interface.
func isInterface(t types.Type) bool {
	_, ok := t.Underlying().(*types.Interface)
	return ok && !isTypeParam(t)
}

// isZero reports whether x is the zero value of dest, the type of the
// result or the element that x gives: nil, a constant whose value is 0, ""
// or false, *new(T) of a type T, an unwritten variable, or a composite
// literal of a struct or an array type whose every element is the zero
// value of its own type. A value whose type is no interface gives an
// interface that is not nil, so for an interface type dest only nil and a
// zero value of an interface type are the zero value. *new(x) of an
// expression x is the value of x.
func (e *enumerator) isZero(x ast.Expr, dest types.Type) bool {
	info := e.pkg.info
	x = ast.Unparen(x)
	tv := info.Types[x]
	if tv.IsNil() {
		return true
	}
	if isInterface(dest) && !isInterface(tv.Type) {
		return false
	}
	if v := tv.Value; v != nil {
		switch v.Kind() {
		case constant.Bool:
			return !constant.BoolVal(v)
		case constant.String:
			return constant.StringVal(v) == ""
		}
		// The other constants are numbers.
		return constant.Sign(v) == 0
	}
	switch n := x.(type) {
	case *ast.Ident:
		v, ok := info.Uses[n].(*types.Var)
		return ok && e.unwritten[v]
	case *ast.StarExpr:
		call, ok := ast.Unparen(n.X).(*ast.CallExpr)
		if !ok {
			return false
		}
		b, ok := e.callee(call).(*types.Builtin)
		return ok && b.Name() == builtinNew && info.Types[call.Args[0]].IsType()
	case *ast.CompositeLit:
		// elem returns the type of the element at index i, whose key is key
		// or nil.
		var elem func(i int, key ast.Expr) types.Type
		switch u := tv.Type.Underlying().(type) {
		case *types.Struct:
			// A keyed element names its field, which the type checker
			// resolves.
			elem = func(i int, key ast.Expr) types.Type {
				if key != nil {
					return info.Uses[key.(*ast.Ident)].Type()
				}
				return u.Field(i).Type()
			}
		case *types.Array:
			elem = func(int, ast.Expr) types.Type { return u.Elem() }
		default:
			return false
		}
		for i, elt := range n.Elts {
			var key ast.Expr
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				key, elt = kv.Key, kv.Value
			}
			if !e.isZero(elt, elem(i, key)) {
				return false
			}
		}
		return true
	}
	return false
}

// isTerminating reports whether s is a terminating statement in the sense
// of the Go specification, by the rule that go/types applies, for a
// statement that contains no labelled statement. delete keeps every
// statement that contains a label, so a break with a label never decides
// a deletion.
func (e *enumerator) isTerminating(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.GOTO || s.Tok == token.FALLTHROUGH
	case *ast.ExprStmt:
		call, ok := ast.Unparen(s.X).(*ast.CallExpr)
		if !ok {
			return false
		}
		b, ok := e.callee(call).(*types.Builtin)
		return ok && b.Name() == builtinPanic
	case *ast.BlockStmt:
		return e.terminates(s.List)
	case *ast.IfStmt:
		return s.Else != nil && e.isTerminating(s.Body) && e.isTerminating(s.Else)
	case *ast.ForStmt:
		return s.Cond == nil && !breaks(s.Body)
	case *ast.SwitchStmt:
		return hasDefault(s.Body) && e.clausesTerminate(s.Body)
	case *ast.TypeSwitchStmt:
		return hasDefault(s.Body) && e.clausesTerminate(s.Body)
	case *ast.SelectStmt:
		return e.clausesTerminate(s.Body)
	}
	return false
}

// terminates reports whether a statement list ends in a terminating
// statement: its final statement that is not empty is one.
func (e *enumerator) terminates(list []ast.Stmt) bool {
	last := final(list)
	return last != nil && e.isTerminating(last)
}

// clausesTerminate reports whether the statement list of every clause of a
// switch, a type switch or a select statement ends in a terminating
// statement and contains no break of the statement.
func (e *enumerator) clausesTerminate(body *ast.BlockStmt) bool {
	for _, c := range body.List {
		var list []ast.Stmt
		switch c := c.(type) {
		case *ast.CaseClause:
			list = c.Body
		case *ast.CommClause:
			list = c.Body
		}
		if !e.terminates(list) || slices.ContainsFunc(list, breaks) {
			return false
		}
	}
	return true
}

// final returns the last statement of list that is not empty, or nil when
// list has none.
func final(list []ast.Stmt) ast.Stmt {
	for i := len(list) - 1; i >= 0; i-- {
		if _, empty := list[i].(*ast.EmptyStmt); !empty {
			return list[i]
		}
	}
	return nil
}

// hasDefault reports whether the body of a switch or a type switch
// statement has a default clause.
func hasDefault(body *ast.BlockStmt) bool {
	return slices.ContainsFunc(body.List, func(c ast.Stmt) bool { return c.(*ast.CaseClause).List == nil })
}

// breaks reports whether s is or contains a break statement without a label
// that ends the closest for, switch or select statement around s. A break
// inside a nested for, range, switch, type switch or select statement ends
// that statement.
func breaks(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BranchStmt:
		return s.Tok == token.BREAK && s.Label == nil
	case *ast.BlockStmt:
		return slices.ContainsFunc(s.List, breaks)
	case *ast.IfStmt:
		return breaks(s.Body) || s.Else != nil && breaks(s.Else)
	}
	return false
}

func hasLabel(s ast.Stmt) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		_, ok := n.(*ast.LabeledStmt)
		found = found || ok
		return !found
	})
	return found
}

func allBlank(es []ast.Expr) bool {
	for _, e := range es {
		if id, ok := e.(*ast.Ident); !ok || id.Name != "_" {
			return false
		}
	}
	return true
}

func isTypeParam(t types.Type) bool {
	_, ok := types.Unalias(t).(*types.TypeParam)
	return ok
}

// isNumber reports whether t is an integer or floating-point type, or a
// type parameter whose every type is one. A type parameter is one when
// types.Satisfies reports that it satisfies numbers. go/types computes the
// parameter's type set through embedded constraints, unions and
// intersections.
func isNumber(t types.Type) bool {
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		return types.Satisfies(tp, numbers)
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsInteger|types.IsFloat) != 0
}

// numbers is the constraint whose type set is every type whose underlying
// type is a typed integer or floating-point type. It is complete, so
// concurrent enumerations only read it.
var numbers = func() *types.Interface {
	var terms []*types.Term
	for _, b := range types.Typ {
		if b.Info()&types.IsUntyped == 0 && b.Info()&(types.IsInteger|types.IsFloat) != 0 {
			terms = append(terms, types.NewTerm(true, b))
		}
	}
	return types.NewInterfaceType(nil, []types.Type{types.NewUnion(terms)}).Complete()
}()

// isPlainBool reports whether t is bool or an untyped boolean.
func isPlainBool(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && (b.Kind() == types.Bool || b.Kind() == types.UntypedBool)
}

// resolves reports whether t has a name that resolves in every file of the
// package without a new import: a predeclared type, or a type without type
// parameters that the package declares at package level. t is the type of
// the operands of arithmetic or of an ordered comparison, which is a typed
// basic type or a named type.
func (e *enumerator) resolves(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return true
	}
	obj := n.Obj()
	return obj.Pkg() == e.pkg.types && n.TypeArgs().Len() == 0 && obj.Parent() == e.pkg.types.Scope()
}

// hasContextShift reports whether x contains a non-constant shift whose
// left operand is an untyped constant.
func (e *enumerator) hasContextShift(x ast.Expr) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if ok && (b.Op == token.SHL || b.Op == token.SHR) && e.pkg.info.Types[b].Value == nil && e.untypedConst(b.X) {
			found = true
		}
		return !found
	})
	return found
}

// untypedConst reports whether x is an untyped constant expression. The
// package's type information records the type that such a constant
// converts to, so types.CheckExpr checks x again alone, at its own
// position, where x keeps its untyped type. x type-checks in its package,
// so it type-checks alone, and CheckExpr returns nil.
func (e *enumerator) untypedConst(x ast.Expr) bool {
	if e.pkg.info.Types[x].Value == nil {
		return false
	}
	alone := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	_ = types.CheckExpr(e.pkg.fset, e.pkg.types, x.Pos(), x, alone)
	b, ok := alone.Types[x].Type.(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}

func sideEffectFree(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return sideEffectFree(v.X)
	}
	return false
}
