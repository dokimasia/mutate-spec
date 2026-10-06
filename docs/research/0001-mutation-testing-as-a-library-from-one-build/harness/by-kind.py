#!/usr/bin/env python3
"""Mutants, kills, survivors and timeouts per operator kind, and per class,
for one run of run-mode.sh.

Usage: by-kind.py <target> [mode]"""
import collections
import json
import os
import statistics
import sys
from pathlib import Path

W = Path(os.environ.get("GOMUT_WORK") or Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "mutate-research")
target = sys.argv[1]
mode = sys.argv[2] if len(sys.argv) > 2 else "schemata"
rows = [json.loads(l) for l in (W / f"work-{target}-{mode}" / f"{mode}.jsonl").read_text().splitlines() if l.strip()]
by = collections.defaultdict(list)
for r in rows:
    by[r["kind"]].append(r)
    prefix = r["kind"].split("-")[0]
    if prefix != r["kind"]:
        by[prefix].append(r)
print(f"{target} {mode}: {len(rows)} mutants")
print(f"{'kind':14s} {'total':>5s} {'killed':>6s} {'survived':>8s} {'timeout':>7s} {'not viable':>10s} {'survival':>8s} {'median ms':>9s}")
for k in sorted(by):
    rs = by[k]
    killed = sum(r["killed"] for r in rs)
    nv = sum(r.get("not_viable", False) for r in rs)
    surv = len(rs) - killed - nv
    to = sum(r["timeout"] for r in rs)
    med = statistics.median(r["seconds"] for r in rs) * 1000
    print(f"{k:14s} {len(rs):5d} {killed:6d} {surv:8d} {to:7d} {nv:10d} {surv / len(rs):8.1%} {med:9.0f}")
