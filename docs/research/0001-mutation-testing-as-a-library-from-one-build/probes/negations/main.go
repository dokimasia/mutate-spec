// Command probe-neg counts the unary minus expressions in the package in -dir
// whose value is not a constant: the sites of an invert-negatives operator.
// It type-checks with the standard library alone and prints every site, so an
// empty result cannot pass for a count.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type pkg struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
	DepOnly         bool
	ImportMap       map[string]string
	Module          *struct{ GoVersion string }
}

func main() {
	dir := flag.String("dir", ".", "package directory")
	flag.Parse()
	cmd := exec.Command("go", "list", "-json", "-export", "-deps", "-compiled", ".")
	cmd.Dir = *dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "go list:", err)
		os.Exit(1)
	}
	exports := map[string]string{}
	var target pkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "decode:", err)
			os.Exit(1)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly {
			target = p
		}
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, f := range target.CompiledGoFiles {
		if !filepath.IsAbs(f) {
			f = filepath.Join(target.Dir, f)
		}
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintln(os.Stderr, "parse:", err)
			os.Exit(1)
		}
		files = append(files, af)
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		if m, ok := target.ImportMap[path]; ok {
			path = m
		}
		return os.Open(exports[path])
	})}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	if _, err := conf.Check(target.ImportPath, fset, files, info); err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
		os.Exit(1)
	}
	sites, constants := 0, 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			u, ok := n.(*ast.UnaryExpr)
			if !ok || u.Op != token.SUB {
				return true
			}
			if info.Types[u].Value != nil {
				constants++
				return true
			}
			sites++
			fmt.Printf("  %s: -%s\n", fset.Position(u.Pos()), types.ExprString(u.X))
			return true
		})
	}
	fmt.Printf("%s: %d unary minus sites with a runtime value, %d in constant expressions, %d files\n", target.ImportPath, sites, constants, len(files))
}
