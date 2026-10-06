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
// expect would change, and a manifest that manifest would change.
//
// # Exit status
//
//   - 0 when the command succeeds and check does not report a problem.
//   - 1 when check reports a problem.
//   - 2 for a usage error, and for an error that ends the command, such as
//     a file that does not read.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mutate-spec/tools/internal/goref"
	"mutate-spec/tools/internal/spec"
)

// The commands, as the first argument names them.
const (
	cmdExpect   = "expect"
	cmdManifest = "manifest"
	cmdCheck    = "check"
)

// The exit statuses of the command.
const (
	exitOK       = 0
	exitProblems = 1
	exitFailed   = 2
)

// The text that the command prints: the usage line of a usage error, the
// prefix of an error that ends the command, and the end of the problem of a
// generated file that differs from the file the command would write.
const (
	usage       = "usage: spec expect|manifest|check [root]"
	errorPrefix = "spec: "
	stale       = ": stale, run spec expect and spec manifest"
)

// defaultRoot is the repository's root as a path from the tools directory.
const defaultRoot = ".."

// The scripts that an engine vendors with the definition, as paths from the
// repository's root.
const (
	syncScript  = "tools/spec-sync.sh"
	checkScript = "tools/spec-check.sh"
)

// digestPrefix starts each digest of the manifest: the name of its hash.
const digestPrefix = "sha256:"

// The indentation of the generated files.
const (
	expectIndent   = "  "
	manifestIndent = "    "
)

// writeMode is the mode of each file that the command writes.
const writeMode fs.FileMode = 0o644

// vendored lists the files that an engine vendors besides the overlays and
// the corpus, which manifest lists by walking their directories.
var vendored = []string{
	spec.VersionFile,
	spec.CatalogueFile,
	spec.ProtocolFile,
	spec.SchemaFile,
	syncScript,
	checkScript,
}

// manifestFile is spec/manifest.json. Its fields are in the alphabetical
// order of their keys.
type manifestFile struct {
	// Digest is the digest of the lines "path digest" of Files, sorted.
	Digest string `json:"digest"`
	// Files maps the path of each vendored file from the repository's root,
	// with forward slashes, to the digest of its content.
	Files map[string]string `json:"files"`
	// Version is the content of VERSION without its line break.
	Version string `json:"version"`
}

func main() {
	os.Exit(command(os.Args[1:], os.Stdout, os.Stderr))
}

// command runs the command line args and returns the exit status. It writes
// each problem as a line to stdout, and the usage line or an error to
// stderr.
func command(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(stderr, usage)
		return exitFailed
	}
	root := defaultRoot
	if len(args) == 2 {
		root = args[1]
	}
	problems, err := run(args[0], root)
	for _, p := range problems {
		fmt.Fprintln(stdout, p)
	}
	switch {
	case err != nil:
		fmt.Fprintln(stderr, errorPrefix+err.Error())
		return exitFailed
	case len(problems) > 0:
		return exitProblems
	}
	return exitOK
}

// run runs the command named cmd on the definition under root, and returns
// the problems that check finds.
func run(cmd, root string) ([]string, error) {
	switch cmd {
	case cmdExpect:
		return nil, writeAll(root, expectations)
	case cmdManifest:
		return nil, writeAll(root, func(root string) (map[string][]byte, error) {
			path, data, err := manifest(root)
			return map[string][]byte{path: data}, err
		})
	case cmdCheck:
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
		if err := os.WriteFile(path, data, writeMode); err != nil {
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
	var differ []string
	for path, data := range files {
		if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, data) {
			rel, _ := filepath.Rel(root, path)
			differ = append(differ, filepath.ToSlash(rel)+stale)
		}
	}
	sort.Strings(differ)
	return append(problems, differ...), nil
}

// expectations returns the expect.json of every case with a Go fixture, by
// path.
func expectations(root string) (map[string][]byte, error) {
	def, err := spec.LoadDefinition(root)
	if err != nil {
		return nil, err
	}
	cases, err := os.ReadDir(def.Path(spec.CorpusDir))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, c := range cases {
		dir := def.Fixture(c.Name(), goref.Language)
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var options spec.Case
		if err := readJSON(filepath.Join(def.Path(spec.CorpusDir), c.Name(), spec.CaseFile), &options); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, spec.ExpectFile)
		var old spec.Expect
		if err := readJSON(path, &old); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		r, err := goref.Enumerate(dir, def.Catalogue, def.Overlays[goref.Language], goref.Options{
			Lines:            old.Lines,
			IncludeGenerated: options.IncludeGenerated,
		})
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
		files[path] = encode(&e, expectIndent)
	}
	return files, nil
}

// readJSON decodes the JSON file at path into v. An error that the file
// does not exist wraps fs.ErrNotExist.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// manifest returns the path of spec/manifest.json and its content: the
// version, the digest of each vendored file by its path from root, and the
// digest of the sorted lines "path digest". A name that starts with a dot
// is left out of the walk.
func manifest(root string) (string, []byte, error) {
	m := manifestFile{Files: map[string]string{}}
	add := func(name string) error {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		if name == spec.VersionFile {
			m.Version = strings.TrimSpace(string(data))
		}
		m.Files[name] = digest(data)
		return nil
	}
	for _, name := range vendored {
		if err := add(name); err != nil {
			return "", nil, err
		}
	}
	for _, dir := range []string{spec.OverlaysDir, spec.CorpusDir} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case strings.HasPrefix(d.Name(), ".") && d.IsDir():
				return filepath.SkipDir
			case strings.HasPrefix(d.Name(), "."), d.IsDir():
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			return add(filepath.ToSlash(rel))
		})
		if err != nil {
			return "", nil, err
		}
	}
	names := make([]string, 0, len(m.Files))
	for name := range m.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	var joined strings.Builder
	for _, name := range names {
		joined.WriteString(name + " " + m.Files[name] + "\n")
	}
	m.Digest = digest([]byte(joined.String()))
	return filepath.Join(root, spec.ManifestFile), encode(&m, manifestIndent), nil
}

// digest returns the digest of data as the manifest writes it: the hash's
// name and the SHA-256 digest in hexadecimal.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return digestPrefix + hex.EncodeToString(sum[:])
}

// encode returns v as indented JSON with a final line break, and with <, >
// and & as they are, so source text in a fixture's expectations reads as
// written. The command encodes only its own types, which encoding/json
// encodes without error, so encode panics on an error.
func encode(v any, indent string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return b.Bytes()
}
