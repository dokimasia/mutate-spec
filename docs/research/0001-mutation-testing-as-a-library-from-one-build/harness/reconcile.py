#!/usr/bin/env python3
"""Join each tool's btree mutants against the type-checked reference list.

The reference is $W/h2h/count-btree.jsonl, which the count command writes:
every mutant of gremlins' five default operators in the files the go command
compiles, with whether it compiles. The prototype's mutants come from its
schemata run, limited to the kinds that correspond to gremlins' operators.

Usage: reconcile.py"""
import collections
import json
import os
import re
from pathlib import Path

W = Path(os.environ.get("GOMUT_WORK") or Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "mutate-research")
H = W / "h2h"
T = "google_btree@v1.1.3"
FIVE = {"aor", "ror-boundary", "ror-negation", "uoi-incdec"}

ref = [json.loads(line) for line in (H / "count-btree.jsonl").read_text().splitlines()]
key = lambda m: (m["file"], m["line"], m["col"], m["from"], m["to"])
refs = {key(m): m for m in ref}
assert len(refs) == len(ref), "duplicate reference keys"
by_pos = collections.defaultdict(list)
for m in ref:
    by_pos[(m["file"], m["line"], m["col"])].append(m)

print(f"reference: {len(ref)} mutants in compiled files")
print("  by kind:", dict(collections.Counter(m["kind"] for m in ref)))
print("  valid:", sum(m["valid"] for m in ref), " invalid:", [(m["line"], m["from"], m["to"], m["operand"], m["error"]) for m in ref if not m["valid"]])
print("  constant expressions:", [(m["line"], m["col"], m["from"], m["to"]) for m in ref if m["constant"]])
print("  operand classes:", dict(collections.Counter((m["kind"], m["operand"]) for m in ref)))


def report(tool, found, extra_note=""):
    """found: list of reference keys, or tuples that describe a mutant outside the reference."""
    keys = [k for k in found if k in refs]
    extra = [k for k in found if k not in refs]
    dup = sum(c - 1 for c in collections.Counter(keys).values() if c > 1)
    hit = set(keys)
    missed = [refs[k] for k in refs if k not in hit]
    print(f"\n{tool}: {len(found)} mutants listed, {len(hit)} distinct reference mutants, {dup} duplicates, {len(extra)} outside the reference {extra_note}")
    reasons = collections.Counter((m["kind"], m["operand"], "constant" if m["constant"] else "runtime", "valid" if m["valid"] else "invalid") for m in missed)
    for r, c in sorted(reasons.items(), key=lambda x: -x[1]):
        print(f"  missed {c:3d}: {r}")
    for e in extra[:8]:
        print("  outside:", e)
    return missed


gomut = []
for line in (W / f"work-{T}-schemata" / "schemata.jsonl").read_text().splitlines():
    r = json.loads(line)
    if r["kind"] not in FIVE:
        continue
    path, ln, col = r["pos"].rsplit(":", 2)
    frm, to = r["mut"].split(" -> ")
    gomut.append((Path(path).name, int(ln), int(col), frm, to))
for m in report("gomut, gremlins' operators", gomut):
    print(f"    gomut missed {m['file']}:{m['line']}:{m['col']} {m['from']}->{m['to']} {m['kind']} {m['operand']} in {m['func']}")

kinds = {"ARITHMETIC_BASE": "arith", "CONDITIONALS_BOUNDARY": "boundary", "CONDITIONALS_NEGATION": "negation",
         "INCREMENT_DECREMENT": "incdec", "INVERT_NEGATIVES": "invert-neg"}
grem, grem_status, grem_files = [], collections.Counter(), collections.Counter()
for line in (H / f"gremlins-{T}.out").read_text().splitlines():
    mm = re.match(r"\s*(KILLED|LIVED|NOT COVERED|TIMED OUT|NOT VIABLE|SKIPPED) (\w+) at (\S+):(\d+):(\d+)", line)
    if not mm:
        continue
    status, typ, f, ln, col = mm.groups()
    grem_files[(f, status)] += 1
    if f not in {m["file"] for m in ref}:
        continue
    grem_status[status] += 1
    sites = by_pos[(f, int(ln), int(col))]
    kind = kinds[typ]
    # INVERT_NEGATIVES and ARITHMETIC_BASE both rewrite a '-' token to '+'.
    match = [m for m in sites if m["kind"] == kind or (m["from"] == "-" and kind in ("arith", "invert-neg"))]
    grem.append(key(match[0]) if match else (f, int(ln), int(col), typ, "no reference site"))
print("\ngremlins statuses by file:", dict(grem_files))
print("gremlins statuses in compiled files:", dict(grem_status))
report("gremlins", grem)

mut = []
for line in (H / f"mutest-go120-{T}.out").read_text().splitlines():
    if line.startswith("{") and '"status"' in line:
        r = json.loads(line)
        mut.append((r["file"], r["line"], r["column"], r["original"], r["mutated"]))
report("mutest", mut, "(boundary for <,<=,>,>= and negation for ==,!= only)")

doc = json.loads((H / f"kanly-{T}.out").read_text())
shared = {"int_arith", "int_cmp_boundary", "int_cmp_negate", "inc_dec"}
kan = []
for r in doc["mutants"]:
    mu = r["mutation"]
    if mu["operator"] in shared:
        kan.append((Path(mu["file"]).name, mu["line"], mu["column"], mu["original"], mu["mutant"]))
print("\nkanly shared-operator statuses:", dict(collections.Counter(r["status"] for r in doc["mutants"] if r["mutation"]["operator"] in shared)))
report("kanly (shared operators only)", kan)
