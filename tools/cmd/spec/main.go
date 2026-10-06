// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

// Command spec writes the generated files of the definition and checks
// every file of it.
//
// Usage:
//
//	spec expect [root]
//	spec manifest [root]
//	spec check [root]
//
// root is the repository's root, .. by default, so the command runs from
// the tools directory.
//
// expect writes spec/corpus/<case>/go/expect.json of every case that has a
// Go fixture, from goref's enumeration of the fixture's root package. It
// keeps the file's lines, its suite, and the tests and the covering tests of
// each mutant, which the enumeration does not decide.
//
// manifest writes spec/manifest.json: the SHA-256 digest of every file that
// an engine vendors, and a digest over the sorted list of those digests.
//
// check reports every problem of the definition, every expect.json that
// expect would change, and a manifest that manifest would change. It exits
// with status 1 when it reports one.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mutate-spec/tools/internal/goref"
	"mutate-spec/tools/internal/spec"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: spec expect|manifest|check [root]")
		os.Exit(2)
	}
	root := ".."
	if len(os.Args) == 3 {
		root = os.Args[2]
	}
	problems, err := run(os.Args[1], root)
	for _, p := range problems {
		fmt.Println(p)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spec:", err)
		os.Exit(2)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

// run runs the command named cmd on the definition under root, and returns
// the problems that check finds.
func run(cmd, root string) ([]string, error) {
	switch cmd {
	case "expect":
		return nil, writeAll(root, expectations)
	case "manifest":
		return nil, writeAll(root, func(root string) (map[string][]byte, error) {
			path, data, err := manifest(root)
			return map[string][]byte{path: data}, err
		})
	case "check":
		return check(root)
	}
	return nil, fmt.Errorf("unknown command %q", cmd)
}

// writeAll writes each file that generate returns.
func writeAll(root string, generate func(string) (map[string][]byte, error)) error {
	files, err := generate(root)
	if err != nil {
		return err
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// check returns every problem of the definition and every generated file
// that differs from the file the command would write.
func check(root string) ([]string, error) {
	s, err := spec.Load(root)
	if err != nil {
		return nil, err
	}
	problems := s.Check()
	files, err := expectations(root)
	if err != nil {
		return nil, err
	}
	path, data, err := manifest(root)
	if err != nil {
		return nil, err
	}
	files[path] = data
	var stale []string
	for path, data := range files {
		if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, data) {
			rel, _ := filepath.Rel(root, path)
			stale = append(stale, filepath.ToSlash(rel)+": stale, run spec expect and spec manifest")
		}
	}
	sort.Strings(stale)
	return append(problems, stale...), nil
}

// expectations returns the expect.json of every case with a Go fixture, by
// path.
func expectations(root string) (map[string][]byte, error) {
	def, err := spec.LoadDefinition(root)
	if err != nil {
		return nil, err
	}
	corpus := filepath.Join(root, "spec", "corpus")
	cases, err := os.ReadDir(corpus)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, c := range cases {
		dir := filepath.Join(corpus, c.Name(), "go")
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		path := filepath.Join(dir, "expect.json")
		var old spec.Expect
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &old); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		r, err := goref.Enumerate(dir, def.Catalogue, def.Overlays["go"], old.Lines)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		e := r.Expect
		e.Suite = old.Suite
		written := map[string]spec.ExpectMutant{}
		for _, m := range old.Mutants {
			written[m.ID()] = m
		}
		for i := range e.Mutants {
			m := written[e.Mutants[i].ID()]
			e.Mutants[i].Tests, e.Mutants[i].CoveredBy = m.Tests, m.CoveredBy
		}
		if files[path], err = encode(e, "  "); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// vendored lists the files that an engine vendors besides the overlays and
// the corpus, which manifest lists by walking their directories.
var vendored = []string{
	"VERSION",
	"spec/catalogue.json",
	"spec/protocol.json",
	"spec/record.schema.json",
	"tools/spec-sync.sh",
	"tools/spec-check.sh",
}

// manifest returns the path of spec/manifest.json and its content: the
// version, the digest of each vendored file by its path from root, and the
// digest of the sorted lines "path digest". A name that starts with a dot
// is left out of the walk.
func manifest(root string) (string, []byte, error) {
	files := map[string]string{}
	add := func(name string) error {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[name] = "sha256:" + hex.EncodeToString(sum[:])
		return nil
	}
	for _, name := range vendored {
		if err := add(name); err != nil {
			return "", nil, err
		}
	}
	for _, dir := range []string{"spec/overlays", "spec/corpus"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			return add(filepath.ToSlash(rel))
		})
		if err != nil {
			return "", nil, err
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var joined strings.Builder
	for _, name := range names {
		joined.WriteString(name + " " + files[name] + "\n")
	}
	sum := sha256.Sum256([]byte(joined.String()))
	version, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return "", nil, err
	}
	data, err := encode(map[string]any{
		"version": strings.TrimSpace(string(version)),
		"digest":  "sha256:" + hex.EncodeToString(sum[:]),
		"files":   files,
	}, "    ")
	return filepath.Join(root, "spec", "manifest.json"), data, err
}

// encode returns v as indented JSON with a final line break, and with <, >
// and & as they are, so source text in a fixture's expectations reads as
// written.
func encode(v any, indent string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
