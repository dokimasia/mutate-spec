// Package gomut is a research prototype of mutation testing for one Go
// package. It applies the five operators of Google's mutation testing
// service, adapted to Go:
//
//   - aor: arithmetic operator replacement, + - * / %
//   - ror: relational operator replacement, to the boundary, to the negation,
//     and to true and false
//   - lcr: logical connector replacement, to the other connector, to either
//     operand, and to true and false
//   - uoi: unary operator insertion, ++ and -- swapped, and a boolean
//     operand negated
//   - sbr: statement block removal, a statement deleted, and a return's
//     results replaced by zero values
//
// It checks the mutants in one of two ways: every mutant compiled into one
// test binary behind a runtime switch (mutant schemata), or one test build
// per mutant. Both read the package through go build's -overlay flag and
// never write to the package's files.
package gomut

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/token"
	"go/types"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// Mutant is one change at one source position. Kind names the operator and
// its variant, such as ror-boundary or sbr-delete.
type Mutant struct {
	ID   int
	Kind string
	File string
	Pos  token.Position
	From string
	To   string

	stillborn bool // go vet rejects the mutant, so neither mode runs it
}

// site is one AST node that one or more mutants change. Its byte range nests
// strictly inside or outside the range of every other site in the file.
type site struct {
	index      int
	kind       string // binary, incdec, lcr, not, delete, zero
	file       string
	pos        token.Pos
	start, end int
	children   []*site
	mutants    []Mutant

	op         token.Token // binary, incdec and lcr sites
	opOff      int
	x, y       ast.Expr
	constraint string // binary sites: num, int, ordered, comparable
	typeArg    string // explicit type argument for the helper, empty to infer
	forPost    bool

	zeros []string // zero sites: one zero-value expression per result
}

// Package is a loaded package with its mutants. Stillborn counts the mutants
// that go vet rejects, which keep their IDs but are not in Mutants.
type Package struct {
	Dir       string
	Name      string
	Mutants   []Mutant
	Stillborn int
	fset     *token.FileSet
	src      map[string][]byte
	roots    map[string][]*site
	sites    []*site
	byID     map[int]*site
	liftLang bool
	tpkg     *types.Package
}

// liftToGo118 adds go1.18 to the file's build constraint, folding in an
// existing //go:build line or legacy // +build lines.
func liftToGo118(src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	var exprs []constraint.Expr
	first := -1
	keep := lines[:0:0]
	for i, l := range lines {
		if strings.HasPrefix(l, "package ") {
			keep = append(keep, lines[i:]...)
			break
		}
		if constraint.IsGoBuild(l) || constraint.IsPlusBuild(l) {
			e, err := constraint.Parse(l)
			if err != nil {
				return nil, err
			}
			if constraint.IsGoBuild(l) {
				exprs = []constraint.Expr{e} // a //go:build line supersedes +build lines
			} else if len(exprs) == 0 || !hasGoBuild(lines) {
				exprs = append(exprs, e)
			}
			if first < 0 {
				first = len(keep)
			}
			continue
		}
		keep = append(keep, l)
	}
	var expr constraint.Expr = &constraint.TagExpr{Tag: "go1.18"}
	for _, e := range exprs {
		expr = &constraint.AndExpr{X: e, Y: expr}
	}
	line := "//go:build " + expr.String()
	if first < 0 {
		return []byte(line + "\n\n" + strings.Join(keep, "\n")), nil
	}
	keep = append(keep[:first], append([]string{line}, keep[first:]...)...)
	return []byte(strings.Join(keep, "\n")), nil
}

func hasGoBuild(lines []string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, "package ") {
			return false
		}
		if constraint.IsGoBuild(l) {
			return true
		}
	}
	return false
}

var boundary = map[token.Token]token.Token{token.LSS: token.LEQ, token.LEQ: token.LSS, token.GTR: token.GEQ, token.GEQ: token.GTR}
var negation = map[token.Token]token.Token{token.EQL: token.NEQ, token.NEQ: token.EQL, token.LSS: token.GEQ, token.GTR: token.LEQ, token.LEQ: token.GTR, token.GEQ: token.LSS}
var arith = map[token.Token]token.Token{token.ADD: token.SUB, token.SUB: token.ADD, token.MUL: token.QUO, token.QUO: token.MUL, token.REM: token.MUL}

// Load parses and type-checks the package in dir and lists its mutants. It
// resolves symbolic links first: the go command keys an overlay by the path it
// computes from its working directory, and a path through a link never matches.
func Load(dir string) (*Package, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return nil, err
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule, Dir: dir}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("load %s: %d packages", dir, len(pkgs))
	}
	if len(pkgs[0].Errors) > 0 {
		return nil, fmt.Errorf("load %s: %v", dir, pkgs[0].Errors)
	}
	lp := pkgs[0]
	p := &Package{Dir: dir, Name: lp.Name, fset: lp.Fset, src: map[string][]byte{}, roots: map[string][]*site{}, byID: map[int]*site{}, tpkg: lp.Types}
	// The helpers are generic. A module whose go line is older than 1.18
	// compiles its files without type parameters, so the generated files
	// raise their own language version with a build constraint. A module at
	// 1.22 or later must not get the constraint: it would lower the version
	// and change the semantics of loop variables.
	goVersion := ""
	if lp.Module != nil {
		goVersion = lp.Module.GoVersion
	}
	p.liftLang = goVersion == "" || version.Compare("go"+goVersion, "go1.18") < 0
	ops := operators(os.Getenv("GOMUT_OPERATORS"))
	id := 0
	for i, f := range lp.Syntax {
		name := lp.CompiledGoFiles[i]
		src, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		p.src[name] = src
		var funcs []*ast.FuncType
		var sites []*site
		astutil.Apply(f, func(c *astutil.Cursor) bool {
			switch n := c.Node().(type) {
			case *ast.FuncDecl:
				funcs = append(funcs, n.Type)
			case *ast.FuncLit:
				funcs = append(funcs, n.Type)
			}
			return true
		}, func(c *astutil.Cursor) bool {
			var fn *ast.FuncType
			if len(funcs) > 0 {
				fn = funcs[len(funcs)-1]
			}
			if s := p.siteFor(lp.TypesInfo, c, src, fn); s != nil && s.keep(ops) {
				s.file = name
				sites = append(sites, s)
			}
			switch c.Node().(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				funcs = funcs[:len(funcs)-1]
			}
			return true
		})
		roots, err := nest(sites)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		p.roots[name] = roots
		for _, s := range sites { // sorted by nest, which is source order
			s.index = len(p.sites)
			p.sites = append(p.sites, s)
			for j := range s.mutants {
				id++
				m := &s.mutants[j]
				m.ID, m.File, m.Pos = id, name, p.fset.Position(s.pos)
				if m.stillborn {
					p.Stillborn++
					continue
				}
				p.byID[id] = s
				p.Mutants = append(p.Mutants, *m)
			}
		}
	}
	return p, nil
}

// operators parses GOMUT_OPERATORS, a comma-separated list of kinds such as
// ror-boundary or of operator classes such as sbr. Empty selects every kind.
func operators(list string) map[string]bool {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	ops := map[string]bool{}
	for _, op := range strings.Split(list, ",") {
		ops[strings.TrimSpace(op)] = true
	}
	return ops
}

// keep drops the mutants whose kind ops excludes, and reports whether any
// remain. A logical connector keeps all five or none, because its helper
// addresses them by their offset from the first.
func (s *site) keep(ops map[string]bool) bool {
	if ops == nil {
		return true
	}
	allowed := func(kind string) bool { return ops[kind] || ops[strings.SplitN(kind, "-", 2)[0]] }
	if s.kind == "lcr" {
		for _, m := range s.mutants {
			if allowed(m.Kind) {
				return true
			}
		}
		return false
	}
	kept := s.mutants[:0]
	for _, m := range s.mutants {
		if allowed(m.Kind) {
			kept = append(kept, m)
		}
	}
	s.mutants = kept
	return len(kept) > 0
}

// nest sorts sites into source order and links each to the sites inside it.
func nest(sites []*site) ([]*site, error) {
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].start != sites[j].start {
			return sites[i].start < sites[j].start
		}
		return sites[i].end > sites[j].end
	})
	var roots, stack []*site
	for _, s := range sites {
		for len(stack) > 0 && stack[len(stack)-1].end <= s.start {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, s)
		} else {
			top := stack[len(stack)-1]
			if s.end > top.end || s.start == top.start && s.end == top.end {
				return nil, fmt.Errorf("sites at offsets %d-%d and %d-%d overlap", top.start, top.end, s.start, s.end)
			}
			top.children = append(top.children, s)
		}
		stack = append(stack, s)
	}
	return roots, nil
}

func (p *Package) off(pos token.Pos) int { return p.fset.Position(pos).Offset }

func (p *Package) newSite(kind string, n ast.Node, pos token.Pos) *site {
	return &site{kind: kind, pos: pos, start: p.off(n.Pos()), end: p.off(n.End())}
}

func (p *Package) siteFor(info *types.Info, c *astutil.Cursor, src []byte, fn *ast.FuncType) *site {
	switch n := c.Node().(type) {
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			return p.lcrSite(info, n)
		}
		return p.binarySite(info, n)
	case *ast.IncDecStmt:
		return p.incDecSite(info, c, n)
	case *ast.ReturnStmt:
		return p.zeroSite(info, n, src, fn)
	case ast.Stmt:
		return p.deleteSite(info, c, n, src)
	case ast.Expr:
		return p.notSite(info, c, n, src)
	}
	return nil
}

func (p *Package) binarySite(info *types.Info, n *ast.BinaryExpr) *site {
	tv := info.Types[n]
	if tv.Value != nil || tv.Type == nil {
		return nil // constant expressions cannot hold a runtime switch
	}
	tx, ty := info.TypeOf(n.X), info.TypeOf(n.Y)
	if tx == nil || ty == nil || isTypeParam(tx) || isTypeParam(ty) {
		return nil
	}
	operand := tx
	if isUntyped(tx) {
		operand = ty
	}
	if !isUntyped(tx) && !isUntyped(ty) && !types.Identical(tx, ty) {
		return nil // mixed interface and concrete operands do not infer one type argument
	}
	s := p.newSite("binary", n, n.OpPos)
	s.op, s.opOff, s.x, s.y = n.Op, p.off(n.OpPos), n.X, n.Y
	// An untyped constant in a non-constant shift takes its type from the
	// context, which a call argument does not supply. Name the type
	// explicitly, and skip the site when the type has no name here.
	if name, ok := p.typeName(operand); ok {
		s.typeArg = name
	} else if hasContextShift(info, n.X) || hasContextShift(info, n.Y) {
		return nil
	}
	op := n.Op.String()
	if to, ok := arith[n.Op]; ok {
		b, ok := operand.Underlying().(*types.Basic)
		if !ok || b.Info()&(types.IsInteger|types.IsFloat) == 0 || !types.Identical(tv.Type, operand) {
			return nil
		}
		s.constraint = "num"
		if n.Op == token.REM || b.Info()&types.IsInteger != 0 && n.Op == token.QUO {
			s.constraint = "int"
		}
		s.mutants = []Mutant{{Kind: "aor", From: op, To: to.String()}}
		return s
	}
	if _, ok := negation[n.Op]; !ok {
		return nil
	}
	if !isPlainBool(tv.Type) {
		return nil // a named boolean result type would not accept the helper's bool
	}
	if n.Op == token.EQL || n.Op == token.NEQ {
		if !types.Comparable(operand) || isUntyped(operand) {
			return nil
		}
		s.constraint = "comparable"
	} else {
		b, ok := operand.Underlying().(*types.Basic)
		if !ok || b.Info()&types.IsOrdered == 0 {
			return nil
		}
		s.constraint = "ordered"
	}
	if to, ok := boundary[n.Op]; ok {
		s.mutants = append(s.mutants, Mutant{Kind: "ror-boundary", From: op, To: to.String()})
	}
	s.mutants = append(s.mutants,
		Mutant{Kind: "ror-negation", From: op, To: negation[n.Op].String()},
		Mutant{Kind: "ror-true", From: op, To: "true"},
		Mutant{Kind: "ror-false", From: op, To: "false"})
	return s
}

// lcrSite mutates a && or || to the other connector, to each operand alone,
// and to true and false. The schemata form evaluates each operand at most
// once and keeps the short-circuit order, so a guard such as p != nil still
// protects its right operand.
func (p *Package) lcrSite(info *types.Info, n *ast.BinaryExpr) *site {
	tv := info.Types[n]
	if tv.Value != nil || !isPlainBool(tv.Type) || callsRecover(info, n) {
		return nil // recover returns nil when a closure calls it
	}
	op, other := n.Op.String(), "||"
	if n.Op == token.LOR {
		other = "&&"
	}
	s := p.newSite("lcr", n, n.OpPos)
	s.op, s.opOff, s.x, s.y = n.Op, p.off(n.OpPos), n.X, n.Y
	s.mutants = []Mutant{
		{Kind: "lcr-swap", From: op, To: other, stillborn: vetSuspect(info, n)},
		{Kind: "lcr-left", From: op, To: "left operand"},
		{Kind: "lcr-right", From: op, To: "right operand"},
		{Kind: "lcr-true", From: op, To: "true"},
		{Kind: "lcr-false", From: op, To: "false"},
	}
	return s
}

// vetSuspect reports whether swapping the connector of n writes the pattern
// that go vet's bools check rejects, and that go test therefore refuses to
// build: x != c1 || x != c2, or x == c1 && x == c2, with distinct constants.
func vetSuspect(info *types.Info, n *ast.BinaryExpr) bool {
	want := token.NEQ // && becomes ||, which vet flags over != comparisons
	if n.Op == token.LOR {
		want = token.EQL
	}
	a, okA := ast.Unparen(n.X).(*ast.BinaryExpr)
	b, okB := ast.Unparen(n.Y).(*ast.BinaryExpr)
	if !okA || !okB || a.Op != want || b.Op != want {
		return false
	}
	xa, ca := splitConst(info, a)
	xb, cb := splitConst(info, b)
	return ca != nil && cb != nil && xa == xb && !constant.Compare(ca, token.EQL, cb)
}

// splitConst returns the non-constant operand of a comparison, printed, and
// the constant it is compared with, or a nil constant.
func splitConst(info *types.Info, e *ast.BinaryExpr) (string, constant.Value) {
	if v := info.Types[e.Y].Value; v != nil && info.Types[e.X].Value == nil {
		return types.ExprString(e.X), v
	}
	if v := info.Types[e.X].Value; v != nil && info.Types[e.Y].Value == nil {
		return types.ExprString(e.Y), v
	}
	return "", nil
}

func (p *Package) incDecSite(info *types.Info, c *astutil.Cursor, n *ast.IncDecStmt) *site {
	t := info.TypeOf(n.X)
	if t == nil || isTypeParam(t) {
		return nil
	}
	b, ok := t.Underlying().(*types.Basic)
	if !ok || b.Info()&(types.IsInteger|types.IsFloat) == 0 {
		return nil
	}
	s := p.newSite("incdec", n, n.TokPos)
	s.op, s.opOff, s.x = n.Tok, p.off(n.TokPos), n.X
	switch c.Parent().(type) {
	case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause, *ast.LabeledStmt:
	case *ast.ForStmt:
		if c.Name() != "Post" || !sideEffectFree(n.X) {
			return nil
		}
		s.forPost = true
	default:
		return nil
	}
	to := token.DEC
	if n.Tok == token.DEC {
		to = token.INC
	}
	s.mutants = []Mutant{{Kind: "uoi-incdec", From: n.Tok.String(), To: to.String()}}
	return s
}

// notSite negates a boolean operand: a variable, field, call, index, type
// assertion, dereference, or a !x, which the mutant turns into x. It skips
// comparisons and connectives, which ror and lcr cover, and every position
// where the expression is not a value that a negation may replace.
func (p *Package) notSite(info *types.Info, c *astutil.Cursor, e ast.Expr, src []byte) *site {
	switch x := e.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.TypeAssertExpr, *ast.StarExpr:
	case *ast.UnaryExpr:
		if x.Op != token.NOT {
			return nil
		}
	default:
		return nil
	}
	tv, ok := info.Types[e]
	if !ok || tv.Value != nil || !tv.IsValue() || !isPlainBool(tv.Type) {
		return nil
	}
	switch parent := c.Parent().(type) {
	case *ast.AssignStmt:
		if c.Name() == "Lhs" || len(parent.Lhs) == 2 && len(parent.Rhs) == 1 {
			return nil // an assignment target, or the source of a comma-ok form
		}
	case *ast.ValueSpec:
		if c.Name() == "Names" || len(parent.Names) == 2 && len(parent.Values) == 1 {
			return nil
		}
	case *ast.UnaryExpr:
		if parent.Op == token.AND || parent.Op == token.NOT {
			return nil // &x needs an addressable operand; !x is a site itself
		}
	case *ast.KeyValueExpr:
		if c.Name() == "Key" {
			return nil
		}
	case *ast.SelectorExpr:
		if c.Name() == "Sel" {
			return nil
		}
	case *ast.IncDecStmt, *ast.ExprStmt, *ast.DeferStmt, *ast.GoStmt, *ast.RangeStmt:
		return nil
	}
	s := p.newSite("not", e, e.Pos())
	text := snippet(src, s.start, s.end)
	s.mutants = []Mutant{{Kind: "uoi-not", From: text, To: "!(" + text + ")"}}
	return s
}

// deleteSite removes one statement of a statement list. It keeps
// declarations, labels and branches, and a final statement that terminates
// its list, whose removal would leave a function without a return.
func (p *Package) deleteSite(info *types.Info, c *astutil.Cursor, st ast.Stmt, src []byte) *site {
	var list []ast.Stmt
	switch parent := c.Parent().(type) {
	case *ast.BlockStmt:
		list = parent.List
	case *ast.CaseClause:
		if c.Name() != "Body" {
			return nil
		}
		list = parent.Body
	case *ast.CommClause:
		if c.Name() != "Body" {
			return nil
		}
		list = parent.Body
	default:
		return nil
	}
	switch s := st.(type) {
	case *ast.ExprStmt, *ast.SendStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
		*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.BlockStmt, *ast.DeferStmt, *ast.GoStmt:
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE || allBlank(s.Lhs) {
			return nil
		}
	default:
		return nil
	}
	if len(list) > 0 && list[len(list)-1] == st && isTerminating(info, st) {
		return nil
	}
	if hasLabel(st) {
		return nil
	}
	s := p.newSite("delete", st, st.Pos())
	s.mutants = []Mutant{{Kind: "sbr-delete", From: snippet(src, s.start, s.end), To: "deleted"}}
	return s
}

// zeroSite returns the zero value of every result instead of the return's
// results. Go rejects a function whose return was deleted, so this is the
// statement removal of a return.
func (p *Package) zeroSite(info *types.Info, n *ast.ReturnStmt, src []byte, fn *ast.FuncType) *site {
	if fn == nil || fn.Results == nil || len(n.Results) == 0 || allZero(info, n.Results) {
		return nil
	}
	s := p.newSite("zero", n, n.Pos())
	for _, f := range fn.Results.List {
		t := string(src[p.off(f.Type.Pos()):p.off(f.Type.End())])
		for range max(len(f.Names), 1) {
			s.zeros = append(s.zeros, "*new("+t+")")
		}
	}
	s.mutants = []Mutant{{Kind: "sbr-zero", From: snippet(src, s.start, s.end), To: "zero values"}}
	return s
}

// allZero reports whether every result is already nil or a zero constant, so
// that replacing the results by zero values would change nothing.
func allZero(info *types.Info, results []ast.Expr) bool {
	for _, e := range results {
		tv := info.Types[e]
		if tv.IsNil() {
			continue
		}
		v := tv.Value
		if v == nil {
			return false
		}
		switch v.Kind() {
		case constant.Bool:
			if constant.BoolVal(v) {
				return false
			}
		case constant.String:
			if constant.StringVal(v) != "" {
				return false
			}
		case constant.Int, constant.Float, constant.Complex:
			if constant.Sign(v) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isTerminating reports whether s may be a terminating statement in the
// sense of the Go specification. It errs towards yes, which only keeps a
// statement out of deletion.
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

func callsRecover(info *types.Info, n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
				_, builtin := info.Uses[id].(*types.Builtin)
				found = found || builtin && id.Name == "recover"
			}
		}
		return !found
	})
	return found
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

func snippet(src []byte, start, end int) string {
	s := strings.Join(strings.Fields(string(src[start:end])), " ")
	if len(s) > 40 {
		s = s[:37] + "..."
	}
	return s
}

func isTypeParam(t types.Type) bool { _, ok := t.(*types.TypeParam); return ok }

// isPlainBool reports whether t is bool or an untyped boolean, the types that
// mix with the bool that the switch functions return.
func isPlainBool(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && (b.Kind() == types.Bool || b.Kind() == types.UntypedBool)
}

// typeName returns a name for t that resolves in every file of the package
// without a new import: a predeclared basic type, or a non-generic named type
// that the package itself declares.
func (p *Package) typeName(t types.Type) (string, bool) {
	switch x := t.(type) {
	case *types.Basic:
		if x.Info()&types.IsUntyped != 0 || x.Kind() == types.UnsafePointer {
			return "", false
		}
		return x.Name(), true
	case *types.Named:
		obj := x.Obj()
		if obj.Pkg() == p.tpkg && x.TypeArgs().Len() == 0 && obj.Parent() == p.tpkg.Scope() {
			return obj.Name(), true
		}
	}
	return "", false
}

// hasContextShift reports whether e holds a non-constant shift whose left
// operand is constant: that operand's type comes from the surrounding
// expression.
func hasContextShift(info *types.Info, e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if ok && (b.Op == token.SHL || b.Op == token.SHR) && info.Types[b].Value == nil && info.Types[b.X].Value != nil {
			found = true
		}
		return !found
	})
	return found
}

func isUntyped(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}

func sideEffectFree(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return sideEffectFree(x.X)
	}
	return false
}

// switcher spells the runtime switch. In the schemata it calls the switch
// functions; for one mutant built alone it writes the constants that the
// switch would return, so both forms run the same code.
type switcher struct {
	schemata bool
	active   int
}

func (w switcher) is(id int) string {
	if w.schemata {
		return fmt.Sprintf("_gomutIs(%d)", id)
	}
	return strconv.FormatBool(id == w.active)
}

// renderRange returns src[start:end] with every site of sites that lies in
// the range replaced by its rendering.
func (p *Package) renderRange(src []byte, start, end int, sites []*site, w switcher) string {
	var b strings.Builder
	at := start
	for _, s := range sites {
		if s.start < start || s.end > end {
			continue
		}
		b.Write(src[at:s.start])
		b.WriteString(p.renderSite(src, s, w))
		at = s.end
	}
	b.Write(src[at:end])
	return b.String()
}

// renderSite writes site s in the schemata form, or with the one mutant
// w.active applied. A mutant built alone leaves the sites inside s as they
// are in the source. Every form writes each operand once, so the output
// grows linearly with the nesting of sites.
func (p *Package) renderSite(src []byte, s *site, w switcher) string {
	sub := func(start, end int) string {
		if !w.schemata {
			return string(src[start:end])
		}
		return p.renderRange(src, start, end, s.children, w)
	}
	expr := func(e ast.Expr) string { return sub(p.off(e.Pos()), p.off(e.End())) }
	id := s.mutants[0].ID
	switch s.kind {
	case "binary":
		if w.schemata {
			fun := "_gomut_s" + strconv.Itoa(s.index)
			if s.typeArg != "" {
				fun += "[" + s.typeArg + "]"
			}
			return fun + "(" + expr(s.x) + ", " + expr(s.y) + ")"
		}
		switch to := s.mutant(w.active).To; to {
		case "true":
			return "(" + string(src[s.start:s.end]) + " || true)"
		case "false":
			return "(" + string(src[s.start:s.end]) + " && false)"
		default:
			return swapOp(src, s, to)
		}
	case "incdec":
		if !w.schemata {
			return swapOp(src, s, s.mutants[0].To)
		}
		x := expr(s.x)
		if s.forPost {
			return x + " = _gomut_s" + strconv.Itoa(s.index) + "(" + x + ")"
		}
		return "if " + w.is(id) + " {\n" + x + s.mutants[0].To + "\n} else {\n" + x + s.op.String() + "\n}"
	case "lcr":
		if w.schemata {
			fun := "_gomutAnd"
			if s.op == token.LOR {
				fun = "_gomutOr"
			}
			return fun + "(" + strconv.Itoa(id) + ", func() bool { return " + expr(s.x) + " }, func() bool { return " + expr(s.y) + " })"
		}
		// The forms keep both operands in the text, so a variable that only
		// an operand uses stays used, and evaluate them as the helpers do.
		a, b, op := "("+expr(s.x)+")", "("+expr(s.y)+")", s.op.String()
		switch s.mutant(w.active).Kind {
		case "lcr-swap":
			return "(" + a + " " + s.mutants[0].To + " " + b + ")"
		case "lcr-left":
			return "(" + a + " || false && " + b + ")"
		case "lcr-right":
			return "(false && " + a + " || " + b + ")"
		case "lcr-true":
			return "(true || " + a + " " + op + " " + b + ")"
		default:
			return "(false && (" + a + " " + op + " " + b + "))"
		}
	case "not":
		return "(" + sub(s.start, s.end) + " != " + w.is(id) + ")"
	case "delete":
		return "if !" + w.is(id) + " {\n" + sub(s.start, s.end) + "\n}"
	case "zero":
		return "if " + w.is(id) + " {\nreturn " + strings.Join(s.zeros, ", ") + "\n}\n" + sub(s.start, s.end)
	}
	panic("gomut: unknown site kind " + s.kind)
}

func (s *site) mutant(id int) Mutant {
	for _, m := range s.mutants {
		if m.ID == id {
			return m
		}
	}
	panic(fmt.Sprintf("gomut: mutant %d is not at this site", id))
}

func swapOp(src []byte, s *site, to string) string {
	return string(src[s.start:s.opOff]) + to + string(src[s.opOff+len(s.op.String()):s.end])
}

// Schemata writes every file of the package with all mutants behind the
// GOMUT_ACTIVE switch, plus one helper file, under dir, and returns the
// overlay that maps the package's paths to them.
func (p *Package) Schemata(dir string) (string, error) {
	replace := map[string]string{}
	for name, roots := range p.roots {
		if len(roots) == 0 {
			continue
		}
		src := p.src[name]
		text := []byte(p.renderRange(src, 0, len(src), roots, switcher{schemata: true}))
		if p.liftLang {
			var err error
			if text, err = liftToGo118(text); err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
		}
		dst := filepath.Join(dir, "schemata_"+strings.ReplaceAll(strings.TrimPrefix(name, "/"), "/", "_"))
		if err := os.WriteFile(dst, text, 0o644); err != nil {
			return "", err
		}
		replace[name] = dst
	}
	helperDst := filepath.Join(dir, "zz_gomut_helpers.go")
	if err := os.WriteFile(helperDst, p.helpers(), 0o644); err != nil {
		return "", err
	}
	replace[filepath.Join(p.Dir, "zz_gomut_helpers.go")] = helperDst
	return writeOverlay(dir, "schemata.json", replace)
}

func (p *Package) helpers() []byte {
	var b bytes.Buffer
	if p.liftLang {
		b.WriteString("//go:build go1.18\n\n")
	}
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"cmp\"\n\t\"os\"\n\t\"strconv\"\n)\n\n", p.Name)
	b.WriteString(`var _gomutActive = func() int { n, _ := strconv.Atoi(os.Getenv("GOMUT_ACTIVE")); return n }()

// _gomutIs reports whether mutant id is active. Every inline site calls it,
// as every helper checks the switch, so GOMUT_ACTIVE=-1 panics wherever a
// test executes instrumented code.
func _gomutIs(id int) bool {
	if _gomutActive < 0 {
		panic("gomut: forced failure")
	}
	return _gomutActive == id
}

// _gomutAnd evaluates a && b, or one of its five mutants, which start at id:
// a || b, a, b, true and false. It calls each operand at most once and only
// when the result depends on it, as && does.
func _gomutAnd(id int, a, b func() bool) bool {
	switch _gomutActive - id {
	case 0:
		return a() || b()
	case 1:
		return a()
	case 2:
		return b()
	case 3:
		return true
	case 4:
		return false
	}
	if _gomutActive < 0 {
		panic("gomut: forced failure")
	}
	return a() && b()
}

// _gomutOr evaluates a || b, or one of its five mutants, which start at id:
// a && b, a, b, true and false.
func _gomutOr(id int, a, b func() bool) bool {
	switch _gomutActive - id {
	case 0:
		return a() && b()
	case 1:
		return a()
	case 2:
		return b()
	case 3:
		return true
	case 4:
		return false
	}
	if _gomutActive < 0 {
		panic("gomut: forced failure")
	}
	return a() || b()
}

type _gomutNum interface{ ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 | ~float64 }

type _gomutInt interface{ ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr }

var _ = cmp.Compare[int] // keeps the import when no site needs cmp.Ordered

`)
	for _, s := range p.sites {
		switch {
		case s.kind == "binary":
			writeBinaryHelper(&b, s)
		case s.kind == "incdec" && s.forPost:
			writeIncDecHelper(&b, s)
		}
	}
	return b.Bytes()
}

func writeBinaryHelper(b *bytes.Buffer, s *site) {
	constraint := map[string]string{"num": "_gomutNum", "int": "_gomutInt", "ordered": "cmp.Ordered", "comparable": "comparable"}[s.constraint]
	result := "bool"
	if s.constraint == "num" || s.constraint == "int" {
		result = "T"
	}
	fmt.Fprintf(b, "func _gomut_s%d[T %s](x, y T) %s {\n\tswitch _gomutActive {\n\tcase -1:\n\t\tpanic(\"gomut: forced failure\")\n", s.index, constraint, result)
	for _, m := range s.mutants {
		if m.To == "true" || m.To == "false" {
			fmt.Fprintf(b, "\tcase %d:\n\t\treturn %s\n", m.ID, m.To)
			continue
		}
		fmt.Fprintf(b, "\tcase %d:\n\t\treturn x %s y\n", m.ID, m.To)
	}
	fmt.Fprintf(b, "\t}\n\treturn x %s y\n}\n\n", s.op)
}

func writeIncDecHelper(b *bytes.Buffer, s *site) {
	orig, mut := "+", "-"
	if s.op == token.DEC {
		orig, mut = "-", "+"
	}
	fmt.Fprintf(b, "func _gomut_s%d[T _gomutNum](v T) T {\n\tswitch _gomutActive {\n\tcase -1:\n\t\tpanic(\"gomut: forced failure\")\n\tcase %d:\n\t\treturn v %s 1\n\t}\n\treturn v %s 1\n}\n\n", s.index, s.mutants[0].ID, mut, orig)
}

// Single writes the one file that mutant m changes, with only m applied, and
// returns the overlay for it.
func (p *Package) Single(dir string, m Mutant) (string, error) {
	s, ok := p.byID[m.ID]
	if !ok {
		return "", fmt.Errorf("no mutant %d", m.ID)
	}
	src := p.src[s.file]
	text := string(src[:s.start]) + p.renderSite(src, s, switcher{active: m.ID}) + string(src[s.end:])
	dst := filepath.Join(dir, fmt.Sprintf("single_%d.go", m.ID))
	if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
		return "", err
	}
	return writeOverlay(dir, fmt.Sprintf("single_%d.json", m.ID), map[string]string{s.file: dst})
}

func writeOverlay(dir, name string, replace map[string]string) (string, error) {
	b, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, b, 0o644)
}

// Kinds counts the mutants of each kind, in a stable order.
func Kinds(ms []Mutant) string {
	counts := map[string]int{}
	for _, m := range ms {
		counts[m.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%s=%d", k, counts[k])
	}
	return strings.Join(parts, " ")
}

// Timeout is the deadline of one mutant's run: ten times the run with no
// mutant active, plus two seconds for process start and scheduling.
func Timeout(control time.Duration) time.Duration { return 10*control + 2*time.Second }

// Check is the library form: a test calls it, and go test drives the whole
// run. It builds one test binary with every mutant of the package in dir,
// proves the instrumentation runs, runs the binary once per mutant, and
// reports each surviving mutant as a failure of t. The calling test file must
// carry a build tag that the nested build does not set, or the nested binary
// would run Check again.
func Check(t testing.TB, dir string) {
	t.Helper()
	p, err := Load(dir)
	if err != nil {
		t.Fatalf("gomut: %v", err)
	}
	work := t.TempDir()
	ov, err := p.Schemata(work)
	if err != nil {
		t.Fatalf("gomut: %v", err)
	}
	env := os.Environ()
	bin := filepath.Join(work, "pkg.test")
	if failed, _, out, err := Run(p.Dir, env, 10*time.Minute, "go", "test", "-c", "-overlay", ov, "-o", bin, "."); err != nil || failed {
		t.Fatalf("gomut: build: %v\n%s", err, out)
	}
	start := time.Now()
	if failed, _, out, err := Run(p.Dir, append(env, "GOMUT_ACTIVE=0"), 10*time.Minute, bin, "-test.count=1"); err != nil || failed {
		t.Fatalf("gomut: the package's tests fail without a mutant: %v\n%s", err, out)
	}
	timeout := Timeout(time.Since(start))
	if failed, _, _, err := Run(p.Dir, append(env, "GOMUT_ACTIVE=-1"), timeout, bin, "-test.count=1", "-test.failfast"); err != nil || !failed {
		t.Fatalf("gomut: the forced-failure run passed, so no test executes an instrumented site")
	}
	survived := 0
	for _, m := range p.Mutants {
		failed, _, _, err := Run(p.Dir, append(env, "GOMUT_ACTIVE="+strconv.Itoa(m.ID)), timeout, bin, "-test.count=1", "-test.failfast", "-test.timeout="+timeout.String())
		if err != nil {
			t.Fatalf("gomut: mutant %d: %v", m.ID, err)
		}
		if !failed {
			survived++
			t.Errorf("mutant %d survived: %s %s: %s -> %s", m.ID, m.Pos, m.Kind, m.From, m.To)
		}
	}
	t.Logf("gomut: %d mutants (%s), %d killed, %d survived, %d that go vet rejects not run, one build, timeout %s", len(p.Mutants), Kinds(p.Mutants), len(p.Mutants)-survived, survived, p.Stillborn, timeout)
}

// Run runs name with args in dir under env, and reports whether it exited
// with an error and whether it ran past the timeout. On timeout it kills the
// whole process group, so a test binary that go test started dies with it.
func Run(dir string, env []string, timeout time.Duration, name string, args ...string) (failed, timedOut bool, out []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, append(append([]string{}, env...), "PWD="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return true, true, out, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		return true, false, out, nil
	}
	return false, false, out, runErr
}
