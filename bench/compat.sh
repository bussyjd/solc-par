#!/usr/bin/env bash
# Compatibility check across solc versions: for each solc binary given, require that
# solc-par (1) shards a clean multi-file compile and returns byte-identical stdout and
# exit code, and (2) matches stock byte-for-byte on warnings, a type error, a parse
# error and a code-generation (stack too deep) error. Sources use the 0.5+ syntax;
# 0.4.x binaries are accepted and only checked for parity (0.4 syntax differs).
#
#   bench/compat.sh <solc binary>...
set -u
here=$(cd "$(dirname "$0")" && pwd)
par=${SOLC_PAR:-$here/../solc-par}
dir=$(mktemp -d -t solc-par-compat); cd "$dir" || exit 1
python3 - <<'EOF'
import json
p = "pragma solidity >=0.5.0 <0.9.0;"
ok = {"A.sol": f'{p} import "./B.sol"; contract A {{ B b = new B(); function f() public view returns (uint) {{ return b.g() + 1; }} }}',
      "B.sol": f'{p} contract B {{ function g() public pure returns (uint) {{ return 2; }} }}',
      "C.sol": f'{p} contract C {{ uint public x; function h() public {{ x = 1; }} }}',
      "D.sol": f'{p} import "./C.sol"; contract D is C {{ function k() public {{ x = 2; }} }}'}
params = ", ".join(f"uint p{i}" for i in range(17)); total = "+".join(f"p{i}" for i in range(17))
cases = {
  "clean": ok,
  "warnings": {**ok, "C.sol": f'{p} contract C {{ function h() public pure returns (uint) {{ uint unused; return 1; }} }}'},
  "type-error": {**ok, "C.sol": f'{p} contract C {{ function h() public {{ undefinedVar = 1; }} }}'},
  "parse-error": {**ok, "B.sol": f'{p} contract B {{ function g( }}'},
  "stack-too-deep": {**ok, "C.sol": f'{p} contract C {{ function h({params}) public pure returns (uint) {{ return {total}; }} }}'},
}
sel = {"*": {"": ["ast"], "*": ["abi", "evm.bytecode.object", "evm.deployedBytecode.object", "metadata", "evm.methodIdentifiers"]}}
for name, src in cases.items():
    json.dump({"language": "Solidity", "sources": {f: {"content": c} for f, c in src.items()},
               "settings": {"optimizer": {"enabled": True, "runs": 200}, "outputSelection": sel}}, open(f"{name}.json", "w"))
EOF
fail=0
printf "%-10s %-8s %s\n" version sharded cases
for solc in "$@"; do
  v=$("$solc" --version 2>/dev/null | sed -n 's/^Version: \([0-9.]*\).*/\1/p'); v=${v:-?}
  res=""; sharded="?"
  for c in clean warnings type-error parse-error stack-too-deep; do
    "$solc" --standard-json < "$c.json" > "$c.stock" 2>/dev/null; sc=$?
    SOLC_PAR_SOLC="$solc" SOLC_PAR_JOBS=4 SOLC_PAR_COSTS="" SOLC_PAR_DEBUG=1 "$par" --standard-json < "$c.json" > "$c.par" 2> "$c.err"; pc=$?
    if [ "$sc" = "$pc" ] && cmp -s "$c.stock" "$c.par"; then res="$res $c=ok"; else res="$res $c=DIFF(exit $sc/$pc)"; fail=1; fi
    if [ "$c" = clean ]; then
      if grep -q "compiling again" "$c.err"; then sharded=no; else sharded=yes; fi
      # 0.5+ must shard a clean compile; anything else is a regression in plan()/merge().
      case $v in 0.4.*|'?') ;; *) [ $sharded = yes ] || fail=1 ;; esac
    fi
  done
  printf "%-10s %-8s %s\n" "$v" "$sharded" "$res"
done
rm -rf "$dir"
exit $fail
