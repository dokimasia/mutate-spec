// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// base returns the repository's definition with one case whose Go fixture
// states a killed aor mutant with the test that kills it and executed its
// site, and a ror-boundary mutant that an annotation suppresses. Check finds
// no problem in it.
func base(t *testing.T) *Spec {
	t.Helper()
	s, err := LoadDefinition("../../..")
	if err != nil {
		t.Fatal(err)
	}
	s.Cases["c"] = Case{
		Case: "c", Proves: "a rule", Fixture: "a function",
		Mutants: []CaseMutant{
			{Scope: "f", Kind: "aor", Nth: 0, Verdict: "killed"},
			{Scope: "f", Kind: "ror-boundary", Nth: 0, Verdict: "suppressed"},
		},
		Errors: []string{},
	}
	s.Expects["c"] = map[string]Expect{"go": {
		Language: "go",
		Mutants: []ExpectMutant{
			{Scope: "f", Kind: "aor", Nth: 0, Key: "0123456789abcdef", File: "f.go", Start: Position{3, 9}, End: Position{3, 14}, Original: "a + b", Replacement: "a - b", Tests: []string{"TestF"}, CoveredBy: []string{"TestF"}},
			{Scope: "f", Kind: "ror-boundary", Nth: 0, Key: "fedcba9876543210", File: "f.go", Start: Position{4, 5}, End: Position{4, 10}, Original: "a < b", Replacement: "a <= b", Reason: "equal values are equivalent"},
		},
		Skipped:   []Skip{{File: "f.go", Start: Position{6, 7}, End: Position{6, 12}, Reason: "constant expression"}},
		Generated: []Generated{{File: "f_gen.go", Mutants: 3}},
		Errors:    []string{},
	}}
	return s
}

func TestCheckPassesTheDefinition(t *testing.T) {
	if problems := base(t).Check(); len(problems) != 0 {
		t.Errorf("Check() = %q, want no problem", problems)
	}
}

func TestCheckFindsEachRuleBroken(t *testing.T) {
	expect := func(s *Spec) *Expect {
		e := s.Expects["c"]["go"]
		return &e
	}
	store := func(s *Spec, e *Expect) { s.Expects["c"]["go"] = *e }
	cases := []struct {
		name   string
		break_ func(s *Spec)
		want   string
	}{
		{"empty key", func(s *Spec) { s.Catalogue.Key = "" }, "catalogue: key is empty"},
		{"empty annotation", func(s *Spec) { s.Catalogue.Annotation = "" }, "catalogue: annotation is empty"},
		{"empty include", func(s *Spec) { s.Catalogue.Include = "" }, "catalogue: include is empty"},
		{"include named as the annotation", func(s *Spec) { s.Catalogue.Include = s.Catalogue.Annotation }, "include is empty or the annotation's name"},
		{"empty variable", func(s *Spec) { s.Protocol.Variable = "" }, "protocol: variable and instrumented must name two variables"},
		{"empty instrumented", func(s *Spec) { s.Protocol.Instrumented = "" }, "protocol: variable and instrumented must name two variables"},
		{"one variable twice", func(s *Spec) { s.Protocol.Instrumented = s.Protocol.Variable }, "protocol: variable and instrumented must name two variables"},
		{"method family", func(s *Spec) { s.Overlays["go"].Families.Methods[0].Family = "sizing" }, `method "Helper" needs a family of the catalogue`},
		{"method kind", func(s *Spec) { s.Overlays["go"].Families.Methods[0].On = "struct" }, "and on: interface"},
		{"class twice", func(s *Spec) { s.Catalogue.Classes = append(s.Catalogue.Classes, s.Catalogue.Classes[0]) }, `class "aor" is listed twice`},
		{"kind twice", func(s *Spec) { s.Catalogue.Kinds = append(s.Catalogue.Kinds, s.Catalogue.Kinds[0]) }, `kind "aor" is listed twice`},
		{"unknown class", func(s *Spec) { s.Catalogue.Kinds[0].Class = "xyz" }, `names class "xyz"`},
		{"class kinds", func(s *Spec) { s.Catalogue.Classes[1].Kinds = s.Catalogue.Classes[1].Kinds[1:] }, `class "ror" lists kinds`},
		{"no description", func(s *Spec) { s.Catalogue.Kinds[2].Description = "" }, "has no description"},
		{"family twice", func(s *Spec) { s.Catalogue.Families = append(s.Catalogue.Families, s.Catalogue.Families[0]) }, `family "logging" is listed twice`},
		{"record name", func(s *Spec) { s.Protocol.Record.Version = 0 }, "a version of at least 1"},
		{"verdict twice", func(s *Spec) { s.Protocol.Verdicts = append(s.Protocol.Verdicts, s.Protocol.Verdicts[0]) }, `verdict "killed" is listed twice`},
		{"verdict score", func(s *Spec) { s.Protocol.Verdicts[0].Score = "maybe" }, `counts as "maybe"`},
		{"error twice", func(s *Spec) { s.Protocol.Errors = append(s.Protocol.Errors, "load") }, `error "load" is listed twice`},
		{"limit factor", func(s *Spec) { s.Protocol.Limits.Memory.Factor = 0 }, "a limit's factor must be positive"},
		{"schema kinds", func(s *Spec) {
			s.Catalogue.Kinds = s.Catalogue.Kinds[:14]
			s.Catalogue.Classes[4].Kinds = []string{"sbr-delete"}
		}, "$defs.kind enumerates"},
		{"schema verdicts", func(s *Spec) { s.Protocol.Verdicts[1].ID = "late" }, "$defs.verdict enumerates"},
		{"schema families", func(s *Spec) { s.Catalogue.Families[3].ID = "sizing" }, "$defs.family enumerates"},
		{"schema errors", func(s *Spec) { s.Protocol.Errors = s.Protocol.Errors[1:] }, "$defs.error enumerates"},
		{"schema record", func(s *Spec) { s.Protocol.Record.Name = "other" }, "properties.record.const"},
		{"schema version", func(s *Spec) { s.Protocol.Record.Version = 2 }, "properties.version.const"},
		{"overlay language", func(s *Spec) { o := s.Overlays["go"]; o.Language = "golang"; s.Overlays["go"] = o }, `declares language "golang"`},
		{"overlay comment", func(s *Spec) { o := s.Overlays["go"]; o.Comment = ""; s.Overlays["go"] = o }, "comment is empty"},
		{"overlay kind", func(s *Spec) { s.Overlays["go"].Kinds["aor-plus"] = []string{"x"} }, `kind "aor-plus" is not in the catalogue`},
		{"overlay rules", func(s *Spec) { s.Overlays["go"].Kinds["aor"] = nil }, `kind "aor" has no rules`},
		{"skip where", func(s *Spec) { s.Overlays["go"].Skips[0].Where = "" }, "does not say where it applies"},
		{"skip twice", func(s *Spec) { o := s.Overlays["go"]; o.Skips = append(o.Skips, o.Skips[0]); s.Overlays["go"] = o }, "listed twice"},
		{"family call twice", func(s *Spec) {
			o := s.Overlays["go"]
			o.Families.Timing = append(o.Families.Timing, o.Families.Timing[0])
			s.Overlays["go"] = o
		}, `timing lists "context.WithDeadline" twice`},
		{"overlay version", func(s *Spec) { o := s.Overlays["go"]; o.Version = "1.0"; s.Overlays["go"] = o }, `version "1.0" is not major.minor.patch`},
		{"result family", func(s *Spec) { s.Overlays["go"].Families.Results[0].Family = "capacity" }, `result rule "context.CancelFunc" of "capacity" needs a family`},
		{"result type", func(s *Spec) { s.Overlays["go"].Families.Results[0].Type = "CancelFunc" }, `result rule "CancelFunc" of "timing" needs`},
		{"result twice", func(s *Spec) {
			o := s.Overlays["go"]
			o.Families.Results = append(o.Families.Results, o.Families.Results[0])
			s.Overlays["go"] = o
		}, "and no twin"},
		{"variables", func(s *Spec) { o := s.Overlays["go"]; o.Variables = nil; s.Overlays["go"] = o }, "states result rules and no variables rule"},
		{"capacity make", func(s *Spec) { s.Overlays["go"].Families.Capacity[0].Of = "" }, "names \"\", not slice or map"},
		{"capacity argument", func(s *Spec) { s.Overlays["go"].Families.Capacity[2].Argument = -1 }, "needs a function and an argument index"},
		{"case name", func(s *Spec) { c := s.Cases["c"]; c.Case = "d"; s.Cases["c"] = c }, `names itself "d"`},
		{"case proves", func(s *Spec) { c := s.Cases["c"]; c.Proves = ""; s.Cases["c"] = c }, "proves and fixture must both be stated"},
		{"case empty", func(s *Spec) {
			c := s.Cases["c"]
			c.Mutants = nil
			s.Cases["c"] = c
			e := expect(s)
			e.Mutants = nil
			store(s, e)
		}, "states neither a mutant nor an error"},
		{"case kind", func(s *Spec) { s.Cases["c"].Mutants[0].Kind = "aor-plus" }, "has a kind that is not in the catalogue"},
		{"case verdict", func(s *Spec) { s.Cases["c"].Mutants[0].Verdict = "dead" }, `verdict "dead", which the protocol does not define`},
		{"case nth", func(s *Spec) { s.Cases["c"].Mutants[0].Nth = -1 }, "has a negative nth"},
		{"case twice", func(s *Spec) { c := s.Cases["c"]; c.Mutants = append(c.Mutants, c.Mutants[0]); s.Cases["c"] = c }, "f aor 0 is listed twice"},
		{"case error", func(s *Spec) { c := s.Cases["c"]; c.Errors = []string{"crash"}; s.Cases["c"] = c }, `error "crash" is not a run error`},
		{"case language", func(s *Spec) { s.Cases["c"].Mutants[0].Languages = []string{"cobol"} }, `names language "cobol"`},
		{"confirmed without confirmation", func(s *Spec) { s.Cases["c"].Mutants[0].Confirmed = true }, "f aor 0 is confirmed in a case that does not confirm"},
		{"survivor not confirmed", func(s *Spec) {
			c := s.Cases["c"]
			c.Confirm = true
			c.Mutants[0].Verdict = "survived"
			s.Cases["c"] = c
		}, "f aor 0 has the verdict survived in a case that confirms, and is not confirmed"},
		{"uncovered not confirmed", func(s *Spec) {
			c := s.Cases["c"]
			c.Confirm = true
			c.Mutants[0].Verdict = "no-coverage"
			s.Cases["c"] = c
		}, "f aor 0 has the verdict no-coverage in a case that confirms, and is not confirmed"},
		{"confirmed suppression", func(s *Spec) {
			c := s.Cases["c"]
			c.Confirm = true
			c.Mutants[1].Confirmed = true
			s.Cases["c"] = c
		}, "f ror-boundary 0 is confirmed with the verdict suppressed"},
		{"no fixture", func(s *Spec) { delete(s.Expects, "c") }, "has no fixture in any language"},
		{"no overlay", func(s *Spec) { s.Expects["c"]["cobol"] = Expect{Language: "cobol"} }, "cobol: no overlay for the language"},
		{"expect language", func(s *Spec) { e := expect(s); e.Language = "rust"; store(s, e) }, `expect.json declares language "rust"`},
		{"expect error", func(s *Spec) { e := expect(s); e.Errors = []string{"stale-annotation"}; store(s, e) }, `states error "stale-annotation", and case.json does not`},
		{"skip reason", func(s *Spec) { e := expect(s); e.Skipped[0].Reason = "too hard"; store(s, e) }, `reason "too hard", which the overlay does not list`},
		{"generated file twice", func(s *Spec) {
			e := expect(s)
			e.Generated = append(e.Generated, e.Generated[0])
			store(s, e)
		}, `generated file "f_gen.go" is empty, absolute, listed twice, or has a negative count`},
		{"generated count", func(s *Spec) { s.Expects["c"]["go"].Generated[0].Mutants = -1 }, "has a negative count"},
		{"mutant in a generated file", func(s *Spec) { s.Expects["c"]["go"].Generated[0].File = "f.go" }, "f aor 0 is in f.go, a generated file"},
		{"extra mutant", func(s *Spec) {
			e := expect(s)
			m := e.Mutants[0]
			m.Nth, m.Key = 1, "00000000000000aa"
			e.Mutants = append(e.Mutants, m)
			store(s, e)
		}, "f aor 1 is in expect.json and not in case.json"},
		{"other language", func(s *Spec) {
			s.Cases["c"].Mutants[0].Languages = []string{"go"}
			s.Overlays["python"] = s.Overlays["go"]
			s.Expects["c"]["python"] = Expect{Language: "python", Mutants: s.Expects["c"]["go"].Mutants, Skipped: []Skip{}, Errors: []string{}}
		}, "c, python: f aor 0 is in expect.json and not in case.json"},
		{"missing mutant", func(s *Spec) { e := expect(s); e.Mutants = e.Mutants[1:]; store(s, e) }, "f aor 0 is in case.json and not in expect.json"},
		{"key digits", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].Key = "0123456789ABCDEF" }, "not 16 hexadecimal digits"},
		{"key length", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].Key = "0123" }, "not 16 hexadecimal digits"},
		{"shared key", func(s *Spec) { s.Expects["c"]["go"].Mutants[1].Key = "0123456789abcdef" }, "share key 0123456789abcdef"},
		{"absolute file", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].File = "/f.go" }, "file or position out of shape"},
		{"end before start", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].End = Position{3, 8} }, "file or position out of shape"},
		{"column zero", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].Start = Position{3, 0} }, "file or position out of shape"},
		{"original empty", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].Original = "" }, "original or a replacement out of length"},
		{"replacement long", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].Replacement = strings.Repeat("é", 121) }, "original or a replacement out of length"},
		{"rule", func(s *Spec) { s.Expects["c"]["go"].Mutants[1].Rule = "sizing" }, `rule "sizing", which the catalogue does not list`},
		{"suppressed", func(s *Spec) { s.Expects["c"]["go"].Mutants[1].Reason = "" }, "is suppressed in one of case.json and expect.json"},
		{"suppressed verdict", func(s *Spec) { s.Cases["c"].Mutants[0].Verdict = "suppressed" }, "is suppressed in one of case.json and expect.json"},
		{"not viable", func(s *Spec) { s.Expects["c"]["go"].Mutants[0].NotViable = true }, "is not viable in one of case.json and expect.json"},
		{"not viable verdict", func(s *Spec) { s.Cases["c"].Mutants[0].Verdict = "not-viable" }, "is not viable in one of case.json and expect.json"},
		{"outside selection", func(s *Spec) { e := expect(s); e.Lines = []string{"f.go:10-12"}; store(s, e) }, "is outside the selection in one of case.json and expect.json"},
		{"not selected verdict", func(s *Spec) { s.Cases["c"].Mutants[0].Verdict = "not-selected" }, "is outside the selection in one of case.json and expect.json"},
		{"suppressed outside the selection", func(s *Spec) {
			e := expect(s)
			e.Lines = []string{"f.go:3-3"}
			store(s, e)
			s.Cases["c"].Mutants[1].Verdict = "not-selected"
		}, "f ror-boundary 0 is outside the selection and states a rule, a reason or not-viable"},
		{"tests of a suppressed mutant", func(s *Spec) { s.Expects["c"]["go"].Mutants[1].Tests = []string{"TestF"} }, "f ror-boundary 0 names tests, and its verdict suppressed names none"},
		{"covering tests of a suppressed mutant", func(s *Spec) { s.Expects["c"]["go"].Mutants[1].CoveredBy = []string{"TestF"} }, "f ror-boundary 0 names covering tests, and its verdict suppressed has none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base(t)
			c.break_(s)
			problems := s.Check()
			for _, p := range problems {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Errorf("Check() = %q, want a problem containing %q", problems, c.want)
		})
	}
}

func TestLoadRejectsAFieldTheTypeLacks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"VERSION", "spec/catalogue.json", "spec/protocol.json", "spec/record.schema.json", "spec/overlays/go.json"} {
		data, err := os.ReadFile(filepath.Join("../../..", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "spec/protocol.json" {
			data = []byte(strings.Replace(string(data), `"verdicts"`, `"verdict": [], "verdicts"`, 1))
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := LoadDefinition(root)
	if err == nil || !strings.Contains(err.Error(), `unknown field "verdict"`) {
		t.Errorf("LoadDefinition() error = %v, want one naming the unknown field", err)
	}
}

func TestSelectedMatchesFileAndLineRange(t *testing.T) {
	lines := []string{"a.go:3-5", "b.go:10-10"}
	cases := []struct {
		file string
		line int
		want bool
	}{
		{"a.go", 2, false}, {"a.go", 3, true}, {"a.go", 5, true}, {"a.go", 6, false},
		{"b.go", 10, true}, {"b.go", 11, false}, {"c.go", 4, false},
	}
	for _, c := range cases {
		if got := Selected(lines, c.file, c.line); got != c.want {
			t.Errorf("Selected(%q, %s, %d) = %v, want %v", lines, c.file, c.line, got, c.want)
		}
	}
	if !Selected(nil, "a.go", 1) {
		t.Error("Selected(nil, ...) = false, want true: no selection selects every line")
	}
}
