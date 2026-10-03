#!/usr/bin/env bash
# Error-path parity: run stock solc and solc-par on small crafted inputs (clean,
# warnings only, type error, parse error, code-generation error) and require
# byte-identical stdout and the same exit code.
#
#   bench/errpath.sh <solc binary>
set -u
here=$(cd "$(dirname "$0")" && pwd)
solc=$1; par=${SOLC_PAR:-$here/../solc-par}
dir=$(mktemp -d -t solc-par-errpath); cd "$dir" || exit 1
python3 - <<'EOF'
import json
ok = {"A.sol": 'pragma solidity ^0.8.0; import "./B.sol"; contract A { B b = new B(); function f() external view returns (uint) { return b.g() + 1; } }',
      "B.sol": 'pragma solidity ^0.8.0; contract B { function g() public pure returns (uint) { return 2; } }',
      "C.sol": 'pragma solidity ^0.8.0; contract C { uint public x; function h() external { x = 1; } }'}
cases = {
  "clean": ok,
  "warnings": {**ok, "C.sol": 'pragma solidity ^0.8.0; contract C { function h() external pure returns (uint) { uint unused; return 1; } }'},
  "type-error": {**ok, "C.sol": 'pragma solidity ^0.8.0; contract C { function h() external { undefinedVar = 1; } }'},
  "parse-error": {**ok, "B.sol": 'pragma solidity ^0.8.0; contract B { function g( }'},
  "stack-too-deep": {**ok, "C.sol": 'pragma solidity ^0.8.0; contract C { function h(uint a, uint b, uint c, uint d, uint e, uint f, uint g, uint hh, uint i, uint j, uint k, uint l, uint m, uint n, uint o, uint p, uint q) external pure returns (uint) { return a+b+c+d+e+f+g+hh+i+j+k+l+m+n+o+p+q; } }'},
}
sel = {"*": {"": ["ast"], "*": ["abi", "evm.bytecode.object", "evm.deployedBytecode.object", "metadata"]}}
for name, src in cases.items():
    inp = {"language": "Solidity", "sources": {f: {"content": c} for f, c in src.items()},
           "settings": {"optimizer": {"enabled": True, "runs": 200}, "outputSelection": sel}}
    json.dump(inp, open(f"{name}.json", "w"))
EOF
fail=0
for c in clean warnings type-error parse-error stack-too-deep; do
  "$solc" --standard-json < "$c.json" > "$c.stock" 2>/dev/null; sc=$?
  SOLC_PAR_SOLC="$solc" SOLC_PAR_JOBS=3 SOLC_PAR_COSTS="" "$par" --standard-json < "$c.json" > "$c.par" 2>/dev/null; pc=$?
  if [ "$sc" = "$pc" ] && cmp -s "$c.stock" "$c.par"; then
    echo "$c: identical (exit $sc)"
  else
    echo "$c: DIFFERENT (stock exit $sc, solc-par exit $pc)"; fail=1
  fi
done
rm -rf "$dir"
exit $fail
