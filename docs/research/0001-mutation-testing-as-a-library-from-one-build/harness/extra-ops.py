#!/usr/bin/env python3
"""What kanly's operators beyond gremlins' five add on btree.

For every kanly operator: mutants, killed, survived, not covered. For each
survivor of an operator outside the five: whether a mutant of the five
survived on the same line, or in the same function. A survivor with neither
points at a test gap that the five operators do not show. The survivors of
the five come from the prototype's schemata run on btree.

Usage: extra-ops.py"""
import collections
import json
import os
from pathlib import Path

W = Path(os.environ.get("GOMUT_WORK") or Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "mutate-research")
T = "google_btree@v1.1.3"
FIVE = {"aor", "ror-boundary", "ror-negation", "uoi-incdec"}
doc = json.loads((W / "h2h" / f"kanly-{T}.out").read_text())
shared = {"int_arith", "int_cmp_boundary", "int_cmp_negate", "inc_dec"}

ref = {(m["line"], m["col"], m["from"], m["to"]): m for m in
       (json.loads(l) for l in (W / "h2h" / "count-btree.jsonl").read_text().splitlines())}
five_survived_lines, five_survived_funcs = set(), set()
for line in (W / f"work-{T}-schemata" / "schemata.jsonl").read_text().splitlines():
    r = json.loads(line)
    if r["kind"] not in FIVE or r["killed"]:
        continue
    _, ln, col = r["pos"].rsplit(":", 2)
    frm, to = r["mut"].split(" -> ")
    five_survived_lines.add(int(ln))
    five_survived_funcs.add(ref[(int(ln), int(col), frm, to)]["func"])

stats = collections.defaultdict(collections.Counter)
new_gap = collections.defaultdict(list)
for r in doc["mutants"]:
    mu = r["mutation"]
    op = mu["operator"]
    stats[op][r["status"]] += 1
    if op in shared or r["status"] != "survived":
        continue
    # kanly names a method without its receiver; the reference writes node.insert.
    fn = mu["function"]
    if mu["line"] in five_survived_lines:
        where = "same line as a five-operator survivor"
    elif fn in {f.split(".")[-1] for f in five_survived_funcs}:
        where = "same function as a five-operator survivor"
    else:
        where = "no five-operator survivor in the function"
    new_gap[where].append((op, mu["line"], mu["original"], mu["mutant"], mu["function"]))

print(f"{'operator':18s} {'total':>5s} {'killed':>6s} {'survived':>8s} {'not cov':>7s}")
for op, c in sorted(stats.items(), key=lambda x: (x[0] not in shared, -sum(x[1].values()))):
    print(f"{op:18s} {sum(c.values()):5d} {c['killed']:6d} {c['survived']:8d} {c['not_covered']:7d}")
print()
for where, rows in new_gap.items():
    print(f"{where}: {len(rows)}")
print("\nsurvivors with no five-operator survivor in their function:")
for row in sorted(new_gap["no five-operator survivor in the function"], key=lambda x: x[1]):
    print("  ", row)
