// Copyright ThesmOS B.V. 2026
// SPDX-License-Identifier: MIT

// Package spec loads the mutate-spec definition from a checkout and checks
// every file against the rules the definition states for it.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Catalogue is spec/catalogue.json: the operator classes, their kinds, the
// rule families, the key's prefix, the annotation that suppresses mutants,
// and the directive that makes a generated file a target.
type Catalogue struct {
	Key        string   `json:"key"`
	Annotation string   `json:"annotation"`
	Include    string   `json:"include"`
	Classes    []Class  `json:"classes"`
	Kinds      []Kind   `json:"kinds"`
	Families   []Family `json:"families"`
}

// Class is one operator class and the kinds it contains, in catalogue order.
type Class struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
}

// Kind is one kind of mutant.
type Kind struct {
	ID          string `json:"id"`
	Class       string `json:"class"`
	Description string `json:"description"`
}

// Family is one rule family of suppressed code.
type Family struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Protocol is spec/protocol.json: the record's name and version, the
// environment variable that every run of the suite sets, the variable that
// every run of the instrumented program sets, the verdicts with their place
// in the score, the run errors and the limits.
type Protocol struct {
	Record       RecordName `json:"record"`
	Variable     string     `json:"variable"`
	Instrumented string     `json:"instrumented"`
	Verdicts     []Verdict  `json:"verdicts"`
	Errors       []string   `json:"errors"`
	Limits       Limits     `json:"limits"`
}

// RecordName names the record format and its version.
type RecordName struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Verdict is one verdict and how the score counts it: detected,
// undetected or excluded.
type Verdict struct {
	ID    string `json:"id"`
	Score string `json:"score"`
}

// Limits are the deadline and the memory ceiling of one mutant's run, each
// a factor of the opening control run's measurement plus a constant.
type Limits struct {
	Deadline struct {
		Factor  float64 `json:"factor"`
		Seconds float64 `json:"seconds"`
	} `json:"deadline"`
	Memory struct {
		Factor float64 `json:"factor"`
		Bytes  int64   `json:"bytes"`
	} `json:"memory"`
}

// Overlay is spec/overlays/<language>.json: how the catalogue maps onto
// one language, and the overlay's semantic version. Variables states which
// calls of a variable a result rule puts into its family, and Compound which
// compound statements a family that lists calls suppresses as a whole.
type Overlay struct {
	Language  string              `json:"language"`
	Version   string              `json:"version"`
	Comment   string              `json:"comment"`
	Generated string              `json:"generated"`
	Files     []string            `json:"files"`
	Scopes    []string            `json:"scopes"`
	Tokens    string              `json:"tokens"`
	Zero      string              `json:"zero"`
	Kinds     map[string][]string `json:"kinds"`
	Skips     []SkipRule          `json:"skips"`
	Families  Families            `json:"families"`
	Variables []string            `json:"variables"`
	Compound  []string            `json:"compound"`
}

// SkipRule is one reason the language's engine lists a site of a catalogue
// class in the record's skipped entries instead of making its mutants, and
// the sites the reason applies to.
type SkipRule struct {
	Reason string `json:"reason"`
	Where  string `json:"where"`
}

// Families lists, per rule family, the APIs whose calls are suppressed.
// Methods lists the methods that belong to a family by their name and
// signature on any type of one kind, because libraries declare them anew.
// Results lists the result rules, which put the calls of a variable that
// holds a result of a family's API into the family.
type Families struct {
	Logging  []string   `json:"logging"`
	Timing   []string   `json:"timing"`
	Flags    []string   `json:"flags"`
	Capacity []Capacity `json:"capacity"`
	Helper   []string   `json:"helper"`
	Methods  []Method   `json:"methods"`
	Results  []Result   `json:"results"`
}

// Calls returns the call families, logging, timing, flags and helper, by
// family.
func (f Families) Calls() map[string][]string {
	return map[string][]string{"logging": f.Logging, "timing": f.Timing, "flags": f.Flags, "helper": f.Helper}
}

// Method puts a call of the method Name with the signature Signature, on a
// value of any type of the kind On, into Family. On is interface: the call
// selects the method of an interface.
type Method struct {
	Family    string `json:"family"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
	On        string `json:"on"`
}

// Result puts a call of a variable into Family when the variable holds only
// results of the type Type that calls of the family's APIs returned, as the
// overlay's variables rules state. Type is written as go/types writes a
// type, such as context.CancelFunc.
type Result struct {
	Family string `json:"family"`
	Type   string `json:"type"`
}

// Capacity is one argument that only sizes an allocation. Func is a
// function's full name, or the builtin make, whose Of names the allocated
// kind: slice or map. Argument is the argument's 0-based index.
type Capacity struct {
	Func     string `json:"func"`
	Of       string `json:"of,omitempty"`
	Argument int    `json:"argument"`
}

// Case is spec/corpus/<case>/case.json: what every language's fixture of
// the case must produce, stated once. Confirm is true for a case whose run
// confirms each survivor, and each mutant whose site never executed, in the
// mutant's ordinary build.
type Case struct {
	Case    string       `json:"case"`
	Confirm bool         `json:"confirm,omitempty"`
	Proves  string       `json:"proves"`
	Fixture string       `json:"fixture"`
	Mutants []CaseMutant `json:"mutants"`
	Errors  []string     `json:"errors"`
}

// CaseMutant identifies a mutant in every language by its scope, its kind
// and Nth, the site's 0-based position among the sites of the same scope
// and kind in source order, and states its verdict. Confirmed is true for
// a mutant whose verdict the run of its ordinary build decided: a survivor
// of its run, or a mutant whose site never executed. Languages restricts
// the mutant to the fixtures of the languages it lists. Empty means every
// language whose overlay defines the kind.
type CaseMutant struct {
	Scope     string   `json:"scope"`
	Kind      string   `json:"kind"`
	Nth       int      `json:"nth"`
	Verdict   string   `json:"verdict"`
	Confirmed bool     `json:"confirmed,omitempty"`
	Languages []string `json:"languages,omitempty"`
}

// ID returns the mutant's identity within its case.
func (m CaseMutant) ID() string { return fmt.Sprintf("%s %s %d", m.Scope, m.Kind, m.Nth) }

// In reports whether the mutant exists in the fixture of language, whose
// overlay is o.
func (m CaseMutant) In(language string, o Overlay) bool {
	if len(m.Languages) > 0 {
		for _, l := range m.Languages {
			if l == language {
				return true
			}
		}
		return false
	}
	_, defined := o.Kinds[m.Kind]
	return defined
}

// Expect is spec/corpus/<case>/<language>/expect.json: what one language's
// record of the case must contain, beyond the verdicts. Lines is the run's
// selection, and Suite names the fixture's other targets whose tests the
// run adds to the target's own. The target is the fixture's root.
type Expect struct {
	Language  string         `json:"language"`
	Lines     []string       `json:"lines,omitempty"`
	Suite     []string       `json:"suite,omitempty"`
	Mutants   []ExpectMutant `json:"mutants"`
	Skipped   []Skip         `json:"skipped"`
	Generated []Generated    `json:"generated"`
	Errors    []string       `json:"errors"`
}

// Generated is a generated file of the target that the run leaves out, and
// the number of mutants that the catalogue's kinds make at its sites.
type Generated struct {
	File    string `json:"file"`
	Mutants int    `json:"mutants"`
}

// ExpectMutant is one mutant as one language's record states it. Tests,
// where it is not empty, lists the tests that the record names for the
// mutant, in any order, and CoveredBy, where it is not empty, the tests that
// the record's coveredBy names, in any order. The enumeration does not
// decide either, so both are written by hand from the fixture's tests.
type ExpectMutant struct {
	Scope       string   `json:"scope"`
	Kind        string   `json:"kind"`
	Nth         int      `json:"nth"`
	Occurrence  int      `json:"occurrence"`
	Key         string   `json:"key"`
	File        string   `json:"file"`
	Start       Position `json:"start"`
	End         Position `json:"end"`
	Original    string   `json:"original"`
	Replacement string   `json:"replacement"`
	Rule        string   `json:"rule,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	NotViable   bool     `json:"notViable,omitempty"`
	Tests       []string `json:"tests,omitempty"`
	CoveredBy   []string `json:"coveredBy,omitempty"`
}

// ID returns the mutant's identity within its case.
func (m ExpectMutant) ID() string { return fmt.Sprintf("%s %s %d", m.Scope, m.Kind, m.Nth) }

// Position is a 1-based line and a 1-based column that counts bytes of UTF-8.
type Position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Before reports whether p is strictly before q.
func (p Position) Before(q Position) bool {
	return p.Line < q.Line || p.Line == q.Line && p.Column < q.Column
}

// Skip is one site of a catalogue class that has no mutant, with the reason.
type Skip struct {
	File   string   `json:"file"`
	Start  Position `json:"start"`
	End    Position `json:"end"`
	Reason string   `json:"reason"`
}

// Spec is a loaded checkout of the definition.
type Spec struct {
	Root      string
	Version   string
	Catalogue Catalogue
	Protocol  Protocol
	// Schema is spec/record.schema.json, decoded without a type.
	Schema   map[string]any
	Overlays map[string]Overlay
	Cases    map[string]Case
	// Expects maps a case to its fixtures' expectations by language.
	Expects map[string]map[string]Expect
}

// Load reads the definition and the corpus under root. A file that does
// not decode into its type, or that names a field its type does not have,
// fails the load.
func Load(root string) (*Spec, error) {
	s, err := LoadDefinition(root)
	if err != nil {
		return nil, err
	}
	return s, s.loadCorpus()
}

// LoadDefinition reads VERSION, the catalogue, the protocol, the record
// schema and the overlays under root, and not the corpus.
func LoadDefinition(root string) (*Spec, error) {
	s := &Spec{Root: root, Overlays: map[string]Overlay{}, Cases: map[string]Case{}, Expects: map[string]map[string]Expect{}}
	version, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return nil, err
	}
	s.Version = strings.TrimSpace(string(version))
	if err := decode(filepath.Join(root, "spec", "catalogue.json"), &s.Catalogue); err != nil {
		return nil, err
	}
	if err := decode(filepath.Join(root, "spec", "protocol.json"), &s.Protocol); err != nil {
		return nil, err
	}
	if err := decode(filepath.Join(root, "spec", "record.schema.json"), &s.Schema); err != nil {
		return nil, err
	}
	overlays, err := filepath.Glob(filepath.Join(root, "spec", "overlays", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range overlays {
		var o Overlay
		if err := decode(path, &o); err != nil {
			return nil, err
		}
		s.Overlays[strings.TrimSuffix(filepath.Base(path), ".json")] = o
	}
	return s, nil
}

// loadCorpus reads every case under spec/corpus and the expectations of
// each language's fixture.
func (s *Spec) loadCorpus() error {
	cases, err := os.ReadDir(filepath.Join(s.Root, "spec", "corpus"))
	if err != nil {
		return err
	}
	for _, entry := range cases {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.Root, "spec", "corpus", entry.Name())
		var c Case
		if err := decode(filepath.Join(dir, "case.json"), &c); err != nil {
			return err
		}
		s.Cases[entry.Name()] = c
		languages, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, l := range languages {
			if !l.IsDir() {
				continue
			}
			var e Expect
			if err := decode(filepath.Join(dir, l.Name(), "expect.json"), &e); err != nil {
				return err
			}
			if s.Expects[entry.Name()] == nil {
				s.Expects[entry.Name()] = map[string]Expect{}
			}
			s.Expects[entry.Name()][l.Name()] = e
		}
	}
	return nil
}

// Fixture returns the directory of a case's fixture in a language.
func (s *Spec) Fixture(name, language string) string {
	return filepath.Join(s.Root, "spec", "corpus", name, language)
}

func decode(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if dec.More() {
		return fmt.Errorf("%s: more than one JSON value", path)
	}
	return nil
}

// ErrProblems reports that a check found problems.
var ErrProblems = errors.New("the definition breaks its rules")

// Check returns every problem the definition has, sorted. An empty result
// means that every file meets every rule.
func (s *Spec) Check() []string {
	var p problems
	s.checkCatalogue(&p)
	s.checkProtocol(&p)
	s.checkSchema(&p)
	s.checkOverlays(&p)
	s.checkCases(&p)
	sort.Strings(p)
	return p
}

type problems []string

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func (s *Spec) kinds() map[string]Kind {
	k := map[string]Kind{}
	for _, kind := range s.Catalogue.Kinds {
		k[kind.ID] = kind
	}
	return k
}

func (s *Spec) families() map[string]bool {
	f := map[string]bool{}
	for _, family := range s.Catalogue.Families {
		f[family.ID] = true
	}
	return f
}

func (s *Spec) verdicts() map[string]string {
	v := map[string]string{}
	for _, verdict := range s.Protocol.Verdicts {
		v[verdict.ID] = verdict.Score
	}
	return v
}

func (s *Spec) checkCatalogue(p *problems) {
	c := s.Catalogue
	if c.Key == "" {
		p.add("catalogue: key is empty")
	}
	if c.Annotation == "" {
		p.add("catalogue: annotation is empty")
	}
	if c.Include == "" || c.Include == c.Annotation {
		p.add("catalogue: include is empty or the annotation's name")
	}
	classes := map[string]Class{}
	for _, class := range c.Classes {
		if _, dup := classes[class.ID]; dup {
			p.add("catalogue: class %q is listed twice", class.ID)
		}
		classes[class.ID] = class
	}
	members := map[string][]string{}
	seen := map[string]bool{}
	for _, kind := range c.Kinds {
		if seen[kind.ID] {
			p.add("catalogue: kind %q is listed twice", kind.ID)
		}
		seen[kind.ID] = true
		if _, ok := classes[kind.Class]; !ok {
			p.add("catalogue: kind %q names class %q, which the catalogue does not list", kind.ID, kind.Class)
		}
		if kind.Description == "" {
			p.add("catalogue: kind %q has no description", kind.ID)
		}
		members[kind.Class] = append(members[kind.Class], kind.ID)
	}
	for _, class := range c.Classes {
		if strings.Join(class.Kinds, ",") != strings.Join(members[class.ID], ",") {
			p.add("catalogue: class %q lists kinds %v, and the kinds that name it are %v", class.ID, class.Kinds, members[class.ID])
		}
	}
	families := map[string]bool{}
	for _, family := range c.Families {
		if families[family.ID] {
			p.add("catalogue: family %q is listed twice", family.ID)
		}
		families[family.ID] = true
	}
}

func (s *Spec) checkProtocol(p *problems) {
	pr := s.Protocol
	if pr.Record.Name == "" || pr.Record.Version < 1 {
		p.add("protocol: the record needs a name and a version of at least 1")
	}
	if pr.Variable == "" || pr.Instrumented == "" || pr.Variable == pr.Instrumented {
		p.add("protocol: variable and instrumented must name two variables")
	}
	seen := map[string]bool{}
	for _, v := range pr.Verdicts {
		if seen[v.ID] {
			p.add("protocol: verdict %q is listed twice", v.ID)
		}
		seen[v.ID] = true
		switch v.Score {
		case "detected", "undetected", "excluded":
		default:
			p.add("protocol: verdict %q counts as %q, which is not detected, undetected or excluded", v.ID, v.Score)
		}
	}
	errs := map[string]bool{}
	for _, e := range pr.Errors {
		if errs[e] {
			p.add("protocol: error %q is listed twice", e)
		}
		errs[e] = true
	}
	l := pr.Limits
	if l.Deadline.Factor <= 0 || l.Deadline.Seconds < 0 || l.Memory.Factor <= 0 || l.Memory.Bytes < 0 {
		p.add("protocol: a limit's factor must be positive and its constant not negative")
	}
}

// checkSchema compares the record schema's enumerations and constants with
// the catalogue and the protocol, so that the schema cannot drift from them.
func (s *Spec) checkSchema(p *problems) {
	defs, _ := s.Schema["$defs"].(map[string]any)
	enum := func(name string) []string {
		def, _ := defs[name].(map[string]any)
		values, _ := def["enum"].([]any)
		out := make([]string, 0, len(values))
		for _, v := range values {
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	want := func(name string, got, from []string) {
		if strings.Join(got, ",") != strings.Join(from, ",") {
			p.add("record schema: $defs.%s enumerates %v, and the definition has %v", name, got, from)
		}
	}
	var kinds, verdicts, families []string
	for _, k := range s.Catalogue.Kinds {
		kinds = append(kinds, k.ID)
	}
	for _, v := range s.Protocol.Verdicts {
		verdicts = append(verdicts, v.ID)
	}
	for _, f := range s.Catalogue.Families {
		families = append(families, f.ID)
	}
	want("kind", enum("kind"), kinds)
	want("verdict", enum("verdict"), verdicts)
	want("family", enum("family"), families)
	want("error", enum("error"), s.Protocol.Errors)
	props, _ := s.Schema["properties"].(map[string]any)
	constant := func(name string) any {
		prop, _ := props[name].(map[string]any)
		return prop["const"]
	}
	if constant("record") != s.Protocol.Record.Name {
		p.add("record schema: properties.record.const is %v, and the protocol names the record %q", constant("record"), s.Protocol.Record.Name)
	}
	if v, _ := constant("version").(float64); int(v) != s.Protocol.Record.Version {
		p.add("record schema: properties.version.const is %v, and the protocol's record version is %d", constant("version"), s.Protocol.Record.Version)
	}
}

func (s *Spec) checkOverlays(p *problems) {
	kinds := s.kinds()
	for name, o := range s.Overlays {
		if o.Language != name {
			p.add("overlay %s.json: declares language %q", name, o.Language)
		}
		if o.Comment == "" {
			p.add("overlay %s: comment is empty", name)
		}
		if len(o.Compound) == 0 {
			p.add("overlay %s: compound states no rule", name)
		}
		for kind, rules := range o.Kinds {
			if _, ok := kinds[kind]; !ok {
				p.add("overlay %s: kind %q is not in the catalogue", name, kind)
			}
			if len(rules) == 0 {
				p.add("overlay %s: kind %q has no rules", name, kind)
			}
		}
		skips := map[string]bool{}
		for _, rule := range o.Skips {
			if rule.Reason == "" || rule.Where == "" || skips[rule.Reason] {
				p.add("overlay %s: skip reason %q is empty, listed twice, or does not say where it applies", name, rule.Reason)
			}
			skips[rule.Reason] = true
		}
		for family, calls := range o.Families.Calls() {
			seen := map[string]bool{}
			for _, call := range calls {
				if call == "" || seen[call] {
					p.add("overlay %s: %s lists %q twice or empty", name, family, call)
				}
				seen[call] = true
			}
		}
		for _, c := range o.Families.Capacity {
			if c.Func == "" || c.Argument < 0 {
				p.add("overlay %s: a capacity entry needs a function and an argument index", name)
			}
			if c.Func == "make" && c.Of != "slice" && c.Of != "map" {
				p.add("overlay %s: capacity entry for make names %q, not slice or map", name, c.Of)
			}
		}
		families := s.families()
		for _, m := range o.Families.Methods {
			if !families[m.Family] || m.Name == "" || m.Signature == "" || m.On != "interface" {
				p.add("overlay %s: method %q needs a family of the catalogue, a name, a signature and on: interface", name, m.Name)
			}
		}
		calls := o.Families.Calls()
		rules := map[Result]bool{}
		for _, r := range o.Families.Results {
			if _, lists := calls[r.Family]; !lists || !families[r.Family] || !strings.Contains(r.Type, ".") || rules[r] {
				p.add("overlay %s: result rule %q of %q needs a family of the catalogue that lists calls, a type of a package, and no twin", name, r.Type, r.Family)
			}
			rules[r] = true
		}
		if len(o.Families.Results) > 0 && len(o.Variables) == 0 {
			p.add("overlay %s: states result rules and no variables rule", name)
		}
		if !semver.MatchString(o.Version) {
			p.add("overlay %s: version %q is not major.minor.patch", name, o.Version)
		}
	}
}

// semver matches a semantic version without a pre-release or build part.
var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func (s *Spec) checkCases(p *problems) {
	kinds := s.kinds()
	families := s.families()
	verdicts := s.verdicts()
	runErrors := map[string]bool{}
	for _, e := range s.Protocol.Errors {
		runErrors[e] = true
	}
	for name, c := range s.Cases {
		if c.Case != name {
			p.add("case %s: case.json names itself %q", name, c.Case)
		}
		if c.Proves == "" || c.Fixture == "" {
			p.add("case %s: proves and fixture must both be stated", name)
		}
		if len(c.Mutants) == 0 && len(c.Errors) == 0 {
			p.add("case %s: states neither a mutant nor an error", name)
		}
		ids := map[string]CaseMutant{}
		for _, m := range c.Mutants {
			if _, ok := kinds[m.Kind]; !ok {
				p.add("case %s: %s has a kind that is not in the catalogue", name, m.ID())
			}
			if _, ok := verdicts[m.Verdict]; !ok {
				p.add("case %s: %s has verdict %q, which the protocol does not define", name, m.ID(), m.Verdict)
			}
			if m.Nth < 0 {
				p.add("case %s: %s has a negative nth", name, m.ID())
			}
			if _, dup := ids[m.ID()]; dup {
				p.add("case %s: %s is listed twice", name, m.ID())
			}
			for _, l := range m.Languages {
				if o, ok := s.Overlays[l]; !ok || o.Kinds[m.Kind] == nil {
					p.add("case %s: %s names language %q, which has no overlay that defines the kind", name, m.ID(), l)
				}
			}
			// Under confirmation every survivor of a run, and every mutant
			// whose site never executed, is confirmed. A confirmed mutant has
			// the verdict of a run of its ordinary build, no-coverage when
			// that run passes for a mutant whose site never executed, or
			// not-viable when the toolchain rejects that build.
			if m.Confirmed && !c.Confirm {
				p.add("case %s: %s is confirmed in a case that does not confirm", name, m.ID())
			}
			if c.Confirm && (m.Verdict == "survived" || m.Verdict == "no-coverage") && !m.Confirmed {
				p.add("case %s: %s has the verdict %s in a case that confirms, and is not confirmed", name, m.ID(), m.Verdict)
			}
			switch m.Verdict {
			case "killed", "timed-out", "exhausted", "survived", "no-coverage", "not-viable", "error":
			default:
				if m.Confirmed {
					p.add("case %s: %s is confirmed with the verdict %s, which no run of a build gives", name, m.ID(), m.Verdict)
				}
			}
			ids[m.ID()] = m
		}
		for _, e := range c.Errors {
			if !runErrors[e] {
				p.add("case %s: error %q is not a run error the protocol defines", name, e)
			}
		}
		if len(s.Expects[name]) == 0 {
			p.add("case %s: has no fixture in any language", name)
		}
		for language, e := range s.Expects[name] {
			s.checkExpect(p, name, language, c, ids, e, families)
		}
	}
}

func (s *Spec) checkExpect(p *problems, name, language string, c Case, ids map[string]CaseMutant, e Expect, families map[string]bool) {
	where := fmt.Sprintf("case %s, %s", name, language)
	o, ok := s.Overlays[language]
	if !ok {
		p.add("%s: no overlay for the language", where)
		return
	}
	if e.Language != language {
		p.add("%s: expect.json declares language %q", where, e.Language)
	}
	errs := map[string]bool{}
	for _, err := range c.Errors {
		errs[err] = true
	}
	for _, err := range e.Errors {
		if !errs[err] {
			p.add("%s: expect.json states error %q, and case.json does not", where, err)
		}
	}
	skips := map[string]bool{}
	for _, rule := range o.Skips {
		skips[rule.Reason] = true
	}
	for _, sk := range e.Skipped {
		if !skips[sk.Reason] {
			p.add("%s: skipped site at %s:%d has reason %q, which the overlay does not list", where, sk.File, sk.Start.Line, sk.Reason)
		}
	}
	generated := map[string]bool{}
	for _, g := range e.Generated {
		if g.File == "" || strings.HasPrefix(g.File, "/") || g.Mutants < 0 || generated[g.File] {
			p.add("%s: generated file %q is empty, absolute, listed twice, or has a negative count", where, g.File)
		}
		generated[g.File] = true
	}
	seen := map[string]bool{}
	keys := map[string]string{}
	for _, m := range e.Mutants {
		want, ok := ids[m.ID()]
		if !ok || !want.In(language, o) {
			p.add("%s: %s is in expect.json and not in case.json for the language", where, m.ID())
			continue
		}
		seen[m.ID()] = true
		if len(m.Key) != 16 || strings.Trim(m.Key, "0123456789abcdef") != "" {
			p.add("%s: %s has key %q, not 16 hexadecimal digits", where, m.ID(), m.Key)
		}
		if other, dup := keys[m.Key]; dup {
			p.add("%s: %s and %s share key %s", where, m.ID(), other, m.Key)
		}
		keys[m.Key] = m.ID()
		if m.File == "" || strings.HasPrefix(m.File, "/") || m.End.Before(m.Start) || m.Start.Line < 1 || m.Start.Column < 1 {
			p.add("%s: %s has a file or position out of shape", where, m.ID())
		}
		if generated[m.File] {
			p.add("%s: %s is in %s, a generated file that the run leaves out", where, m.ID(), m.File)
		}
		if n := len([]rune(m.Original)); n == 0 || n > 120 || len([]rune(m.Replacement)) > 120 {
			p.add("%s: %s has an original or a replacement out of length", where, m.ID())
		}
		if m.Rule != "" && !families[m.Rule] {
			p.add("%s: %s has rule %q, which the catalogue does not list", where, m.ID(), m.Rule)
		}
		// A mutant outside the selection is not-selected, whatever else
		// excludes it. Inside it, a suppressed mutant is suppressed, and
		// otherwise a mutant that the toolchain rejects is not-viable.
		selected := Selected(e.Lines, m.File, m.Start.Line)
		suppressed := m.Rule != "" || m.Reason != ""
		if !selected != (want.Verdict == "not-selected") {
			p.add("%s: %s is outside the selection in one of case.json and expect.json and not in the other", where, m.ID())
		}
		if !selected && (suppressed || m.NotViable) {
			p.add("%s: %s is outside the selection and states a rule, a reason or not-viable", where, m.ID())
		}
		if selected && suppressed != (want.Verdict == "suppressed") {
			p.add("%s: %s is suppressed in one of case.json and expect.json and not in the other", where, m.ID())
		}
		if selected && !suppressed && m.NotViable != (want.Verdict == "not-viable") {
			p.add("%s: %s is not viable in one of case.json and expect.json and not in the other", where, m.ID())
		}
		switch want.Verdict {
		case "killed", "timed-out", "exhausted":
		default:
			if len(m.Tests) > 0 {
				p.add("%s: %s names tests, and its verdict %s names none", where, m.ID(), want.Verdict)
			}
		}
		// Only a mutant whose site the instrumented program executed has
		// tests that executed it.
		switch want.Verdict {
		case "killed", "timed-out", "exhausted", "survived":
		default:
			if len(m.CoveredBy) > 0 {
				p.add("%s: %s names covering tests, and its verdict %s has none", where, m.ID(), want.Verdict)
			}
		}
	}
	for id, m := range ids {
		if !seen[id] && m.In(language, o) {
			p.add("%s: %s is in case.json and not in expect.json", where, id)
		}
	}
}

// Selected reports whether a site that starts on line of file lies in the
// selection, a list of file:first-last entries. An empty selection selects
// every line.
func Selected(lines []string, file string, line int) bool {
	if len(lines) == 0 {
		return true
	}
	for _, entry := range lines {
		f, r, ok := strings.Cut(entry, ":")
		if !ok || f != file {
			continue
		}
		var first, last int
		if _, err := fmt.Sscanf(r, "%d-%d", &first, &last); err == nil && first <= line && line <= last {
			return true
		}
	}
	return false
}
