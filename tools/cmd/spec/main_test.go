// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mutate-spec/tools/internal/spec"
)

// repository is the root of the checkout that the tests run in.
const repository = "../../.."

// copyTree copies the files under each of names, paths from repository,
// into root.
func copyTree(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		err := filepath.WalkDir(filepath.Join(repository, name), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(repository, path)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, rel), data, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestCheckPassesTheRepository(t *testing.T) {
	problems, err := run("check", repository)
	if err != nil || len(problems) != 0 {
		t.Errorf("check = %q, %v, want no problem", problems, err)
	}
}

func TestCheckReportsAStaleExpectationAndManifest(t *testing.T) {
	root := t.TempDir()
	copyTree(t, root, "VERSION", "spec", "tools/spec-sync.sh", "tools/spec-check.sh")
	path := filepath.Join(root, "spec", "corpus", "hang", "go", "expect.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"replacement": "i < n"`, `"replacement": "i > n"`, 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := run("check", root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"spec/corpus/hang/go/expect.json: stale, run spec expect and spec manifest",
		"spec/manifest.json: stale, run spec expect and spec manifest",
	}
	if !slices.Equal(problems, want) {
		t.Errorf("check = %q, want %q", problems, want)
	}
}

func TestExpectKeepsTheHandWrittenFields(t *testing.T) {
	root := t.TempDir()
	copyTree(t, root, "VERSION", "spec/catalogue.json", "spec/protocol.json", "spec/record.schema.json", "spec/overlays")
	dir := filepath.Join(root, "spec", "corpus", "c", "go")
	files := map[string]string{
		"go.mod":      "module fixture\n\ngo 1.21\n",
		"f.go":        "package fixture\n\nfunc f(a int) int {\n\treturn a + 1\n}\n\nfunc g(a int) int {\n\treturn a - 1\n}\n",
		"expect.json": `{"language": "go", "lines": ["f.go:3-5"], "suite": ["other"], "mutants": [{"scope": "f", "kind": "aor", "nth": 0, "tests": ["TestF"], "coveredBy": ["TestF", "TestAll"]}]}`,
	}
	for name, content := range files {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run("expect", root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var e spec.Expect
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range e.Mutants {
		got = append(got, m.ID()+" "+strings.Join(m.Tests, ",")+" "+strings.Join(m.CoveredBy, ","))
	}
	want := []string{"f sbr-zero 0  ", "f aor 0 TestF TestF,TestAll", "g sbr-zero 0  ", "g aor 0  "}
	if !slices.Equal(e.Lines, []string{"f.go:3-5"}) || !slices.Equal(e.Suite, []string{"other"}) || !slices.Equal(got, want) {
		t.Errorf("expect.json has lines %q, suite %q and mutants %q, want [f.go:3-5], [other] and %q", e.Lines, e.Suite, got, want)
	}
	if strings.Contains(string(data), `<`) || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("expect.json is not written as source reads, with a final line break:\n%s", data)
	}
}

func TestManifestDigestsEveryVendoredFile(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"VERSION":                       "0.9.0\n",
		"spec/catalogue.json":           "{}",
		"spec/protocol.json":            "{}",
		"spec/record.schema.json":       "{}",
		"spec/overlays/go.json":         `{"language": "go"}`,
		"spec/corpus/c/case.json":       `{"case": "c"}`,
		"spec/corpus/c/go/f.go":         "package fixture\n",
		"spec/corpus/c/go/.f.go.swp":    "an editor's file",
		"tools/spec-sync.sh":            "sync",
		"tools/spec-check.sh":           "check",
		"tools/internal/other/other.go": "package other\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, data, err := manifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "spec", "manifest.json") {
		t.Errorf("manifest's path is %s", path)
	}
	var m struct {
		Version string            `json:"version"`
		Digest  string            `json:"digest"`
		Files   map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var names []string
	var joined strings.Builder
	for name := range m.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if m.Files[name] != digest([]byte(files[name])) {
			t.Errorf("%s has the digest %s, want the digest of its content", name, m.Files[name])
		}
		joined.WriteString(name + " " + m.Files[name] + "\n")
	}
	want := []string{
		"VERSION", "spec/catalogue.json", "spec/corpus/c/case.json", "spec/corpus/c/go/f.go", "spec/overlays/go.json",
		"spec/protocol.json", "spec/record.schema.json", "tools/spec-check.sh", "tools/spec-sync.sh",
	}
	if !slices.Equal(names, want) {
		t.Errorf("the manifest lists %q, want %q", names, want)
	}
	if m.Version != "0.9.0" || m.Digest != digest([]byte(joined.String())) {
		t.Errorf("version %q and digest %s, want 0.9.0 and the digest of the sorted lines", m.Version, m.Digest)
	}
}

// TestScriptsVendorAndCheckACopy runs copies of the two scripts against a
// copy of the definition, offline: the sync, a check of the intact copy
// against upstream, and the checks that an added file and a changed file
// fail.
func TestScriptsVendorAndCheckACopy(t *testing.T) {
	for _, tool := range []string{"sh", "python3", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the scripts need %s: %v", tool, err)
		}
	}
	repo, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	copyTree(t, work, "tools/spec-sync.sh", "tools/spec-check.sh")
	dest := filepath.Join(work, "vendored")
	script := func(env []string, args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("sh", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatal(err)
		}
		return string(out), cmd.ProcessState.ExitCode()
	}
	offline := "SPEC_RAW=file:///nonexistent"
	if out, code := script([]string{"SPEC_LOCAL=" + repo, offline}, "tools/spec-sync.sh", dest, "go"); code != 0 ||
		!strings.Contains(out, "files intact at") {
		t.Fatalf("spec-sync.sh exited %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "corpus", "hang", "go", "sum.go")); err != nil {
		t.Errorf("the sync did not copy the Go fixture of hang: %v", err)
	}
	if out, code := script([]string{"SPEC_RAW=file://" + repo}, "tools/spec-check.sh", dest, "--strict"); code != 0 ||
		!strings.Contains(out, "spec: matches main") {
		t.Errorf("spec-check.sh --strict against the same definition exited %d:\n%s", code, out)
	}
	if out, code := script([]string{offline}, "tools/spec-check.sh", dest, "--strict"); code != 3 {
		t.Errorf("spec-check.sh --strict without upstream exited %d, want 3:\n%s", code, out)
	}
	if err := os.WriteFile(filepath.Join(dest, "corpus", "hang", "go", "extra.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogue := filepath.Join(dest, "catalogue.json")
	data, err := os.ReadFile(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogue, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := script([]string{offline}, "tools/spec-check.sh", dest)
	for _, want := range []string{
		"these do not match the manifest beside them: spec/catalogue.json",
		"the manifest does not list corpus/hang/go/extra.go",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("spec-check.sh exited %d, want 1 and the problem %q:\n%s", code, want, out)
		}
	}
}
