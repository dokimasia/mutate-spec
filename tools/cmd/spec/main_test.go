// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.dokimi.dev/assert"
	"go.dokimi.dev/assert/expect"
	"go.dokimi.dev/assert/golden"

	"mutate-spec/tools/internal/goref"
	"mutate-spec/tools/internal/spec"
)

// repository is the root of the checkout that the tests run in, as a path
// from this package's directory.
const repository = "../../.."

// mainEnv names the variable that makes the test binary run main, so a test
// can run the command as a process.
const mainEnv = "SPEC_TEST_MAIN"

// timeout bounds a run of the command or of a script as a process.
const timeout = time.Minute

// staleCase is the case of the repository whose expectation staleCopy
// changes.
const staleCase = "hang"

// The case that a test writes into a copy of the definition, and the files
// of its Go fixture: the module file with its content, a source file, a
// generated file, and the test that a hand-written expectation names.
const (
	testCase  = "c"
	goMod     = "go.mod"
	module    = "module fixture\n\ngo 1.21\n"
	source    = "f.go"
	generated = "gen.go"
	testName  = "TestF"
)

// The environment and the arguments of the scripts.
const (
	// envLocal names a checkout that spec-sync.sh copies from.
	envLocal = "SPEC_LOCAL"
	// envRaw replaces the base URL of upstream.
	envRaw = "SPEC_RAW"
	// scheme starts the URL of a checkout.
	scheme = "file://"
	// offline is the URL of an upstream that does not exist.
	offline = scheme + "/nonexistent"
	// strict makes spec-check.sh fail on a difference from upstream.
	strict = "--strict"
)

// The exit statuses of spec-check.sh.
const (
	// scriptPass reports that the checks pass.
	scriptPass = 0
	// scriptFail reports a copy that is not intact, or that differs from
	// upstream under --strict.
	scriptFail = 1
	// scriptUnreachable reports an upstream that does not read under
	// --strict.
	scriptUnreachable = 3
)

// The modes of the files and the directories that the tests write, and of a
// directory that a test makes read-only.
const (
	fileMode     = 0o644
	dirMode      = 0o755
	readOnlyMode = 0o555
)

// shell runs the scripts.
const shell = "sh"

// scriptTools are the programs that the scripts need.
var scriptTools = []string{shell, "python3", "curl"}

// TestMain runs main when mainEnv is set, and the tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) != "" {
		main()
	}
	os.Exit(m.Run())
}

func TestCommand(t *testing.T) {
	t.Parallel()

	t.Run("main", func(t *testing.T) {
		t.Parallel()

		t.Run("exits with the status of the command line of the process", func(t *testing.T) {
			t.Parallel()
			root := staleCopy(t)
			exe, err := os.Executable()
			assert.NoError(t, err, "the test binary has a path")
			var stdout, stderr bytes.Buffer
			var ran error
			assert.CompletesWithin(t, timeout, func(ctx context.Context) error {
				cmd := exec.CommandContext(ctx, exe, cmdCheck, root)
				cmd.Env = append(os.Environ(), mainEnv+"=1")
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				ran = cmd.Run()
				return nil
			}, "the command finishes")
			exit := assert.ErrorAs[*exec.ExitError](t, ran, "the command exits with a status")
			expect.Equal(t, exit.ExitCode(), exitProblems, "the status states that the check found a problem")
			expect.Equal(t, stdout.String(), staleProblems()+"\n", "the problems are on stdout")
			expect.Empty(t, stderr.String(), "and nothing is on stderr")
		})
	})

	t.Run("command", func(t *testing.T) {
		t.Parallel()

		usages := []struct {
			name string
			args []string
		}{
			{name: "returns 2 for a command line without a command", args: nil},
			{name: "returns 2 for a command line of three arguments", args: []string{cmdCheck, repository, repository}},
		}
		for _, tt := range usages {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				var stdout, stderr bytes.Buffer
				expect.Equal(t, command(tt.args, &stdout, &stderr), exitFailed, "a usage error ends the command")
				expect.Equal(t, stderr.String(), usage+"\n", "the usage line is on stderr")
				expect.Empty(t, stdout.String(), "and nothing is on stdout")
			})
		}

		t.Run("returns 0 for a check of the repository", func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			expect.Equal(t, command([]string{cmdCheck, repository}, &stdout, &stderr), exitOK,
				"the repository's definition and generated files pass the check")
			expect.Empty(t, stdout.String(), "no problem is on stdout")
			expect.Empty(t, stderr.String(), "and no error is on stderr")
		})

		t.Run("returns 1 for a check that finds a problem", func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			expect.Equal(t, command([]string{cmdCheck, staleCopy(t)}, &stdout, &stderr), exitProblems,
				"a stale expectation fails the check")
			expect.Equal(t, stdout.String(), staleProblems()+"\n", "each problem is a line on stdout")
			expect.Empty(t, stderr.String(), "and no error is on stderr")
		})

		t.Run("returns 2 for an error that ends the command", func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			expect.Equal(t, command([]string{cmdCheck, t.TempDir()}, &stdout, &stderr), exitFailed,
				"a definition that does not load ends the command")
			expect.HasPrefix(t, stderr.String(), errorPrefix, "the error is on stderr after the command's name")
			expect.Empty(t, stdout.String(), "and nothing is on stdout")
		})

		t.Run("reads the definition at .. without a root argument", func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			expect.Equal(t, command([]string{cmdCheck}, &stdout, &stderr), exitFailed,
				"the parent of this package's directory has no definition")
			expect.Contains(t, stderr.String(), filepath.Join(defaultRoot, spec.VersionFile),
				"and the error names the version file under ..")
		})
	})

	t.Run("run", func(t *testing.T) {
		t.Parallel()

		t.Run("returns no problem for a check of the repository", func(t *testing.T) {
			t.Parallel()
			problems, err := run(cmdCheck, repository)
			assert.NoError(t, err, "the check reads the repository")
			expect.Empty(t, problems, "and finds no problem")
		})

		t.Run("returns each generated file that a check finds stale", func(t *testing.T) {
			t.Parallel()
			problems, err := run(cmdCheck, staleCopy(t))
			assert.NoError(t, err, "the check reads the copy")
			expect.Equal(t, strings.Join(problems, "\n"), staleProblems(), "the expectation and the manifest are stale")
		})

		t.Run("returns the problems of the definition before the stale files", func(t *testing.T) {
			t.Parallel()
			root := fullCopy(t)
			var c spec.Catalogue
			assert.NoError(t, readJSON(filepath.Join(root, spec.CatalogueFile), &c), "the copy's catalogue reads")
			c.Every = ""
			writeFile(t, filepath.Join(root, spec.CatalogueFile), encode(&c, expectIndent))
			problems, err := run(cmdCheck, root)
			assert.NoError(t, err, "the check reads the copy")
			expect.Equal(t, problems, []string{"catalogue: every is empty", spec.ManifestFile + stale},
				"the broken rule precedes the stale manifest")
		})

		t.Run("returns no problem after expect and manifest write a stale copy", func(t *testing.T) {
			t.Parallel()
			root := staleCopy(t)
			_, err := run(cmdExpect, root)
			assert.NoError(t, err, "expect writes the expectations")
			_, err = run(cmdManifest, root)
			assert.NoError(t, err, "manifest writes the manifest")
			problems, err := run(cmdCheck, root)
			assert.NoError(t, err, "the check reads the copy")
			expect.Empty(t, problems, "and finds every generated file current")
		})

		t.Run("writes an expectation with its hand-written fields kept", func(t *testing.T) {
			t.Parallel()
			root := definitionCopy(t)
			dir := writeCase(t, root, `{"case": "c"}`, map[string]string{
				source: "package fixture\n\nfunc f(a int) bool {\n\treturn a < 1\n}\n\nfunc g(a int) int {\n\treturn a - 1\n}\n",
				spec.ExpectFile: `{"language": "go", "lines": ["f.go:3-5"], "suite": ["other"], "mutants": [` +
					`{"scope": "f", "kind": "ror-boundary", "nth": 0, "tests": ["TestF"], "coveredBy": ["TestF", "TestAll"]}]}`,
			})
			_, err := run(cmdExpect, root)
			assert.NoError(t, err, "expect writes the expectation")
			data, e := readExpect(t, dir)
			expect.Equal(t, e.Lines, []string{source + ":3-5"}, "the selection is kept")
			expect.Equal(t, e.Suite, []string{"other"}, "and the suite")
			tests, covered := map[string][]string{}, map[string][]string{}
			for _, m := range e.Mutants {
				if m.Tests != nil {
					tests[m.ID()] = m.Tests
				}
				if m.CoveredBy != nil {
					covered[m.ID()] = m.CoveredBy
				}
			}
			expect.Equal(t, tests, map[string][]string{"f ror-boundary 0": {testName}}, "and the tests of the mutant that names them")
			expect.Equal(t, covered, map[string][]string{"f ror-boundary 0": {testName, "TestAll"}}, "and its covering tests")
			expect.That(t, string(data)).
				Contains("a < 1", "the source text reads as written").
				HasSuffix("}\n", "and the file ends with a line break")
		})

		includes := []struct {
			name    string
			caseDoc string
			want    []spec.Generated
			scopes  []string
		}{
			{
				name:    "writes the mutants of the generated files of a case that includes them",
				caseDoc: `{"case": "c", "includeGenerated": true}`,
				want:    []spec.Generated{{File: generated, Mutants: 2, Included: true}},
				scopes:  []string{"f", "f", "f", "h", "h"},
			},
			{
				name:    "leaves out the mutants of the generated files of a case that does not include them",
				caseDoc: `{"case": "c"}`,
				want:    []spec.Generated{{File: generated, Mutants: 2}},
				scopes:  []string{"f", "f", "f"},
			},
		}
		for _, tt := range includes {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				root := definitionCopy(t)
				dir := writeCase(t, root, tt.caseDoc, map[string]string{
					source:    "package fixture\n\nfunc f(a int) bool {\n\treturn a < 1\n}\n",
					generated: "// Code generated by hand. DO NOT EDIT.\n\npackage fixture\n\nfunc h(a int) int {\n\treturn a * 2\n}\n",
				})
				_, err := run(cmdExpect, root)
				assert.NoError(t, err, "expect writes the expectation")
				_, e := readExpect(t, dir)
				expect.Equal(t, e.Generated, tt.want, "the generated file states its mutants and whether the run includes them")
				var scopes []string
				for _, m := range e.Mutants {
					scopes = append(scopes, m.Scope)
				}
				expect.Equal(t, scopes, tt.scopes, "and the expectation lists the mutants of the files that the run includes")
			})
		}

		t.Run("writes no expectation for a case without a Go fixture", func(t *testing.T) {
			t.Parallel()
			root := definitionCopy(t)
			writeFile(t, filepath.Join(root, spec.CorpusDir, testCase, spec.CaseFile), []byte(`{"case": "c"}`))
			_, err := run(cmdExpect, root)
			assert.NoError(t, err, "expect reads the corpus")
			entries, err := os.ReadDir(filepath.Join(root, spec.CorpusDir, testCase))
			assert.NoError(t, err, "the case's directory reads")
			expect.Length(t, entries, 1, "the directory has its case.json alone")
		})

		t.Run("writes the manifest with the digest of every vendored file", func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for name, content := range map[string]string{
				spec.VersionFile:   "0.9.0\n",
				spec.CatalogueFile: "{}",
				spec.ProtocolFile:  "{}",
				spec.SchemaFile:    "{}",
				filepath.Join(spec.OverlaysDir, goref.Language+spec.OverlaySuffix):   `{"language": "go"}`,
				filepath.Join(spec.CorpusDir, testCase, spec.CaseFile):               `{"case": "c"}`,
				filepath.Join(spec.CorpusDir, testCase, goref.Language, source):      "package fixture\n",
				filepath.Join(spec.CorpusDir, testCase, goref.Language, ".f.go.swp"): "an editor's file",
				filepath.Join(spec.CorpusDir, ".cache", "x.json"):                    "a tool's cache",
				syncScript:  "sync",
				checkScript: "check",
				filepath.Join("tools", "internal", "other", "other.go"): "package other\n",
			} {
				writeFile(t, filepath.Join(root, name), []byte(content))
			}
			_, err := run(cmdManifest, root)
			assert.NoError(t, err, "manifest writes the manifest")
			data, err := os.ReadFile(filepath.Join(root, spec.ManifestFile))
			assert.NoError(t, err, "the manifest reads")
			// The golden file's digests were computed with printf and
			// sha256sum, outside Go.
			golden.Match(t, "manifest.json", data, golden.ShouldUpdate())
		})

		notExist := []struct {
			name  string
			cmd   string
			setup func(t *testing.T) string
		}{
			{name: "returns an error for an expect without a definition", cmd: cmdExpect, setup: emptyRoot},
			{name: "returns an error for an expect without a corpus", cmd: cmdExpect, setup: definitionCopy},
			{
				name: "returns an error for an expect of a fixture without a case.json",
				cmd:  cmdExpect,
				setup: func(t *testing.T) string {
					root := definitionCopy(t)
					writeFile(t, filepath.Join(root, spec.CorpusDir, testCase, goref.Language, source), []byte("package fixture\n"))
					return root
				},
			},
			{name: "returns an error for a manifest without a vendored script", cmd: cmdManifest, setup: definitionCopy},
			{
				name: "returns an error for a manifest without a corpus",
				cmd:  cmdManifest,
				setup: func(t *testing.T) string {
					root := definitionCopy(t)
					copyTree(t, root, syncScript, checkScript)
					return root
				},
			},
			{name: "returns an error for a check without a definition", cmd: cmdCheck, setup: emptyRoot},
			{
				name: "returns an error for a check without a vendored script",
				cmd:  cmdCheck,
				setup: func(t *testing.T) string {
					root := fullCopy(t)
					assert.NoError(t, os.Remove(filepath.Join(root, syncScript)), "the script is removed")
					return root
				},
			},
		}
		for _, tt := range notExist {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := run(tt.cmd, tt.setup(t))
				assert.ErrorIs(t, err, fs.ErrNotExist, "the command reports the missing file")
			})
		}

		broken := []struct {
			name  string
			cmd   string
			setup func(t *testing.T) string
			want  string
		}{
			{
				name:  "returns an error for an unknown command",
				cmd:   "vet",
				setup: func(*testing.T) string { return repository },
				want:  `unknown command "vet"`,
			},
			{
				name: "returns an error for an expect of a case.json that is no JSON",
				cmd:  cmdExpect,
				setup: func(t *testing.T) string {
					root := definitionCopy(t)
					writeCase(t, root, "{", map[string]string{source: "package fixture\n"})
					return root
				},
				want: spec.CaseFile,
			},
			{
				name: "returns an error for an expect of a fixture that does not type-check",
				cmd:  cmdExpect,
				setup: func(t *testing.T) string {
					root := definitionCopy(t)
					writeCase(t, root, `{"case": "c"}`, map[string]string{source: "package fixture\n\nfunc f() int { return missing }\n"})
					return root
				},
				want: "undefined: missing",
			},
			{
				name: "returns an error for a check of a fixture that does not type-check",
				cmd:  cmdCheck,
				setup: func(t *testing.T) string {
					root := fullCopy(t)
					writeCase(t, root, `{"case": "c"}`, map[string]string{
						source:          "package fixture\n\nfunc f() int { return missing }\n",
						spec.ExpectFile: `{"language": "go"}`,
					})
					return root
				},
				want: "undefined: missing",
			},
		}
		for _, tt := range broken {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := run(tt.cmd, tt.setup(t))
				assert.HasError(t, err, "the command fails")
				assert.Contains(t, err.Error(), tt.want, "and states why")
			})
		}

		t.Run("returns an error for an expect of an expect.json that does not read", func(t *testing.T) {
			t.Parallel()
			root := definitionCopy(t)
			dir := writeCase(t, root, `{"case": "c"}`, map[string]string{source: "package fixture\n"})
			assert.NoError(t, os.Mkdir(filepath.Join(dir, spec.ExpectFile), dirMode), "expect.json is a directory")
			_, err := run(cmdExpect, root)
			assert.HasError(t, err, "expect fails")
			assert.ErrorIsNot(t, err, fs.ErrNotExist, "and does not take the expectation for a missing one")
		})

		t.Run("returns an error for an expect that cannot write the expectation", func(t *testing.T) {
			t.Parallel()
			root := definitionCopy(t)
			dir := writeCase(t, root, `{"case": "c"}`, map[string]string{source: "package fixture\n"})
			assert.NoError(t, os.Chmod(dir, readOnlyMode), "the fixture's directory loses its write permission")
			t.Cleanup(func() { _ = os.Chmod(dir, dirMode) })
			_, err := run(cmdExpect, root)
			assert.ErrorIs(t, err, fs.ErrPermission, "expect reports the file that it cannot write")
		})
	})

	t.Run("readJSON", func(t *testing.T) {
		t.Parallel()

		t.Run("returns an error that wraps fs.ErrNotExist for a missing file", func(t *testing.T) {
			t.Parallel()
			var v any
			assert.ErrorIs(t, readJSON(filepath.Join(t.TempDir(), spec.CaseFile), &v), fs.ErrNotExist,
				"readJSON reports the missing file")
		})

		t.Run("returns an error that names a file that is no JSON", func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), spec.CaseFile)
			writeFile(t, path, []byte("{"))
			var v any
			err := readJSON(path, &v)
			assert.HasError(t, err, "readJSON refuses the file")
			assert.Contains(t, err.Error(), path, "and names it")
		})
	})

	t.Run("digest", func(t *testing.T) {
		t.Parallel()

		// The digests are the SHA-256 test vectors of FIPS 180-2.
		tests := []struct {
			name string
			give string
			want string
		}{
			{name: "returns the digest of no data", give: "", want: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
			{name: "returns the digest of abc", give: "abc", want: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, digest([]byte(tt.give)), tt.want, "digest names the hash and writes its digest in hexadecimal")
			})
		}
	})

	t.Run("encode", func(t *testing.T) {
		t.Parallel()

		t.Run("panics on a value that encoding/json cannot encode", func(t *testing.T) {
			t.Parallel()
			reason := assert.Panics(t, func() { encode(make(chan int), expectIndent) }, "encode refuses a channel")
			err, ok := reason.(error)
			assert.True(t, ok, "the panic is an error")
			assert.ErrorAs[*json.UnsupportedTypeError](t, err, "and it is encoding/json's error of the type")
		})
	})
}

func TestScripts(t *testing.T) {
	t.Parallel()
	for _, tool := range scriptTools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the scripts need %s: %v", tool, err)
		}
	}

	t.Run("sync", func(t *testing.T) {
		t.Parallel()

		t.Run("copies the definition and the Go fixtures of the corpus", func(t *testing.T) {
			t.Parallel()
			_, dest := synced(t)
			for _, name := range []string{
				path.Base(spec.CatalogueFile),
				path.Base(spec.ManifestFile),
				filepath.Join("corpus", staleCase, spec.CaseFile),
				filepath.Join("corpus", staleCase, goref.Language, spec.ExpectFile),
			} {
				_, err := os.Stat(filepath.Join(dest, name))
				expect.NoError(t, err, "the copy has "+name)
			}
		})
	})

	t.Run("check", func(t *testing.T) {
		t.Parallel()

		t.Run("exits 0 for a copy that matches upstream", func(t *testing.T) {
			t.Parallel()
			work, dest := synced(t)
			out, code := script(t, work, []string{envRaw + "=" + scheme + absolute(t)}, checkScript, dest, strict)
			expect.Equal(t, code, scriptPass, "the check passes")
			expect.Contains(t, out, "spec: matches main", "and states that the copy matches upstream")
		})

		t.Run("exits 3 for a strict check without upstream", func(t *testing.T) {
			t.Parallel()
			work, dest := synced(t)
			out, code := script(t, work, []string{envRaw + "=" + offline}, checkScript, dest, strict)
			expect.Equal(t, code, scriptUnreachable, "the check fails")
			expect.Contains(t, out, "could not reach main", "and states that upstream did not read")
		})

		t.Run("exits 1 for a copy with a changed file", func(t *testing.T) {
			t.Parallel()
			work, dest := synced(t)
			catalogue := filepath.Join(dest, path.Base(spec.CatalogueFile))
			data, err := os.ReadFile(catalogue)
			assert.NoError(t, err, "the copy's catalogue reads")
			writeFile(t, catalogue, append(data, '\n'))
			out, code := script(t, work, []string{envRaw + "=" + offline}, checkScript, dest)
			expect.Equal(t, code, scriptFail, "the check fails")
			expect.Contains(t, out, "these do not match the manifest beside them: "+spec.CatalogueFile, "and names the file")
		})

		t.Run("exits 1 for a copy with a file that the manifest does not list", func(t *testing.T) {
			t.Parallel()
			work, dest := synced(t)
			extra := filepath.Join("corpus", staleCase, goref.Language, "extra.go")
			writeFile(t, filepath.Join(dest, extra), []byte("package fixture\n"))
			out, code := script(t, work, []string{envRaw + "=" + offline}, checkScript, dest)
			expect.Equal(t, code, scriptFail, "the check fails")
			expect.Contains(t, out, "the manifest does not list "+extra, "and names the file")
		})
	})
}

// emptyRoot returns a new directory without a definition.
func emptyRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// definitionCopy copies VERSION, the catalogue, the protocol, the record
// schema and the overlays of the repository into a new directory, and
// returns it.
func definitionCopy(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyTree(t, root, spec.VersionFile, spec.CatalogueFile, spec.ProtocolFile, spec.SchemaFile, spec.OverlaysDir)
	return root
}

// fullCopy copies every file of the repository that an engine vendors, and
// the manifest, into a new directory, and returns it.
func fullCopy(t *testing.T) string {
	t.Helper()
	root := definitionCopy(t)
	copyTree(t, root, spec.CorpusDir, spec.ManifestFile, syncScript, checkScript)
	return root
}

// staleCopy returns a full copy whose expectation of staleCase replaces the
// first mutant's replacement, so that both the expectation and the
// manifest are stale.
func staleCopy(t *testing.T) string {
	t.Helper()
	root := fullCopy(t)
	dir := filepath.Join(root, spec.CorpusDir, staleCase, goref.Language)
	_, e := readExpect(t, dir)
	e.Mutants[0].Replacement += " "
	writeFile(t, filepath.Join(dir, spec.ExpectFile), encode(&e, expectIndent))
	return root
}

// staleProblems returns the lines that a check of staleCopy reports.
func staleProblems() string {
	return path.Join(spec.CorpusDir, staleCase, goref.Language, spec.ExpectFile) + stale + "\n" + spec.ManifestFile + stale
}

// writeCase writes testCase into the corpus under root with the case.json
// doc and the files of its Go fixture, with a module file, and returns the
// fixture's directory.
func writeCase(t *testing.T, root, doc string, files map[string]string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, spec.CorpusDir, testCase, spec.CaseFile), []byte(doc))
	dir := filepath.Join(root, spec.CorpusDir, testCase, goref.Language)
	writeFile(t, filepath.Join(dir, goMod), []byte(module))
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), []byte(content))
	}
	return dir
}

// readExpect returns the expect.json of the fixture in dir, as its bytes and
// decoded.
func readExpect(t *testing.T, dir string) ([]byte, spec.Expect) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, spec.ExpectFile))
	assert.NoError(t, err, "the expectation reads")
	var e spec.Expect
	assert.NoError(t, json.Unmarshal(data, &e), "and decodes")
	return data, e
}

// copyTree copies each of names, a file or a directory of the repository,
// to the same path under root.
func copyTree(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		from, to := filepath.Join(repository, name), filepath.Join(root, name)
		info, err := os.Stat(from)
		assert.NoError(t, err, "the repository has "+name)
		if info.IsDir() {
			assert.NoError(t, os.CopyFS(to, os.DirFS(from)), "the directory is copied")
			continue
		}
		data, err := os.ReadFile(from)
		assert.NoError(t, err, "the file reads")
		writeFile(t, to, data)
	}
}

// writeFile writes data to path, and creates the directories of path.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), dirMode), "the directory of the file is created")
	assert.NoError(t, os.WriteFile(path, data, fileMode), "the file is written")
}

// absolute returns the repository's root as an absolute path.
func absolute(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(repository)
	assert.NoError(t, err, "the repository has an absolute path")
	return root
}

// synced returns a new work directory with copies of the scripts, and the
// directory in it to which spec-sync.sh copied the repository's Go
// definition. The sync reads the repository, and its closing check finds
// no upstream.
func synced(t *testing.T) (work, dest string) {
	t.Helper()
	work = t.TempDir()
	copyTree(t, work, syncScript, checkScript)
	dest = filepath.Join(work, "vendored")
	out, code := script(t, work, []string{envLocal + "=" + absolute(t), envRaw + "=" + offline}, syncScript, dest, goref.Language)
	assert.Equal(t, code, scriptPass, "the sync passes: "+out)
	assert.Contains(t, out, "files intact at", "and its check finds the copy intact")
	return work, dest
}

// script runs the script at name, a path from work, with args and with env
// added to the environment, and returns its output and its exit status.
func script(t *testing.T, work string, env []string, name string, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	var cmd *exec.Cmd
	assert.CompletesWithin(t, timeout, func(ctx context.Context) error {
		cmd = exec.CommandContext(ctx, shell, append([]string{name}, args...)...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout, cmd.Stderr = &out, &out
		// An exit status other than 0 is an outcome that the caller checks,
		// and ProcessState is nil when the shell does not start.
		_ = cmd.Run()
		return nil
	}, "the script finishes")
	assert.NotNil(t, cmd.ProcessState, "the script runs")
	return out.String(), cmd.ProcessState.ExitCode()
}
