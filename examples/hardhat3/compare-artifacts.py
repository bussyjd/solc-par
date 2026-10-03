import json, pathlib, sys
a, b = map(pathlib.Path, sys.argv[1:3])
def arts(r): return {p.relative_to(r): p for p in r.rglob("*.json") if "build-info" not in p.parts and not p.name.endswith(".dbg.json")}
A, B = arts(a), arts(b)
diff = [str(f) for f in sorted(A.keys() & B.keys()) if any(json.load(open(A[f])).get(k) != json.load(open(B[f])).get(k) for k in ("abi","bytecode","deployedBytecode","linkReferences","deployedLinkReferences"))]
def bi(r):
    out = {}
    for p in (r / "build-info").glob("*.json"):
        d = json.load(open(p)); out.update(d.get("output", {}).get("contracts", {}))
    return out
ba, bb = bi(a), bi(b)
nb = sum(1 for f in ba.values() for c in f.values() if c.get("evm", {}).get("bytecode", {}).get("object"))
print(f"artifacts: A={len(A)} B={len(B)} common={len(A.keys()&B.keys())} abi/bytecode/deployedBytecode differing: {len(diff)} {diff[:3]}")
print(f"build-info output.contracts: A={sum(map(len,ba.values()))} B={sum(map(len,bb.values()))} with_bytecode={nb} equal={ba==bb}")
ok = bool(A) and A.keys() == B.keys() and not diff and ba == bb and bool(ba)
print("IDENTICAL" if ok else "DIFFERENT"); sys.exit(not ok)
