#!/usr/bin/env sh
# Slither / crytic-compile (validated with slither 0.11.6, crytic-compile 0.4.2).
# Slither never calls solc for Foundry/Hardhat projects itself: it runs `forge build`
# or `npx hardhat compile`, so solc-par is configured in that tool, not in Slither.
set -eu
export SOLC_PAR_SOLC="${SOLC_PAR_SOLC:-$HOME/.svm/0.8.37/solc-0.8.37}"
SOLC_PAR="${SOLC_PAR:-$(command -v solc-par)}"

# Foundry project (validated on openzeppelin-contracts: 34.2 s -> 19.4 s, identical findings)
FOUNDRY_SOLC="$SOLC_PAR" slither .

# Hardhat project: use examples/hardhat3 or examples/hardhat2 config, then just
# slither .

# Plain .sol files, no framework: --solc + --solc-standard-json. crytic-compile lists
# only the directory's own files in `sources` and lets solc's import callback load the
# rest, so this is sharded ONLY when the directory has no outside imports; otherwise
# solc-par detects the extra sources and recompiles once with stock solc (correct,
# slower than stock; SOLC_PAR_DEBUG=1 says so). Without --solc-standard-json
# crytic-compile runs `solc --combined-json` per file, which solc-par forwards to stock
# solc unchanged (correct, but no speed-up). Whole projects: use the Foundry/Hardhat route.
slither contracts/ --solc "$SOLC_PAR" --solc-standard-json

# crytic-compile directly takes the same flags:
# crytic-compile contracts/ --solc "$SOLC_PAR" --solc-standard-json
