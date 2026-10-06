// Command measure checks the mutants of one Go package in one of two modes and
// writes each mutant's verdict and wall time as JSON lines:
//
//	schemata: one test binary with every mutant behind GOMUT_ACTIVE, run once
//	          per mutant
//	single:   one go test build and run per mutant, as gremlins and ooze do
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"example.com/gomut"
)

type line struct {
	ID        int     `json:"id"`
	Kind      string  `json:"kind"`
	Pos       string  `json:"pos"`
	Mut       string  `json:"mut"`
	Killed    bool    `json:"killed"`
	Timeout   bool    `json:"timeout"`
	NotViable bool    `json:"not_viable,omitempty"`
	Seconds   float64 `json:"seconds"`
}

func main() {
	pkgDir := flag.String("pkg", ".", "package directory")
	mode := flag.String("mode", "schemata", "schemata or single")
	work := flag.String("work", "", "scratch directory for overlays and the test binary")
	every := flag.Int("every", 1, "run every k-th mutant only; the schemata build still holds all of them")
	dry := flag.Bool("dry", false, "schemata mode: build, run the control and forced-failure runs, and stop")
	flag.Parse()
	if err := run(*pkgDir, *mode, *work, *every, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		os.Exit(2)
	}
}

func run(pkgDir, mode, work string, every int, dry bool) error {
	start := time.Now()
	p, err := gomut.Load(pkgDir)
	if err != nil {
		return err
	}
	fmt.Printf("package %s at %s: %d mutants, loaded in %s\n", p.Name, p.Dir, len(p.Mutants), time.Since(start).Round(time.Millisecond))
	fmt.Printf("mutants by kind: %s; not run because go vet rejects them: %d\n", gomut.Kinds(p.Mutants), p.Stillborn)
	var sample []gomut.Mutant
	for i, m := range p.Mutants {
		if i%every == 0 {
			sample = append(sample, m)
		}
	}
	if every > 1 {
		fmt.Printf("running every %dth mutant: %d of %d (%s)\n", every, len(sample), len(p.Mutants), gomut.Kinds(sample))
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	env := os.Environ()

	t := time.Now()
	failed, _, out, err := gomut.Run(p.Dir, env, 10*time.Minute, "go", "test", "-count=1", ".")
	if err != nil || failed {
		return fmt.Errorf("baseline go test failed: %v\n%s", err, out)
	}
	baseline := time.Since(t)
	timeout := gomut.Timeout(baseline)
	fmt.Printf("baseline go test (build cached, tests run): %s; mutant timeout %s\n", baseline.Round(time.Millisecond), timeout.Round(time.Millisecond))

	results, err := os.Create(filepath.Join(work, mode+".jsonl"))
	if err != nil {
		return err
	}
	defer results.Close()
	enc := json.NewEncoder(results)

	var killed, timedOut, notViable int
	record := func(m gomut.Mutant, failed, to, nv bool, d time.Duration) {
		switch {
		case nv:
			notViable++
		case failed:
			killed++
		}
		if to {
			timedOut++
		}
		_ = enc.Encode(line{ID: m.ID, Kind: m.Kind, Pos: m.Pos.String(), Mut: m.From + " -> " + m.To, Killed: failed && !nv, Timeout: to, NotViable: nv, Seconds: d.Seconds()})
	}

	phase := time.Now()
	switch mode {
	case "schemata":
		t := time.Now()
		ov, err := p.Schemata(work)
		if err != nil {
			return err
		}
		gen := time.Since(t)
		bin := filepath.Join(work, "pkg.test")
		t = time.Now()
		failed, _, out, err := gomut.Run(p.Dir, env, 10*time.Minute, "go", "test", "-c", "-overlay", ov, "-o", bin, ".")
		if err != nil || failed {
			return fmt.Errorf("schemata build failed: %v\n%s", err, out)
		}
		build := time.Since(t)
		t = time.Now()
		failed, _, out, err = gomut.Run(p.Dir, append(env, "GOMUT_ACTIVE=0"), timeout, bin, "-test.count=1")
		if err != nil || failed {
			return fmt.Errorf("schemata control run failed: %v\n%s", err, out)
		}
		control := time.Since(t)
		// Every switch panics under GOMUT_ACTIVE=-1. A run that still passes
		// executed no instrumented code, and every mutant would survive.
		forced, _, _, err := gomut.Run(p.Dir, append(env, "GOMUT_ACTIVE=-1"), timeout, bin, "-test.count=1", "-test.failfast")
		if err != nil {
			return err
		}
		if !forced {
			return fmt.Errorf("schemata forced-failure run passed: the instrumented code did not run")
		}
		fmt.Printf("schemata: generated in %s, built once in %s, control run %s, forced-failure run failed as required\n", gen.Round(time.Millisecond), build.Round(time.Millisecond), control.Round(time.Millisecond))
		if dry {
			return nil
		}
		for _, m := range sample {
			t := time.Now()
			failed, to, _, err := gomut.Run(p.Dir, append(env, "GOMUT_ACTIVE="+strconv.Itoa(m.ID)), timeout, bin, "-test.count=1", "-test.failfast", "-test.timeout="+timeout.String())
			if err != nil {
				return fmt.Errorf("mutant %d: %w", m.ID, err)
			}
			record(m, failed, to, false, time.Since(t))
		}
	case "single":
		for _, m := range sample {
			ov, err := p.Single(work, m)
			if err != nil {
				return err
			}
			t := time.Now()
			failed, to, out, err := gomut.Run(p.Dir, env, timeout, "go", "test", "-count=1", "-failfast", "-overlay", ov, "-timeout="+timeout.String(), ".")
			if err != nil {
				return fmt.Errorf("mutant %d: %w", m.ID, err)
			}
			nv := failed && !to && (bytes.Contains(out, []byte("[build failed]")) || bytes.Contains(out, []byte("[setup failed]")))
			if nv {
				fmt.Printf("mutant %d (%s at %s) does not build:\n%s\n", m.ID, m.Kind, m.Pos, out)
			}
			record(m, failed, to, nv, time.Since(t))
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	total := time.Since(phase)
	n := len(sample)
	fmt.Printf("%s: %d mutants, %d killed (%d by timeout), %d survived, %d not viable, in %s, %s per mutant\n",
		mode, n, killed, timedOut, n-killed-notViable, notViable, total.Round(time.Millisecond), (total / time.Duration(max(n, 1))).Round(time.Millisecond))
	return nil
}
