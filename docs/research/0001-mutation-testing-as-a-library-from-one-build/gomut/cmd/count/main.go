// Command count lists the mutants of one package under gremlins' five default
// operators: arithmetic, comparison boundary, comparison negation,
// increment-decrement and negation of a unary minus.
//
// For the files the go command compiles, it type-checks every mutant and
// prints one JSON line per mutant with whether it compiles. For every non-test
// .go file in the directory, compiled or not, it prints to stderr the counts a
// syntax-only tool finds: gremlins' token mapping, and ooze's arithmetic,
// comparison and comparison-invert viruses.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type mutant struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Kind     string `json:"kind"`
	From     string `json:"from"`
	To       string `json:"to"`
	Operand  string `json:"operand"`
	Constant bool   `json:"constant"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
	Func     string `json:"func"`

	set func(token.Token)
}

var (
	arith    = map[token.Token]token.Token{token.ADD: token.SUB, token.SUB: token.ADD, token.MUL: token.QUO, token.QUO: token.MUL, token.REM: token.MUL}
	boundary = map[token.Token]token.Token{token.LSS: token.LEQ, token.LEQ: token.LSS, token.GTR: token.GEQ, token.GEQ: token.GTR}
	negation = map[token.Token]token.Token{token.LSS: token.GEQ, token.LEQ: token.GTR, token.GTR: token.LEQ, token.GEQ: token.LSS, token.EQL: token.NEQ, token.NEQ: token.EQL}
)

func main() {
	dir := flag.String("dir", ".", "package directory")
	flag.Parse()
	abs, err := filepath.Abs(*dir)
	check(err)
	abs, err = filepath.EvalSymlinks(abs)
	check(err)

	cfg := &packages.Config{Dir: abs, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps | packages.NeedModule}
	pkgs, err := packages.Load(cfg, ".")
	check(err)
	if len(pkgs) != 1 || len(pkgs[0].Errors) > 0 {
		fail("load: %d packages, errors %v", len(pkgs), pkgs[0].Errors)
	}
	lp := pkgs[0]

	conf := types.Config{
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if path == "unsafe" {
				return types.Unsafe, nil
			}
			if p, ok := lp.Imports[path]; ok {
				return p.Types, nil
			}
			return nil, fmt.Errorf("no import %q", path)
		}),
		Sizes: types.SizesFor("gc", "amd64"),
	}
	if lp.Module != nil && lp.Module.GoVersion != "" {
		conf.GoVersion = "go" + lp.Module.GoVersion
	}
	typeCheck := func() string {
		var first string
		conf.Error = func(err error) {
			if first == "" {
				first = err.Error()
			}
		}
		_, _ = conf.Check(lp.PkgPath, lp.Fset, lp.Syntax, nil)
		return first
	}
	if e := typeCheck(); e != "" {
		fail("the unmutated package does not type-check: %s", e)
	}

	var all []*mutant
	compiled := map[string]bool{}
	for i, f := range lp.Syntax {
		name := filepath.Base(lp.CompiledGoFiles[i])
		compiled[name] = true
		all = append(all, collect(lp.Fset, lp.TypesInfo, f, name)...)
	}
	for _, m := range all {
		m.set(tokenOf(m.To))
		m.Error = typeCheck()
		m.Valid = m.Error == ""
		m.set(tokenOf(m.From))
	}
	enc := json.NewEncoder(os.Stdout)
	for _, m := range all {
		check(enc.Encode(m))
	}

	// What a syntax-only tool sees: every non-test .go file, build constraints ignored.
	names, err := filepath.Glob(filepath.Join(abs, "*.go"))
	check(err)
	sort.Strings(names)
	for _, path := range names {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		check(err)
		var gremlins, ooze int
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				switch {
				case x.Op == token.SUB:
					gremlins += 2 // INVERT_NEGATIVES and ARITHMETIC_BASE share the token
				case arith[x.Op] != 0:
					gremlins++
				case boundary[x.Op] != 0:
					gremlins += 2
				case negation[x.Op] != 0:
					gremlins++
				}
				if arith[x.Op] != 0 {
					ooze++
				}
				if boundary[x.Op] != 0 {
					ooze++
				}
				if negation[x.Op] != 0 {
					ooze++
				}
			case *ast.UnaryExpr:
				if x.Op == token.SUB {
					gremlins += 2
				}
			case *ast.IncDecStmt:
				gremlins++
			}
			return true
		})
		fmt.Fprintf(os.Stderr, "syntax %-20s compiled=%-5v gremlins-sites=%d ooze-sites=%d\n", base, compiled[base], gremlins, ooze)
	}
}

// collect lists the mutants of one compiled file, each with a setter that
// rewrites its operator in place.
func collect(fset *token.FileSet, info *types.Info, f *ast.File, name string) []*mutant {
	var out []*mutant
	var fn string
	add := func(pos token.Pos, kind string, from, to token.Token, operand string, constant bool, set func(token.Token)) {
		p := fset.Position(pos)
		out = append(out, &mutant{File: name, Line: p.Line, Col: p.Column, Kind: kind, From: from.String(), To: to.String(),
			Operand: operand, Constant: constant, Func: fn, set: set})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			fn = x.Name.Name
			if x.Recv != nil && len(x.Recv.List) == 1 {
				fn = recvName(x.Recv.List[0].Type) + "." + fn
			}
		case *ast.BinaryExpr:
			constant := info.Types[x].Value != nil
			operand := classify(info, x.X, x.Y)
			set := func(t token.Token) { x.Op = t }
			if to, ok := arith[x.Op]; ok {
				add(x.OpPos, "arith", x.Op, to, operand, constant, set)
			}
			if to, ok := boundary[x.Op]; ok {
				add(x.OpPos, "boundary", x.Op, to, operand, constant, set)
			}
			if to, ok := negation[x.Op]; ok {
				add(x.OpPos, "negation", x.Op, to, operand, constant, set)
			}
		case *ast.UnaryExpr:
			if x.Op == token.SUB {
				add(x.OpPos, "invert-neg", token.SUB, token.ADD, classify(info, x.X, nil), info.Types[x].Value != nil, func(t token.Token) { x.Op = t })
			}
		case *ast.IncDecStmt:
			to := token.DEC
			if x.Tok == token.DEC {
				to = token.INC
			}
			add(x.TokPos, "incdec", x.Tok, to, classify(info, x.X, nil), false, func(t token.Token) { x.Tok = t })
		}
		return true
	})
	return out
}

// classify names the operand type of an operator: the type of the first
// operand that is not an untyped constant.
func classify(info *types.Info, x, y ast.Expr) string {
	t := info.TypeOf(x)
	if y != nil {
		if b, ok := t.(*types.Basic); ok && b.Info()&types.IsUntyped != 0 {
			t = info.TypeOf(y)
		}
	}
	switch u := t.(type) {
	case *types.TypeParam:
		return "typeparam"
	case nil:
		return "unknown"
	default:
		switch v := u.Underlying().(type) {
		case *types.Basic:
			switch {
			case v.Info()&types.IsInteger != 0:
				return "integer"
			case v.Info()&types.IsFloat != 0:
				return "float"
			case v.Info()&types.IsString != 0:
				return "string"
			case v.Info()&types.IsBoolean != 0:
				return "bool"
			}
			return v.Name()
		case *types.Pointer:
			return "pointer"
		case *types.Interface:
			return "interface"
		case *types.Slice, *types.Map, *types.Signature, *types.Chan:
			return "reference"
		}
		return fmt.Sprintf("%T", u.Underlying())
	}
}

func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

func tokenOf(s string) token.Token {
	for t := token.ILLEGAL; t < token.TILDE+1; t++ {
		if t.String() == s {
			return t
		}
	}
	fail("no token %q", s)
	return token.ILLEGAL
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "count: "+format+"\n", args...)
	os.Exit(1)
}
