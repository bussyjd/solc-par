#!/usr/bin/env sh
# Direct use from a script: solc-par is `solc --standard-json` with four env vars.
# Validated: 3-file input, stdout byte-identical to stock solc 0.8.37 (sha256 equal),
# same exit code; SOLC_PAR_JOBS=1 and `--version` forward to stock unchanged.
set -eu
: "${SOLC_PAR_SOLC:=$HOME/.svm/0.8.37/solc-0.8.37}"   # required: the real solc
export SOLC_PAR_SOLC
export SOLC_PAR_JOBS="${SOLC_PAR_JOBS:-$(getconf _NPROCESSORS_ONLN)}"  # shards; 1 = no sharding
export SOLC_PAR_COSTS="${SOLC_PAR_COSTS:-./.solc-par-costs.json}"      # learned sizes; "" disables
export SOLC_PAR_DEBUG=1                                                 # say on stderr why a fallback happened

solc-par --standard-json < input.json > output.json

# parity check against stock:
# "$SOLC_PAR_SOLC" --standard-json < input.json | cmp - output.json && echo identical
