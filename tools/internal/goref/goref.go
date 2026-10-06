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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mutate-spec/tools/internal/spec"
)

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

// Enumerate reads the Go package in dir and returns its mutants under the
// catalogue and the Go overlay, with lines as the run's selection.
func Enumerate(dir string, cat spec.Catalogue, ov spec.Overlay, lines []string) (*Result, error) {
	pkg, err := load(dir, ov.Comment+cat.Include)
	if err != nil {
		return nil, err
	}
	e := &enumerator{
		pkg: pkg, cat: cat, ov: ov, families: callFamilies(ov), classes: classes(cat),
		calls: map[*ast.CallExpr]string{}, operands: map[ast.Expr]bool{},
	}
	e.results = e.resultVariables()
	for _, f := range pkg.files {
		if !f.generated {
			e.file(f)
		}
	}
	r := e.result(lines)
	r.Expect.Generated = e.generated()
	return r, nil
}

// generated returns each generated file of the package in file order, with
// the number of mutants that the kinds make at its sites. A second
// enumerator walks the files, so the result contains none of their sites,
// annotations and suppressions.
func (e *enumerator) generated() []spec.Generated {
	g := &enumerator{
		pkg: e.pkg, cat: e.cat, ov: e.ov, families: e.families, classes: e.classes,
		calls: map[*ast.CallExpr]string{}, operands: map[ast.Expr]bool{}, results: e.results,
	}
	out := []spec.Generated{}
	for _, f := range e.pkg.files {
		if !f.generated {
			continue
		}
		before := len(g.sites)
		g.file(f)
		mutants := 0
		for _, s := range g.sites[before:] {
			mutants += len(s.mutants)
		}
		out = append(out, spec.Generated{File: f.name, Mutants: mutants})
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

// load parses and type-checks the files that the build compiles, without
// test files and without the files that build constraints exclude. A file
// is generated when go/ast.IsGenerated reports it and its header does not
// contain the line include.
func load(dir, include string) (*pkgInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	p := &pkgInfo{fset: token.NewFileSet()}
	var syntax []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		ok, err := build.Default.MatchFile(dir, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		af, err := parser.ParseFile(p.fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, imp := range af.Imports {
			if imp.Path.Value == `"C"` {
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
	p.info = &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(p.fset, "gc", nil), GoVersion: goVersion(dir)}
	if p.types, err = conf.Check("fixture", p.fset, syntax, p.info); err != nil {
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
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return "go" + strings.TrimSpace(v)
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

// callFamilies maps each API of a call family to the family.
func callFamilies(ov spec.Overlay) map[string]string {
	m := map[string]string{}
	for family, names := range ov.Families.Calls() {
		for _, name := range names {
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
	// result rule puts into a family to the family.
	calls    map[*ast.CallExpr]string
	operands map[ast.Expr]bool
	results  map[*types.Var]string
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
	info := e.pkg.info
	if e.calls[call] != "" || info.Types[call.Fun].IsType() {
		return true
	}
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	if _, builtin := info.Uses[id].(*types.Builtin); !builtin {
		return false
	}
	switch id.Name {
	case "len", "cap", "min", "max", "real", "imag", "complex":
		return true
	}
	return false
}

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
// the receiver's type in parentheses or not.
func scopeOf(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
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
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
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
		if lit, ok := n.(*ast.FuncLit); ok {
			inner = lit.Type
		}
		if call, ok := n.(*ast.CallExpr); ok {
			e.family(f, call)
		}
		if e.constantSite(n) {
			if constant == 0 {
				e.addSkip(f, n, "constant expression")
			}
			constant++
		}
		if constant == 0 {
			e.visit(f, n, parent, scope, inner)
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

func (e *enumerator) visit(f *file, n, parent ast.Node, scope string, fn *ast.FuncType) {
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
		e.not(f, x, parent, scope)
	case ast.Expr:
		e.not(f, x, parent, scope)
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
// kinds needs only the comparison's result.
func (e *enumerator) equality(f *file, n *ast.BinaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if tv.Value != nil || tv.Type == nil {
		return
	}
	if !isPlainBool(tv.Type) {
		e.addSkip(f, n, "named boolean result")
		return
	}
	s := e.newSite(f, n, scope)
	whole := e.text(f, n)
	s.add("ror-true", "true", "("+whole+" || true)")
	s.add("ror-false", "false", "("+whole+" && false)")
}

// ordered makes the mutants of <, <=, > and >=: the boundary, and false for
// < and > or true for <= and >=.
func (e *enumerator) ordered(f *file, n *ast.BinaryExpr, scope string) {
	info := e.pkg.info
	tv := info.Types[n]
	if tv.Value != nil || tv.Type == nil {
		return
	}
	operand := operandType(info, n)
	switch {
	case !isPlainBool(tv.Type):
		e.addSkip(f, n, "named boolean result")
		return
	case isTypeParam(operand):
		e.addSkip(f, n, "operand of type-parameter type")
		return
	case e.contextShift(operand, n):
		e.addSkip(f, n, "untyped constant in a non-constant shift")
		return
	}
	s := e.newSite(f, n, scope)
	whole := e.text(f, n)
	s.add("ror-boundary", e.swapped(f, s, n.OpPos, n.Op, boundary[n.Op], false), e.swapped(f, s, n.OpPos, n.Op, boundary[n.Op], true))
	if n.Op == token.LEQ || n.Op == token.GEQ {
		s.add("ror-true", "true", "("+whole+" || true)")
	} else {
		s.add("ror-false", "false", "("+whole+" && false)")
	}
}

// arithmetic makes the mutant of +, -, *, / and % on numbers whose operands
// and result have one type. A mutant that divides an integer by a constant
// 0 is not viable, because the compiler rejects the division.
func (e *enumerator) arithmetic(f *file, n *ast.BinaryExpr, scope string) {
	info := e.pkg.info
	tv := info.Types[n]
	if tv.Value != nil || tv.Type == nil {
		return
	}
	operand := operandType(info, n)
	if operand == nil || !isNumber(operand) || !types.Identical(tv.Type, operand) {
		return
	}
	switch {
	case isTypeParam(operand):
		e.addSkip(f, n, "operand of type-parameter type")
		return
	case e.contextShift(operand, n):
		e.addSkip(f, n, "untyped constant in a non-constant shift")
		return
	}
	s := e.newSite(f, n, scope)
	m := s.add("aor", e.swapped(f, s, n.OpPos, n.Op, arith[n.Op], false), e.swapped(f, s, n.OpPos, n.Op, arith[n.Op], true))
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
	if _, ok := e.typeName(operand); ok {
		return false
	}
	return hasContextShift(e.pkg.info, n.X) || hasContextShift(e.pkg.info, n.Y)
}

// connector makes the mutants of && and ||: each operand alone, and false
// for && or true for ||. The form of a mutant keeps each operand that it
// does not evaluate behind a constant that skips it. Each operand of the
// site is recorded, so that no negation of it is made.
func (e *enumerator) connector(f *file, n *ast.BinaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if tv.Value != nil {
		return
	}
	if !isPlainBool(tv.Type) {
		e.addSkip(f, n, "named boolean result")
		return
	}
	e.operands[ast.Unparen(n.X)] = true
	e.operands[ast.Unparen(n.Y)] = true
	s := e.newSite(f, n, scope)
	a, b := e.text(f, n.X), e.text(f, n.Y)
	op, pa, pb := n.Op.String(), "("+a+")", "("+b+")"
	s.add("lcr-left", a, "("+pa+" || false && "+pb+")")
	s.add("lcr-right", b, "(false && "+pa+" || "+pb+")")
	if n.Op == token.LOR {
		s.add("lcr-true", "true", "(true || "+pa+" "+op+" "+pb+")")
	} else {
		s.add("lcr-false", "false", "(false && ("+pa+" "+op+" "+pb+"))")
	}
}

// compound makes the mutant of +=, -=, *=, /= and %= on one number. A
// mutant that divides an integer by a constant 0 is not viable.
func (e *enumerator) compound(f *file, n *ast.AssignStmt, scope string) {
	to, ok := assign[n.Tok]
	if !ok || len(n.Lhs) != 1 {
		return
	}
	t := e.pkg.info.TypeOf(n.Lhs[0])
	if t == nil || !isNumber(t) {
		return
	}
	switch {
	case isTypeParam(t):
		e.addSkip(f, n, "operand of type-parameter type")
		return
	case !sideEffectFree(n.Lhs[0]):
		e.addSkip(f, n, "assignment target with side effects")
		return
	}
	s := e.newSite(f, n, scope)
	m := s.add("aor", e.swapped(f, s, n.TokPos, n.Tok, to, false), e.swapped(f, s, n.TokPos, n.Tok, to, true))
	m.notViable = to == token.QUO_ASSIGN && zeroDivisor(e.pkg.info, t, n.Rhs[0])
}

// incdec makes the mutant of an increment or decrement statement in a
// statement list or in a for loop's post statement.
func (e *enumerator) incdec(f *file, n *ast.IncDecStmt, parent ast.Node, scope string) {
	t := e.pkg.info.TypeOf(n.X)
	if t == nil || !isNumber(t) {
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
			e.addSkip(f, n, "operand of type-parameter type")
			return
		case !sideEffectFree(n.X):
			e.addSkip(f, n, "assignment target with side effects")
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
	s.add("uoi-incdec", text, text)
}

// not negates a boolean operand: an identifier, a selector, a call, an
// index, a type assertion, a dereference, or a !x, whose mutant is x. An
// operand of a connector that is a site has no negation: the connector's
// mutants subsume it.
func (e *enumerator) not(f *file, x ast.Expr, parent ast.Node, scope string) {
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
	switch p := parent.(type) {
	case *ast.AssignStmt:
		if contains(p.Lhs, x) {
			return
		}
	case *ast.UnaryExpr:
		if p.Op == token.AND || p.Op == token.NOT {
			return
		}
	case *ast.KeyValueExpr:
		if p.Key == x {
			return
		}
	case *ast.SelectorExpr:
		if ast.Expr(p.Sel) == x {
			return
		}
	case *ast.CallExpr:
		if p.Fun == x {
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
	s.add("uoi-not", replacement, "("+text+" != true)")
}

// minus makes the mutant of a unary minus whose value is not a constant.
func (e *enumerator) minus(f *file, n *ast.UnaryExpr, scope string) {
	tv := e.pkg.info.Types[n]
	if tv.Value != nil || tv.Type == nil || !isNumber(tv.Type) {
		return
	}
	if isTypeParam(tv.Type) {
		e.addSkip(f, n, "operand of type-parameter type")
		return
	}
	s := e.newSite(f, n, scope)
	operand := e.text(f, n.X)
	s.add("uoi-minus", operand, "("+operand+")")
}

// delete removes one statement of a statement list. It keeps declarations,
// labels, branches, returns, increments and decrements, and a last
// statement that terminates its list.
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
	if list[len(list)-1] == st && isTerminating(e.pkg.info, st) || hasLabel(st) {
		return
	}
	s := e.newSite(f, st, scope)
	s.stmt = st
	s.add("sbr-delete", "", "if false {\n"+e.text(f, st)+"\n}")
}

// zero makes the mutant that returns the zero value of every result type
// before a return statement whose results are not all zero already. The
// mutant does not evaluate the results, and its form keeps the original
// return after the new one, where it never runs.
func (e *enumerator) zero(f *file, n *ast.ReturnStmt, fn *ast.FuncType, scope string) {
	if fn == nil || fn.Results == nil || len(n.Results) == 0 || allZero(e.pkg.info, n.Results) {
		return
	}
	var zeros []string
	for _, field := range fn.Results.List {
		zero := e.zeroOf(f, field.Type)
		for range max(len(field.Names), 1) {
			zeros = append(zeros, zero)
		}
	}
	ret := "return " + strings.Join(zeros, ", ")
	s := e.newSite(f, n, scope)
	s.add("sbr-zero", ret, "if true {\n"+ret+"\n}\n"+e.text(f, n))
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
	for _, c := range e.ov.Families.Capacity {
		if c.Func != name || c.Argument >= len(call.Args) {
			continue
		}
		if name == "make" && allocated(info.TypeOf(call.Args[0])) != c.Of {
			continue
		}
		arg := call.Args[c.Argument]
		e.suppressions = append(e.suppressions, suppression{f: f, start: e.off(arg.Pos()), end: e.off(arg.End()), family: "capacity"})
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
	for _, m := range e.ov.Families.Methods {
		if m.On == "interface" && m.Name == fn.Name() && m.Signature == bare.String() {
			return m.Family, true
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
	rules := map[spec.Result]bool{}
	for _, r := range e.ov.Families.Results {
		rules[r] = true
	}
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
		if rules[spec.Result{Family: family, Type: types.TypeString(results.At(k).Type(), nil)}] {
			return family
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

// unnamed returns the types of t's variables without their names.
func unnamed(t *types.Tuple) *types.Tuple {
	vars := make([]*types.Var, t.Len())
	for i := range vars {
		vars[i] = types.NewParam(token.NoPos, nil, "", t.At(i).Type())
	}
	return types.NewTuple(vars...)
}

// allocated names the kind of collection that make allocates for t.
func allocated(t types.Type) string {
	if t == nil {
		return ""
	}
	switch t.Underlying().(type) {
	case *types.Slice:
		return "slice"
	case *types.Map:
		return "map"
	case *types.Chan:
		return "chan"
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
				e.errors = append(e.errors, "annotation-without-reason")
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
			e.errors = append(e.errors, "stale-annotation")
		}
	}
	r := &Result{Forms: map[string]Form{}, Static: map[string]string{}}
	r.Expect = spec.Expect{Language: "go", Lines: lines, Mutants: []spec.ExpectMutant{}, Skipped: []spec.Skip{}, Errors: dedupe(e.errors)}
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
			r.Static[m.key] = "not-selected"
		case rule != "" || reason != "":
			r.Static[m.key] = "suppressed"
		case notViable:
			r.Static[m.key] = "not-viable"
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
		if a.f == s.f && a.line == line && (a.kinds[m.kind] || a.kinds[e.classes[m.kind]] || a.kinds["all"]) {
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
	sum := sha256.Sum256([]byte(strings.Join([]string{prefix, file, scope, kind, tokens, strconv.Itoa(occurrence)}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

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
	if len(o) <= 120 && len(r) <= 120 {
		return string(o), string(r)
	}
	same := 0
	for same < len(o) && same < len(r) && o[same] == r[same] {
		same++
	}
	start := max(0, same-40)
	return window(o, start), window(r, start)
}

// window returns the characters of text from start, with an ellipsis in
// place of the characters before start, and of those past 120 characters.
func window(text []rune, start int) string {
	head, room := "", 120
	if start > 0 {
		head, room = "…", 119
	}
	if len(text)-start <= room {
		return head + string(text[start:])
	}
	return head + string(text[start:start+room-1]) + "…"
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

// allZero reports whether every result is the zero value of its type.
func allZero(info *types.Info, results []ast.Expr) bool {
	for _, e := range results {
		if !isZero(info, e) {
			return false
		}
	}
	return true
}

// isZero reports whether x is the zero value of its type: nil, a constant
// whose value is 0, "" or false, *new(T) of a type T, or a composite literal
// of a struct or an array type whose every element is such a value. *new(x)
// of an expression x is the value of x.
func isZero(info *types.Info, x ast.Expr) bool {
	x = ast.Unparen(x)
	tv := info.Types[x]
	if tv.IsNil() {
		return true
	}
	if v := tv.Value; v != nil {
		switch v.Kind() {
		case constant.Bool:
			return !constant.BoolVal(v)
		case constant.String:
			return constant.StringVal(v) == ""
		case constant.Int, constant.Float, constant.Complex:
			return constant.Sign(v) == 0
		}
		return false
	}
	switch n := x.(type) {
	case *ast.StarExpr:
		call, ok := ast.Unparen(n.X).(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return false
		}
		id, ok := ast.Unparen(call.Fun).(*ast.Ident)
		if !ok {
			return false
		}
		_, builtin := info.Uses[id].(*types.Builtin)
		return builtin && id.Name == "new" && info.Types[call.Args[0]].IsType()
	case *ast.CompositeLit:
		switch tv.Type.Underlying().(type) {
		case *types.Struct, *types.Array:
		default:
			return false
		}
		for _, elt := range n.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				elt = kv.Value
			}
			if !isZero(info, elt) {
				return false
			}
		}
		return true
	}
	return false
}

// isTerminating reports whether s is a terminating statement in the sense
// of the Go specification. It errs towards yes, which only keeps a
// statement from being deleted.
func isTerminating(info *types.Info, s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt:
		return s.Tok == token.GOTO
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		id, ok := ast.Unparen(call.Fun).(*ast.Ident)
		if !ok {
			return false
		}
		_, builtin := info.Uses[id].(*types.Builtin)
		return builtin && id.Name == "panic"
	case *ast.BlockStmt:
		return len(s.List) > 0 && isTerminating(info, s.List[len(s.List)-1])
	case *ast.IfStmt:
		return s.Else != nil && isTerminating(info, s.Body) && isTerminating(info, s.Else)
	case *ast.ForStmt:
		return s.Cond == nil
	case *ast.SwitchStmt:
		return clausesTerminate(info, s.Body)
	case *ast.TypeSwitchStmt:
		return clausesTerminate(info, s.Body)
	case *ast.SelectStmt:
		return clausesTerminate(info, s.Body)
	case *ast.LabeledStmt:
		return isTerminating(info, s.Stmt)
	}
	return false
}

func clausesTerminate(info *types.Info, body *ast.BlockStmt) bool {
	for _, c := range body.List {
		var list []ast.Stmt
		switch c := c.(type) {
		case *ast.CaseClause:
			list = c.Body
		case *ast.CommClause:
			list = c.Body
		}
		if len(list) == 0 {
			return false
		}
		last := list[len(list)-1]
		if b, ok := last.(*ast.BranchStmt); ok && b.Tok == token.FALLTHROUGH {
			continue
		}
		if !isTerminating(info, last) {
			return false
		}
	}
	return true
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
// type parameter whose every type is one.
func isNumber(t types.Type) bool {
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		iface, _ := tp.Underlying().(*types.Interface)
		if iface == nil || iface.NumEmbeddeds() == 0 {
			return false
		}
		for i := range iface.NumEmbeddeds() {
			u, ok := iface.EmbeddedType(i).(*types.Union)
			if !ok {
				return false
			}
			for j := range u.Len() {
				if !isNumber(u.Term(j).Type()) {
					return false
				}
			}
		}
		return true
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsInteger|types.IsFloat) != 0
}

// isPlainBool reports whether t is bool or an untyped boolean.
func isPlainBool(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && (b.Kind() == types.Bool || b.Kind() == types.UntypedBool)
}

func isUntyped(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}

// typeName returns a name for t that resolves in every file of the package
// without a new import: a predeclared type, or a type without type
// parameters that the package declares at package level.
func (e *enumerator) typeName(t types.Type) (string, bool) {
	switch x := types.Unalias(t).(type) {
	case *types.Basic:
		if x.Info()&types.IsUntyped != 0 || x.Kind() == types.UnsafePointer {
			return "", false
		}
		return x.Name(), true
	case *types.Named:
		obj := x.Obj()
		if obj.Pkg() == e.pkg.types && x.TypeArgs().Len() == 0 && obj.Parent() == e.pkg.types.Scope() {
			return obj.Name(), true
		}
	}
	return "", false
}

// hasContextShift reports whether x contains a non-constant shift whose
// left operand is an untyped constant.
func hasContextShift(info *types.Info, x ast.Expr) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if ok && (b.Op == token.SHL || b.Op == token.SHR) && info.Types[b].Value == nil && untypedConst(info, b.X) {
			found = true
		}
		return !found
	})
	return found
}

// untypedConst reports whether x is an untyped constant expression. The
// type checker records the type such a constant converts to, so the test
// reads the syntax: literals, untyped named constants, and operators on
// them.
func untypedConst(info *types.Info, x ast.Expr) bool {
	switch v := ast.Unparen(x).(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		c, ok := info.Uses[v].(*types.Const)
		return ok && isUntyped(c.Type())
	case *ast.SelectorExpr:
		c, ok := info.Uses[v.Sel].(*types.Const)
		return ok && isUntyped(c.Type())
	case *ast.UnaryExpr:
		return untypedConst(info, v.X)
	case *ast.BinaryExpr:
		return untypedConst(info, v.X) && untypedConst(info, v.Y)
	}
	return false
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
