#!/usr/bin/env python3
"""Compare both modes of run-mode.sh for one target: verdicts, wall time with
and without the mutants that ran into the timeout, and the ratio.

Usage: final.py <target> [excluded-id...]

Pass an ID to leave a mutant out of both modes, such as one that an earlier
prototype ran and the current one marks not viable."""
import json
import os
import statistics
import sys
from pathlib import Path

W = Path(os.environ.get("GOMUT_WORK") or Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "mutate-research")
target, excluded = sys.argv[1], {int(a) for a in sys.argv[2:]}


def load(mode):
    rows = [json.loads(l) for l in (W / f"work-{target}-{mode}" / f"{mode}.jsonl").read_text().splitlines() if l.strip()]
    return {r["id"]: r for r in rows if r["id"] not in excluded}


s, m = load("schemata"), load("single")
assert set(s) == set(m), "the modes ran different mutants"
for name, rows in (("schemata", s), ("single", m)):
    fast = [r["seconds"] for r in rows.values() if not r["timeout"]]
    killed = sum(r["killed"] for r in rows.values())
    not_viable = sum(r.get("not_viable", False) for r in rows.values())
    print(f"{name:9s} mutants={len(rows)} killed={killed} timeouts={sum(r['timeout'] for r in rows.values())} "
          f"survived={len(rows) - killed - not_viable} not_viable={not_viable} "
          f"total={sum(r['seconds'] for r in rows.values()):.1f}s "
          f"without_timeouts={sum(fast):.1f}s median={statistics.median(fast) * 1000:.0f}ms")
fs = sum(r["seconds"] for r in s.values() if not r["timeout"])
fm = sum(r["seconds"] for r in m.values() if not r["timeout"])
print(f"ratio without timeouts: {fm / fs:.1f}x")
differ = sorted(i for i in s if s[i]["killed"] != m[i]["killed"])
print(f"verdicts that differ: {len(differ)} {[(i, s[i]['kind'], s[i]['pos'].rsplit('/', 1)[-1]) for i in differ]}")
