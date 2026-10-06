// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package spec_test

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.dokimi.dev/assert"
	"go.dokimi.dev/assert/expect"
	"go.dokimi.dev/assert/prop"

	"mutate-spec/tools/internal/spec"
)

// repository is the root of the checkout that the tests read.
const repository = "../../.."

// The cases of the repository that the tests read, the case of base with
// its language, the scope, the file and the test of base's mutants, and
// base's generated file.
const (
	hangCase      = "hang"
	includedCase  = "generated-included"
	baseCase      = "c"
	golang        = "go"
	baseScope     = "f"
	baseFile      = "f.go"
	baseTest      = "TestF"
	baseGenerated = "f_gen.go"
)

// The families of the Go overlay that the tests edit.
const (
	familyLogging  = "logging"
	familyTiming   = "timing"
	familyFlags    = "flags"
	familyCapacity = "capacity"
	familyHelper   = "helper"
)

// Languages besides Go: one that has no overlay in the definition, and one
// that a test gives a copy of the Go overlay.
const (
	noOverlay = "cobol"
	copied    = "python"
)

// The names of a file and of a directory that are no part of the layout.
const (
	strayFile = "README.md"
	notesFile = "notes.md"
	strayDir  = "old"
)

// The modes of the files and the directories that the tests write, and of a
// directory that a test makes unlistable.
const (
	fileMode       = 0o644
	dirMode        = 0o755
	unlistableMode = 0o311
)

// identity is the generated input of a property over the fields that
// identify a mutant.
type identity struct {
	Scope, Kind string
	Nth         int
}

// fileLine is the generated input of a property over a line of a file.
type fileLine struct {
	File string
	Line int
}

func TestSpec(t *testing.T) {
	t.Parallel()

	t.Run("Load", func(t *testing.T) {
		t.Parallel()

		t.Run("reads every case of the corpus with the expectations of its fixtures", func(t *testing.T) {
			t.Parallel()
			s, err := spec.Load(repository)
			assert.NoError(t, err, "Load reads the repository's definition")
			expect.That(t, s.Cases).
				Contains(hangCase, "the cases include hang").
				Contains(includedCase, "and generated-included")
			expect.Contains(t, s.Expects[hangCase], golang, "hang has a Go fixture")
		})

		t.Run("returns the error of a case.json that does not decode", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			writeFile(t, filepath.Join(root, spec.CorpusDir, baseCase, spec.CaseFile), `{"case": "c", "verdict": []}`)

			_, err := spec.Load(root)
			assert.HasError(t, err, "Load refuses the case")
			assert.Contains(t, err.Error(), `unknown field "verdict"`, "and names the field that the type lacks")
		})

		t.Run("returns the error of an expect.json that does not decode", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			writeFile(t, filepath.Join(root, spec.CorpusDir, hangCase, golang, spec.ExpectFile), "{")

			_, err := spec.Load(root)
			assert.HasError(t, err, "Load refuses the expectation")
			assert.Contains(t, err.Error(), spec.ExpectFile, "and names its file")
		})

		t.Run("returns the error of a definition that does not load", func(t *testing.T) {
			t.Parallel()
			_, err := spec.Load(t.TempDir())
			assert.ErrorIs(t, err, os.ErrNotExist, "Load reports the definition's missing file")
		})

		t.Run("returns an error for a definition without a corpus", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			assert.NoError(t, os.RemoveAll(filepath.Join(root, spec.CorpusDir)), "the corpus is removed")

			_, err := spec.Load(root)
			assert.ErrorIs(t, err, os.ErrNotExist, "Load reports the missing corpus")
		})

		t.Run("returns the error of a case directory that does not read", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			dir := filepath.Join(root, spec.CorpusDir, hangCase)
			assert.NoError(t, os.Chmod(dir, unlistableMode), "the case directory loses its read permission")
			t.Cleanup(func() { _ = os.Chmod(dir, dirMode) })

			_, err := spec.Load(root)
			assert.ErrorIs(t, err, os.ErrPermission, "Load reports the directory that it cannot list")
		})

		t.Run("skips a file of the corpus that is no case", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			writeFile(t, filepath.Join(root, spec.CorpusDir, strayFile), "the corpus")
			writeFile(t, filepath.Join(root, spec.CorpusDir, hangCase, notesFile), "a case's notes")

			s, err := spec.Load(root)
			assert.NoError(t, err, "Load reads the corpus")
			assert.Length(t, s.Cases, 1, "and reads the one case")
		})
	})

	t.Run("LoadDefinition", func(t *testing.T) {
		t.Parallel()

		t.Run("returns an error naming a field that the type lacks", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			path := filepath.Join(root, spec.ProtocolFile)
			data, err := os.ReadFile(path)
			assert.NoError(t, err, "the copy of the protocol reads")
			writeFile(t, path, strings.Replace(string(data), `"verdicts"`, `"verdict": [], "verdicts"`, 1))

			_, err = spec.LoadDefinition(root)
			assert.HasError(t, err, "LoadDefinition refuses the protocol")
			assert.Contains(t, err.Error(), `unknown field "verdict"`, "and names the field that the type lacks")
		})

		t.Run("returns an error for a file of two JSON values", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			path := filepath.Join(root, spec.CatalogueFile)
			data, err := os.ReadFile(path)
			assert.NoError(t, err, "the copy of the catalogue reads")
			writeFile(t, path, string(data)+"{}")

			_, err = spec.LoadDefinition(root)
			assert.HasError(t, err, "LoadDefinition refuses the catalogue")
			assert.Contains(t, err.Error(), "more than one JSON value", "and states why")
		})

		t.Run("returns an error for a root without a VERSION file", func(t *testing.T) {
			t.Parallel()
			_, err := spec.LoadDefinition(t.TempDir())
			assert.ErrorIs(t, err, os.ErrNotExist, "LoadDefinition reports the missing file")
		})

		missing := []struct {
			name string
			path string
		}{
			{name: "returns an error for a missing catalogue", path: spec.CatalogueFile},
			{name: "returns an error for a missing protocol", path: spec.ProtocolFile},
			{name: "returns an error for a missing record schema", path: spec.SchemaFile},
			{name: "returns an error for a missing directory of overlays", path: spec.OverlaysDir},
		}
		for _, tt := range missing {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				root := copyDefinition(t)
				assert.NoError(t, os.RemoveAll(filepath.Join(root, tt.path)), "the file is removed")

				_, err := spec.LoadDefinition(root)
				assert.ErrorIs(t, err, os.ErrNotExist, "LoadDefinition reports the missing file")
			})
		}

		malformed := []struct {
			name string
			path string
		}{
			{name: "returns an error for a record schema that is no JSON", path: spec.SchemaFile},
			{name: "returns an error for an overlay that is no JSON", path: filepath.Join(spec.OverlaysDir, golang+spec.OverlaySuffix)},
		}
		for _, tt := range malformed {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				root := copyDefinition(t)
				writeFile(t, filepath.Join(root, tt.path), "{")

				_, err := spec.LoadDefinition(root)
				assert.HasError(t, err, "LoadDefinition refuses the file")
				assert.Contains(t, err.Error(), filepath.Base(tt.path), "and names it")
			})
		}

		t.Run("skips a directory and a file of another suffix among the overlays", func(t *testing.T) {
			t.Parallel()
			root := copyDefinition(t)
			writeFile(t, filepath.Join(root, spec.OverlaysDir, strayFile), "the overlays")
			writeFile(t, filepath.Join(root, spec.OverlaysDir, strayDir+spec.OverlaySuffix, notesFile), "an old overlay's notes")

			s, err := spec.LoadDefinition(root)
			assert.NoError(t, err, "LoadDefinition reads the overlays")
			assert.Equal(t, slices.Sorted(maps.Keys(s.Overlays)), []string{golang}, "and reads the Go overlay alone")
		})
	})

	t.Run("Check", func(t *testing.T) {
		t.Parallel()

		t.Run("returns no problem for the repository's definition and corpus", func(t *testing.T) {
			t.Parallel()
			s, err := spec.Load(repository)
			assert.NoError(t, err, "Load reads the repository's definition")
			assert.Empty(t, s.Check(), "Check finds no broken rule")
		})

		t.Run("returns no problem for the case of the tests", func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, base(t).Check(), "Check finds no broken rule in the case")
		})

		t.Run("returns no problem for a sample that a mutant without coverage does not enter", func(t *testing.T) {
			t.Parallel()
			s := base(t)
			addSecondKill(s)
			editCase(s, func(c *spec.Case) {
				c.Sample = 1
				c.Mutants[0].Verdict = spec.NoCoverage
			})
			editExpect(s, func(e *spec.Expect) { e.Mutants[0].Tests, e.Mutants[0].CoveredBy = nil, nil })
			assert.Empty(t, s.Check(), "the sample's run is the second kill")
		})

		t.Run("returns no problem for a sample that a confirmed mutant without coverage enters", func(t *testing.T) {
			t.Parallel()
			s := base(t)
			addSecondKill(s)
			editCase(s, func(c *spec.Case) {
				c.Sample, c.Confirm = 1, true
				c.Mutants[0].Verdict, c.Mutants[0].Confirmed = spec.NoCoverage, true
				c.Mutants[2].Verdict = spec.NotRun
			})
			editExpect(s, func(e *spec.Expect) { e.Mutants[0].Tests, e.Mutants[0].CoveredBy = nil, nil })
			assert.Empty(t, s.Check(), "the confirmation of the mutant without coverage is the sample's run")
		})

		t.Run("returns its problems in one order", func(t *testing.T) {
			t.Parallel()
			s := base(t)
			for _, kind := range []string{"aor-one", "aor-two", "aor-three", "aor-four"} {
				s.Overlays[golang].Kinds[kind] = []string{"a rule"}
			}
			assert.StableOrder(t, func() ([]string, error) { return s.Check(), nil },
				"Check sorts the problems that it finds in maps")
		})

		for _, tt := range brokenRules {
			t.Run("returns a problem "+tt.name, func(t *testing.T) {
				t.Parallel()
				s := base(t)
				tt.edit(s)
				assert.Contains(t, strings.Join(s.Check(), "\n"), tt.want, "Check names the broken rule")
			})
		}
	})

	t.Run("Spec", func(t *testing.T) {
		t.Parallel()

		t.Run("Fixture", func(t *testing.T) {
			t.Parallel()

			t.Run("returns the directory of a case's fixture in a language", func(t *testing.T) {
				t.Parallel()
				s := &spec.Spec{Root: repository}
				assert.Equal(t, s.Fixture(hangCase, golang), filepath.Join(repository, spec.CorpusDir, hangCase, golang),
					"Fixture joins the corpus, the case and the language")
			})
		})

		t.Run("Path", func(t *testing.T) {
			t.Parallel()

			t.Run("returns the path of each file of the layout in the checkout", func(t *testing.T) {
				t.Parallel()
				s := &spec.Spec{Root: repository}
				layout := []string{
					spec.VersionFile, spec.CatalogueFile, spec.ProtocolFile, spec.SchemaFile, spec.ManifestFile,
					spec.OverlaysDir, spec.CorpusDir,
				}
				assert.Total(t, func(name string) error {
					_, err := os.Stat(s.Path(name))
					return err
				}, layout, "every file and directory of the layout is in the repository's checkout")
			})
		})
	})

	t.Run("CaseMutant", func(t *testing.T) {
		t.Parallel()

		t.Run("ID", func(t *testing.T) {
			t.Parallel()

			t.Run("returns the scope, the kind and the nth", func(t *testing.T) {
				t.Parallel()
				m := spec.CaseMutant{Scope: baseScope, Kind: spec.AOR, Nth: 2}
				assert.Equal(t, m.ID(), "f aor 2", "ID joins the fields that identify the mutant")
			})
		})

		t.Run("In", func(t *testing.T) {
			t.Parallel()
			overlay := spec.Overlay{Kinds: map[string][]string{spec.AOR: {"a rule"}}}
			tests := []struct {
				name string
				give spec.CaseMutant
				want bool
			}{
				{name: "reports true for a kind that the overlay defines", give: spec.CaseMutant{Kind: spec.AOR}, want: true},
				{name: "reports false for a kind that the overlay lacks", give: spec.CaseMutant{Kind: spec.UOINot}, want: false},
				{
					name: "reports true for a language that the mutant lists",
					give: spec.CaseMutant{Kind: spec.UOINot, Languages: []string{noOverlay, golang}},
					want: true,
				},
				{
					name: "reports false for a language that the mutant does not list",
					give: spec.CaseMutant{Kind: spec.AOR, Languages: []string{noOverlay}},
					want: false,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					assert.Equal(t, tt.give.In(golang, overlay), tt.want, "In decides whether the fixture has the mutant")
				})
			}
		})
	})

	t.Run("ExpectMutant", func(t *testing.T) {
		t.Parallel()

		t.Run("ID", func(t *testing.T) {
			t.Parallel()

			t.Run("returns the identity of the case.json mutant with the same fields", func(t *testing.T) {
				t.Parallel()
				prop.Equal(t,
					func(in identity) string {
						return spec.ExpectMutant{Scope: in.Scope, Kind: in.Kind, Nth: in.Nth}.ID()
					},
					func(in identity) string {
						return spec.CaseMutant{Scope: in.Scope, Kind: in.Kind, Nth: in.Nth}.ID()
					},
					"the two files identify a mutant alike")
			})
		})
	})

	t.Run("Position", func(t *testing.T) {
		t.Parallel()

		t.Run("Before", func(t *testing.T) {
			t.Parallel()

			t.Run("reports false for a position and itself", func(t *testing.T) {
				t.Parallel()
				prop.False(t, func(p spec.Position) bool { return p.Before(p) }, "no position is before itself")
			})

			t.Run("orders two positions by line and then by column", func(t *testing.T) {
				t.Parallel()
				prop.ForAll(t, "Before orders positions as a reader does", func(c *prop.Case) {
					p := spec.Position{Line: c.Draw(prop.Integer(1, 9), "p.line"), Column: c.Draw(prop.Integer(1, 9), "p.column")}
					q := spec.Position{Line: c.Draw(prop.Integer(1, 9), "q.line"), Column: c.Draw(prop.Integer(1, 9), "q.column")}
					want := p.Line < q.Line || p.Line == q.Line && p.Column < q.Column
					assert.Equal(c, p.Before(q), want, "Before compares the lines first")
					assert.False(c, p.Before(q) && q.Before(p), "and no two positions are each before the other")
				})
			})
		})
	})

	t.Run("Selected", func(t *testing.T) {
		t.Parallel()

		t.Run("reports true for every line without a selection", func(t *testing.T) {
			t.Parallel()
			prop.True(t, func(in fileLine) bool { return spec.Selected(nil, in.File, in.Line) },
				"a run without a selection selects every line")
		})

		t.Run("reports whether one entry's range contains the line", func(t *testing.T) {
			t.Parallel()
			prop.ForAll(t, "an entry selects the lines first to last of its file", func(c *prop.Case) {
				first := c.Draw(prop.Integer(1, 50), "first")
				last := c.Draw(prop.Integer(first, 60), "last")
				line := c.Draw(prop.Integer(1, 70), "line")
				lines := []string{fmt.Sprintf("a.go:%d-%d", first, last)}
				assert.Equal(c, spec.Selected(lines, "a.go", line), first <= line && line <= last,
					"Selected selects the lines of the range")
				assert.False(c, spec.Selected(lines, "b.go", line), "and no line of another file")
			})
		})

		t.Run("reports false for an entry without a range", func(t *testing.T) {
			t.Parallel()
			assert.False(t, spec.Selected([]string{"a.go"}, "a.go", 1), "an entry needs a range")
		})
	})
}

// brokenRules lists one edit per rule of Check, each of which breaks only
// that rule in base's definition, with the problem that Check states.
var brokenRules = []struct {
	name string
	edit func(s *spec.Spec)
	want string
}{
	{name: "for an empty key", edit: func(s *spec.Spec) { s.Catalogue.Key = "" }, want: "catalogue: key is empty"},
	{
		name: "for an empty annotation",
		edit: func(s *spec.Spec) { s.Catalogue.Annotation = "" },
		want: "catalogue: annotation is empty",
	},
	{
		name: "for an empty include directive",
		edit: func(s *spec.Spec) { s.Catalogue.Include = "" },
		want: "catalogue: include is empty",
	},
	{
		name: "for an include directive named as the annotation",
		edit: func(s *spec.Spec) { s.Catalogue.Include = s.Catalogue.Annotation },
		want: "include is empty or the annotation's name",
	},
	{name: "for an empty keyword of every kind", edit: func(s *spec.Spec) { s.Catalogue.Every = "" }, want: "catalogue: every is empty"},
	{
		name: "for a keyword of every kind that names a kind",
		edit: func(s *spec.Spec) { s.Catalogue.Every = spec.AOR },
		want: `every is "aor", the name of a kind or a class`,
	},
	{
		name: "for a keyword of every kind that names a class",
		edit: func(s *spec.Spec) { s.Catalogue.Every = s.Catalogue.Classes[1].ID },
		want: `every is "ror", the name of a kind or a class`,
	},
	{
		name: "for a class listed twice",
		edit: func(s *spec.Spec) { s.Catalogue.Classes = append(s.Catalogue.Classes, s.Catalogue.Classes[0]) },
		want: `class "aor" is listed twice`,
	},
	{
		name: "for a kind listed twice",
		edit: func(s *spec.Spec) { s.Catalogue.Kinds = append(s.Catalogue.Kinds, s.Catalogue.Kinds[0]) },
		want: `kind "aor" is listed twice`,
	},
	{name: "for a kind of an unknown class", edit: func(s *spec.Spec) { s.Catalogue.Kinds[0].Class = "xyz" }, want: `names class "xyz"`},
	{
		name: "for a class that lists other kinds than name it",
		edit: func(s *spec.Spec) { s.Catalogue.Classes[1].Kinds = s.Catalogue.Classes[1].Kinds[1:] },
		want: `class "ror" lists kinds`,
	},
	{
		name: "for a kind without a description",
		edit: func(s *spec.Spec) { s.Catalogue.Kinds[2].Description = "" },
		want: "has no description",
	},
	{
		name: "for a family listed twice",
		edit: func(s *spec.Spec) { s.Catalogue.Families = append(s.Catalogue.Families, s.Catalogue.Families[0]) },
		want: `family "logging" is listed twice`,
	},
	{
		name: "for an empty variable",
		edit: func(s *spec.Spec) { s.Protocol.Variable = "" },
		want: "protocol: variable and instrumented must name two variables",
	},
	{
		name: "for one variable named twice",
		edit: func(s *spec.Spec) { s.Protocol.Instrumented = s.Protocol.Variable },
		want: "protocol: variable and instrumented must name two variables",
	},
	{name: "for a record version of 0", edit: func(s *spec.Spec) { s.Protocol.Record.Version = 0 }, want: "a version of at least 1"},
	{
		name: "for a verdict listed twice",
		edit: func(s *spec.Spec) { s.Protocol.Verdicts = append(s.Protocol.Verdicts, s.Protocol.Verdicts[0]) },
		want: `verdict "killed" is listed twice`,
	},
	{name: "for a verdict of an unknown place", edit: func(s *spec.Spec) { s.Protocol.Verdicts[0].Score = "maybe" }, want: `counts as "maybe"`},
	{
		name: "for a run error listed twice",
		edit: func(s *spec.Spec) { s.Protocol.Errors = append(s.Protocol.Errors, s.Protocol.Errors[0]) },
		want: `error "load" is listed twice`,
	},
	{
		name: "for a limit's factor of 0",
		edit: func(s *spec.Spec) { s.Protocol.Limits.Memory.Factor = 0 },
		want: "a limit's factor must be positive",
	},
	{
		name: "for a schema whose kinds differ from the catalogue's",
		edit: func(s *spec.Spec) {
			s.Catalogue.Kinds = s.Catalogue.Kinds[:12]
			s.Catalogue.Classes[4].Kinds = []string{spec.SBRDelete}
		},
		want: "$defs.kind enumerates",
	},
	{
		name: "for a schema whose verdicts differ from the protocol's",
		edit: func(s *spec.Spec) { s.Protocol.Verdicts[1].ID = "late" },
		want: "$defs.verdict enumerates",
	},
	{
		name: "for a schema whose families differ from the catalogue's",
		edit: func(s *spec.Spec) { s.Catalogue.Families[3].ID = "sizing" },
		want: "$defs.family enumerates",
	},
	{
		name: "for a schema whose run errors differ from the protocol's",
		edit: func(s *spec.Spec) { s.Protocol.Errors = s.Protocol.Errors[1:] },
		want: "$defs.error enumerates",
	},
	{
		name: "for a schema whose record name differs from the protocol's",
		edit: func(s *spec.Spec) { s.Protocol.Record.Name = "other" },
		want: "properties.record.const",
	},
	{
		name: "for a schema whose record version differs from the protocol's",
		edit: func(s *spec.Spec) { s.Protocol.Record.Version = 2 },
		want: "properties.version.const",
	},
	{
		name: "for an overlay that declares another language",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Language = "golang" }) },
		want: `declares language "golang"`,
	},
	{
		name: "for an overlay without a comment marker",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Comment = "" }) },
		want: "comment is empty",
	},
	{
		name: "for an overlay without a compound rule",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Compound = nil }) },
		want: "compound states no rule",
	},
	{
		name: "for an overlay's kind that the catalogue lacks",
		edit: func(s *spec.Spec) { s.Overlays[golang].Kinds["aor-plus"] = []string{"x"} },
		want: `kind "aor-plus" is not in the catalogue`,
	},
	{name: "for an overlay's kind without rules", edit: func(s *spec.Spec) { s.Overlays[golang].Kinds[spec.AOR] = nil }, want: `kind "aor" has no rules`},
	{
		name: "for a skip that does not state where it applies",
		edit: func(s *spec.Spec) { s.Overlays[golang].Skips[0].Where = "" },
		want: "does not state where it applies",
	},
	{
		name: "for a skip listed twice",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Skips = append(o.Skips, o.Skips[0]) }) },
		want: "listed twice",
	},
	{
		name: "for an overlay's family that the catalogue lacks",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families["sizing"] = spec.Rules{APIs: []string{"pkg.Size"}} },
		want: `family "sizing" is not in the catalogue`,
	},
	{
		name: "for a family without a rule",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families[familyFlags] = spec.Rules{} },
		want: `family "flags" states no rule`,
	},
	{
		name: "for an empty API",
		edit: func(s *spec.Spec) { editRules(s, familyFlags, func(r *spec.Rules) { r.APIs = append(r.APIs, "") }) },
		want: "flags lists an empty API",
	},
	{
		name: "for an API that two families list",
		edit: func(s *spec.Spec) {
			editRules(s, familyFlags, func(r *spec.Rules) { r.APIs = append(r.APIs, s.Overlays[golang].Families[familyTiming].APIs[8]) })
		},
		want: `timing lists "time.Sleep", which flags lists too`,
	},
	{
		name: "for a method rule on a struct",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families[familyHelper].Methods[0].On = "struct" },
		want: `method rule "Helper" of helper needs a name, a signature and on: interface`,
	},
	{
		name: "for a method rule that two families state",
		edit: func(s *spec.Spec) {
			editRules(s, familyLogging, func(r *spec.Rules) {
				r.Methods = append(r.Methods, s.Overlays[golang].Families[familyHelper].Methods[0])
			})
		},
		want: `states the method rule "Helper", which helper states too`,
	},
	{
		name: "for a result rule of a family that lists no API",
		edit: func(s *spec.Spec) {
			editRules(s, familyCapacity, func(r *spec.Rules) { r.Results = s.Overlays[golang].Families[familyTiming].Results })
		},
		want: `result rule "context.CancelFunc" of capacity needs a family that lists APIs`,
	},
	{
		name: "for a result rule of a type of no package",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families[familyTiming].Results[0].Type = "CancelFunc" },
		want: `result rule "CancelFunc" of timing needs`,
	},
	{
		name: "for a result rule stated twice",
		edit: func(s *spec.Spec) {
			editRules(s, familyTiming, func(r *spec.Rules) { r.Results = append(r.Results, r.Results[0]) })
		},
		want: "and no twin",
	},
	{
		name: "for result rules without a variables rule",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Variables = nil }) },
		want: "states result rules and no variables rule",
	},
	{
		name: "for an argument rule of make without its allocated kind",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families[familyCapacity].Arguments[0].Of = "" },
		want: `names "", not slice or map`,
	},
	{
		name: "for an argument rule without an argument index",
		edit: func(s *spec.Spec) { s.Overlays[golang].Families[familyCapacity].Arguments[2].Argument = -1 },
		want: "needs a function and an argument index",
	},
	{
		name: "for an argument rule that two families state",
		edit: func(s *spec.Spec) {
			editRules(s, familyTiming, func(r *spec.Rules) {
				r.Arguments = append(r.Arguments, s.Overlays[golang].Families[familyCapacity].Arguments[0])
			})
		},
		want: `states the argument rule of "make", which capacity states too`,
	},
	{
		name: "for an overlay version that is not major.minor.patch",
		edit: func(s *spec.Spec) { editOverlay(s, func(o *spec.Overlay) { o.Version = "1.0" }) },
		want: `version "1.0" is not major.minor.patch`,
	},
	{name: "for a case that names another case", edit: func(s *spec.Spec) { editCase(s, func(c *spec.Case) { c.Case = "d" }) }, want: `names itself "d"`},
	{
		name: "for a case that does not state what it proves",
		edit: func(s *spec.Spec) { editCase(s, func(c *spec.Case) { c.Proves = "" }) },
		want: "proves and fixture must both be stated",
	},
	{
		name: "for a case without a mutant and without an error",
		edit: func(s *spec.Spec) {
			editCase(s, func(c *spec.Case) { c.Mutants = nil })
			editExpect(s, func(e *spec.Expect) { e.Mutants = nil })
		},
		want: "states neither a mutant nor an error",
	},
	{
		name: "for a case mutant of an unknown kind",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Kind = "aor-plus" },
		want: "has a kind that is not in the catalogue",
	},
	{
		name: "for a case mutant of an unknown verdict",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Verdict = "dead" },
		want: `verdict "dead", which the protocol does not define`,
	},
	{name: "for a negative nth", edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Nth = -1 }, want: "has a negative nth"},
	{
		name: "for a case mutant listed twice",
		edit: func(s *spec.Spec) { editCase(s, func(c *spec.Case) { c.Mutants = append(c.Mutants, c.Mutants[0]) }) },
		want: "f aor 0 is listed twice",
	},
	{
		name: "for an unknown run error",
		edit: func(s *spec.Spec) { editCase(s, func(c *spec.Case) { c.Errors = []string{"crash"} }) },
		want: `error "crash" is not a run error`,
	},
	{
		name: "for a language without an overlay",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Languages = []string{noOverlay} },
		want: `names language "cobol"`,
	},
	{
		name: "for a confirmed mutant of a case that does not confirm",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Confirmed = true },
		want: "f aor 0 is confirmed in a case that does not confirm",
	},
	{
		name: "for a survivor of a case that confirms without its confirmation",
		edit: func(s *spec.Spec) {
			editCase(s, func(c *spec.Case) {
				c.Confirm = true
				c.Mutants[0].Verdict = spec.Survived
			})
		},
		want: "f aor 0 has the verdict survived in a case that confirms, and is not confirmed",
	},
	{
		name: "for a mutant without coverage of a case that confirms without its confirmation",
		edit: func(s *spec.Spec) {
			editCase(s, func(c *spec.Case) {
				c.Confirm = true
				c.Mutants[0].Verdict = spec.NoCoverage
			})
		},
		want: "f aor 0 has the verdict no-coverage in a case that confirms, and is not confirmed",
	},
	{
		name: "for a confirmed suppression",
		edit: func(s *spec.Spec) {
			editCase(s, func(c *spec.Case) {
				c.Confirm = true
				c.Mutants[1].Confirmed = true
			})
		},
		want: "f ror-boundary 0 is confirmed with the verdict suppressed",
	},
	{name: "for a negative sample", edit: func(s *spec.Spec) { editCase(s, func(c *spec.Case) { c.Sample = -1 }) }, want: "sample -1 is negative"},
	{
		name: "for a mutant of a sample that is not-run",
		edit: func(s *spec.Spec) {
			editCase(s, func(c *spec.Case) {
				c.Sample = 1
				c.Mutants[0].Verdict = spec.NotRun
			})
		},
		want: "f aor 0 is not-run, and the sample of 1 runs in key order gives it the verdict of a run",
	},
	{
		name: "for a mutant beyond a sample that runs",
		edit: func(s *spec.Spec) {
			addSecondKill(s)
			editCase(s, func(c *spec.Case) { c.Sample = 1 })
		},
		want: "f ror-false 0 is killed, and the sample of 1 runs in key order gives it not-run",
	},
	{name: "for a case without a fixture", edit: func(s *spec.Spec) { delete(s.Expects, baseCase) }, want: "has no fixture in any language"},
	{
		name: "for a fixture of a language without an overlay",
		edit: func(s *spec.Spec) { s.Expects[baseCase][noOverlay] = spec.Expect{Language: noOverlay} },
		want: "cobol: no overlay for the language",
	},
	{
		name: "for an expectation that declares another language",
		edit: func(s *spec.Spec) { editExpect(s, func(e *spec.Expect) { e.Language = noOverlay }) },
		want: `expect.json declares language "cobol"`,
	},
	{
		name: "for an expected run error that the case does not state",
		edit: func(s *spec.Spec) { editExpect(s, func(e *spec.Expect) { e.Errors = []string{spec.ErrorStale} }) },
		want: `states error "stale-annotation", and case.json does not`,
	},
	{
		name: "for a skipped site of an unknown reason",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Skipped[0].Reason = "too hard" },
		want: `reason "too hard", which the overlay does not list`,
	},
	{
		name: "for a generated file listed twice",
		edit: func(s *spec.Spec) {
			editExpect(s, func(e *spec.Expect) { e.Generated = append(e.Generated, e.Generated[0]) })
		},
		want: `generated file "f_gen.go" is empty, absolute, listed twice, or has a negative count`,
	},
	{
		name: "for a generated file of a negative count",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Generated[0].Mutants = -1 },
		want: "has a negative count",
	},
	{
		name: "for a generated file that the expectation includes and the case does not",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Generated[0].Included = true },
		want: `generated file "f_gen.go" is included in one of case.json and expect.json and not in the other`,
	},
	{
		name: "for a mutant in a generated file that the run leaves out",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Generated[0].File = baseFile },
		want: "f aor 0 is in f.go, a generated file",
	},
	{
		name: "for an expected mutant that the case lacks",
		edit: func(s *spec.Spec) {
			editExpect(s, func(e *spec.Expect) {
				m := e.Mutants[0]
				m.Nth, m.Key = 1, "00000000000000aa"
				e.Mutants = append(e.Mutants, m)
			})
		},
		want: "f aor 1 is in expect.json and not in case.json",
	},
	{
		name: "for an expected mutant of another language than the case states",
		edit: func(s *spec.Spec) {
			s.Cases[baseCase].Mutants[0].Languages = []string{golang}
			s.Overlays[copied] = s.Overlays[golang]
			s.Expects[baseCase][copied] = spec.Expect{
				Language: copied,
				Mutants:  s.Expects[baseCase][golang].Mutants,
				Skipped:  []spec.Skip{},
				Errors:   []string{},
			}
		},
		want: "c, python: f aor 0 is in expect.json and not in case.json",
	},
	{
		name: "for a case mutant that the expectation lacks",
		edit: func(s *spec.Spec) { editExpect(s, func(e *spec.Expect) { e.Mutants = e.Mutants[1:] }) },
		want: "f aor 0 is in case.json and not in expect.json",
	},
	{
		name: "for a key of uppercase digits",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].Key = "0123456789ABCDEF" },
		want: "not 16 hexadecimal digits",
	},
	{
		name: "for a key of four digits",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].Key = "0123" },
		want: "not 16 hexadecimal digits",
	},
	{
		name: "for two mutants of one key",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[1].Key = "0123456789abcdef" },
		want: "share key 0123456789abcdef",
	},
	{
		name: "for an absolute file",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].File = "/" + baseFile },
		want: "file or position out of shape",
	},
	{
		name: "for an end before the start",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].End = spec.Position{Line: 3, Column: 8} },
		want: "file or position out of shape",
	},
	{
		name: "for a column of 0",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].Start = spec.Position{Line: 3, Column: 0} },
		want: "file or position out of shape",
	},
	{
		name: "for an empty original",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].Original = "" },
		want: "original or a replacement out of length",
	},
	{
		name: "for a replacement of 121 characters",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].Replacement = strings.Repeat("é", 121) },
		want: "original or a replacement out of length",
	},
	{
		name: "for a rule that the catalogue lacks",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[1].Rule = "sizing" },
		want: `rule "sizing", which the catalogue does not list`,
	},
	{
		name: "for a suppressed case mutant without a rule or a reason",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[1].Reason = "" },
		want: "is suppressed in one of case.json and expect.json",
	},
	{
		name: "for a suppression that only the case states",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Verdict = spec.Suppressed },
		want: "is suppressed in one of case.json and expect.json",
	},
	{
		name: "for a rejection that only the expectation states",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[0].NotViable = true },
		want: "is not viable in one of case.json and expect.json",
	},
	{
		name: "for a rejection that only the case states",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Verdict = spec.NotViable },
		want: "is not viable in one of case.json and expect.json",
	},
	{
		name: "for a mutant outside the selection that the case runs",
		edit: func(s *spec.Spec) { editExpect(s, func(e *spec.Expect) { e.Lines = []string{baseFile + ":10-12"} }) },
		want: "is outside the selection in one of case.json and expect.json",
	},
	{
		name: "for a mutant inside the selection that the case does not select",
		edit: func(s *spec.Spec) { s.Cases[baseCase].Mutants[0].Verdict = spec.NotSelected },
		want: "is outside the selection in one of case.json and expect.json",
	},
	{
		name: "for a mutant outside the selection that states a reason",
		edit: func(s *spec.Spec) {
			editExpect(s, func(e *spec.Expect) { e.Lines = []string{baseFile + ":3-3"} })
			s.Cases[baseCase].Mutants[1].Verdict = spec.NotSelected
		},
		want: "f ror-boundary 0 is outside the selection and states a rule, a reason or not-viable",
	},
	{
		name: "for the tests of a suppressed mutant",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[1].Tests = []string{baseTest} },
		want: "f ror-boundary 0 names tests, and its verdict suppressed names none",
	},
	{
		name: "for the covering tests of a suppressed mutant",
		edit: func(s *spec.Spec) { s.Expects[baseCase][golang].Mutants[1].CoveredBy = []string{baseTest} },
		want: "f ror-boundary 0 names covering tests, and its verdict suppressed has none",
	},
}

// base returns the repository's definition with one case whose Go fixture
// states a killed aor mutant with the test that kills it and executed its
// site, a ror-boundary mutant that an annotation suppresses, and a
// generated file that the run leaves out. Check returns an empty list for it.
func base(t *testing.T) *spec.Spec {
	t.Helper()
	s, err := spec.LoadDefinition(repository)
	assert.NoError(t, err, "LoadDefinition reads the repository's definition")
	s.Cases[baseCase] = spec.Case{
		Case: baseCase, Proves: "a rule", Fixture: "a function",
		Mutants: []spec.CaseMutant{
			{Scope: baseScope, Kind: spec.AOR, Nth: 0, Verdict: spec.Killed},
			{Scope: baseScope, Kind: spec.RORBoundary, Nth: 0, Verdict: spec.Suppressed},
		},
		Errors: []string{},
	}
	s.Expects[baseCase] = map[string]spec.Expect{golang: {
		Language: golang,
		Mutants: []spec.ExpectMutant{
			{
				Scope: baseScope, Kind: spec.AOR, Nth: 0, Key: "0123456789abcdef", File: baseFile,
				Start: spec.Position{Line: 3, Column: 9}, End: spec.Position{Line: 3, Column: 14},
				Original: "a + b", Replacement: "a - b", Tests: []string{baseTest}, CoveredBy: []string{baseTest},
			},
			{
				Scope: baseScope, Kind: spec.RORBoundary, Nth: 0, Key: "fedcba9876543210", File: baseFile,
				Start: spec.Position{Line: 4, Column: 5}, End: spec.Position{Line: 4, Column: 10},
				Original: "a < b", Replacement: "a <= b", Reason: "equal values are equivalent",
			},
		},
		Skipped: []spec.Skip{{
			File: baseFile, Start: spec.Position{Line: 6, Column: 7}, End: spec.Position{Line: 6, Column: 12},
			Reason: spec.SkipConstant,
		}},
		Generated: []spec.Generated{{File: baseGenerated, Mutants: 3}},
		Errors:    []string{},
	}}
	return s
}

// addSecondKill adds a killed ror-false mutant to the case of base, whose
// key sorts after the aor mutant's and before the ror-boundary mutant's.
func addSecondKill(s *spec.Spec) {
	editCase(s, func(c *spec.Case) {
		c.Mutants = append(c.Mutants, spec.CaseMutant{Scope: baseScope, Kind: spec.RORFalse, Nth: 0, Verdict: spec.Killed})
	})
	editExpect(s, func(e *spec.Expect) {
		e.Mutants = append(e.Mutants, spec.ExpectMutant{
			Scope: baseScope, Kind: spec.RORFalse, Nth: 0, Key: "1111111111111111", File: baseFile,
			Start: spec.Position{Line: 4, Column: 5}, End: spec.Position{Line: 4, Column: 10},
			Original: "a < b", Replacement: "false",
		})
	})
}

// editOverlay applies edit to the Go overlay of s.
func editOverlay(s *spec.Spec, edit func(*spec.Overlay)) {
	o := s.Overlays[golang]
	edit(&o)
	s.Overlays[golang] = o
}

// editRules applies edit to the rules of the family of the Go overlay of s.
func editRules(s *spec.Spec, family string, edit func(*spec.Rules)) {
	r := s.Overlays[golang].Families[family]
	edit(&r)
	s.Overlays[golang].Families[family] = r
}

// editCase applies edit to the case of base.
func editCase(s *spec.Spec, edit func(*spec.Case)) {
	c := s.Cases[baseCase]
	edit(&c)
	s.Cases[baseCase] = c
}

// editExpect applies edit to the Go expectation of the case of base.
func editExpect(s *spec.Spec, edit func(*spec.Expect)) {
	e := s.Expects[baseCase][golang]
	edit(&e)
	s.Expects[baseCase][golang] = e
}

// copyDefinition copies the repository's definition, with one case of a
// Go fixture, into a new directory, and returns it.
func copyDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{
		spec.VersionFile, spec.CatalogueFile, spec.ProtocolFile, spec.SchemaFile,
		filepath.Join(spec.OverlaysDir, golang+spec.OverlaySuffix),
		filepath.Join(spec.CorpusDir, hangCase, spec.CaseFile),
		filepath.Join(spec.CorpusDir, hangCase, golang, spec.ExpectFile),
	} {
		data, err := os.ReadFile(filepath.Join(repository, name))
		assert.NoError(t, err, "the repository's file reads")
		writeFile(t, filepath.Join(root, name), string(data))
	}
	return root
}

// writeFile writes content to path, and creates the directories of path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), dirMode), "the directory of the file is created")
	assert.NoError(t, os.WriteFile(path, []byte(content), fileMode), "the file is written")
}
