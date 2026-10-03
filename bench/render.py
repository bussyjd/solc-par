#!/usr/bin/env python3
"""render.py results.tsv...: print a markdown table of median wall times per project/variant and the speed-up."""
import collections, statistics, sys

t = collections.defaultdict(list)
for f in sys.argv[1:]:
    for line in open(f):
        p, v, r, s, rc = line.rstrip("\n").split("\t")
        if p != "project" and rc == "0":
            t[(p, v)].append(float(s))
projects = sorted({p for p, _ in t})
print("| project | stock | solc-par cold | solc-par warm | speed-up (warm) |")
print("|---|--:|--:|--:|--:|")
for p in projects:
    med = lambda v: statistics.median(t[(p, v)]) if t[(p, v)] else float("nan")
    n = lambda v: len(t[(p, v)])
    print(f"| {p} | {med('stock'):.1f} s (n={n('stock')}) | {med('par-cold'):.1f} s | {med('par-warm'):.1f} s (n={n('par-warm')}) | {med('stock') / med('par-warm'):.1f}× |")
