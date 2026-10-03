#!/usr/bin/env python3
"""ident.py outA outB: compare every forge artifact json of two out dirs (abi/bytecode/deployedBytecode/metadata + whole file)."""
import collections, json, pathlib, sys

a, b = map(pathlib.Path, sys.argv[1:3])
CORE = ("abi", "bytecode", "deployedBytecode", "metadata")
files = lambda r: {p.relative_to(r) for p in r.rglob("*.json") if "build-info" not in p.parts}
fa, fb = files(a), files(b)
core_diff, full_diff, nonempty = [], collections.Counter(), 0
for f in sorted(fa & fb):
    x, y = json.load(open(a / f)), json.load(open(b / f))
    nonempty += bool(x.get("bytecode", {}).get("object", "0x") not in ("0x", ""))
    if any(x.get(k) != y.get(k) for k in CORE):
        core_diff.append(str(f))
    for k in set(x) | set(y):
        full_diff[k] += x.get(k) != y.get(k)
full_diff = {k: n for k, n in full_diff.items() if n}
print(f"artifacts: A={len(fa)} B={len(fb)} common={len(fa & fb)} with_creation_bytecode={nonempty}")
print(f"only in A: {sorted(map(str, fa - fb))[:5]} only in B: {sorted(map(str, fb - fa))[:5]}")
print(f"abi/bytecode/deployedBytecode/metadata differing artifacts: {len(core_diff)} {core_diff[:5]}")
print(f"any-key differing artifacts by key: {full_diff or 'none'}")
ok = bool(fa) and fa == fb and not core_diff  # zero artifacts = failed build, never "identical"
print("IDENTICAL" if ok else "DIFFERENT")
sys.exit(not ok)
