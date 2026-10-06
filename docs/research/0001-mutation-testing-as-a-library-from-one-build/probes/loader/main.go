// Command probe-loader type-checks the package in -dir with the standard
// library alone: go list -export supplies each dependency's export data, and
// go/importer reads it. It prints what it loaded, so an empty result cannot
// pass for a success.
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
	"time"
)

type pkg struct {
	ImportPath      string
	Dir             string
	Name            string
	Export          string
	GoFiles         []string
	CompiledGoFiles []string
	IgnoredGoFiles  []string
	ImportMap       map[string]string
	DepOnly         bool
	Module          *struct{ GoVersion string }
	Error           *struct{ Err string }
}

func main() {
	dir := flag.String("dir", ".", "package directory")
	flag.Parse()
	start := time.Now()
	cmd := exec.Command("go", "list", "-json", "-export", "-deps", "-compiled", "-e", ".")
	cmd.Dir = *dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go list: %v\n%s", err, stderr.String())
		os.Exit(1)
	}
	listed := time.Since(start)
	exports := map[string]string{}
	var target *pkg
	dec := json.NewDecoder(bytes.NewReader(out))
	n := 0
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "decode:", err)
			os.Exit(1)
		}
		n++
		if p.Error != nil {
			fmt.Fprintf(os.Stderr, "package %s: %s\n", p.ImportPath, p.Error.Err)
			os.Exit(1)
		}
		exports[p.ImportPath] = p.Export
		if !p.DepOnly {
			q := p
			target = &q
		}
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, f := range target.CompiledGoFiles {
		if !filepath.IsAbs(f) {
			f = filepath.Join(target.Dir, f)
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			fmt.Fprintln(os.Stderr, "parse:", err)
			os.Exit(1)
		}
		files = append(files, af)
	}
	lookup := func(path string) (io.ReadCloser, error) {
		if m, ok := target.ImportMap[path]; ok {
			path = m
		}
		e, ok := exports[path]
		if !ok || e == "" {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(e)
	}
	goVersion := ""
	if target.Module != nil && target.Module.GoVersion != "" {
		goVersion = "go" + target.Module.GoVersion
	}
	var typeErrs []error
	conf := types.Config{
		Importer:  importer.ForCompiler(fset, "gc", lookup),
		GoVersion: goVersion,
		Error:     func(err error) { typeErrs = append(typeErrs, err) },
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	_, err = conf.Check(target.ImportPath, fset, files, info)
	binary := 0
	for e, tv := range info.Types {
		if _, ok := e.(*ast.BinaryExpr); ok && tv.Type != nil {
			binary++
		}
	}
	fmt.Printf("%s: %d packages listed in %s, %d compiled files of %d go files (%d ignored), go line %q, %d type errors, first error %v, %d typed binary expressions, total %s\n",
		target.ImportPath, n, listed.Round(time.Millisecond), len(target.CompiledGoFiles), len(target.GoFiles), len(target.IgnoredGoFiles), goVersion, len(typeErrs), err, binary, time.Since(start).Round(time.Millisecond))
}
