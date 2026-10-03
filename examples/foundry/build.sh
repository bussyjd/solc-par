#!/usr/bin/env sh
# Foundry: three equivalent ways to make forge call solc-par instead of solc.
# Validated: openzeppelin-contracts, via-IR, solc 0.8.37 -> 27.3 s stock, 5.3 s solc-par,
# 519/519 artifacts byte-identical (bench/compare-artifacts.py).
set -eu
export SOLC_PAR_SOLC="${SOLC_PAR_SOLC:-$HOME/.svm/0.8.37/solc-0.8.37}"   # macOS: "$HOME/Library/Application Support/svm/0.8.37/solc-0.8.37"
SOLC_PAR="${SOLC_PAR:-$(command -v solc-par)}"

# 1. per invocation
forge build --use "$SOLC_PAR"

# 2. environment (same config key as `solc = "..."` in foundry.toml); also reaches
#    tools that run forge for you, e.g. slither / crytic-compile
# FOUNDRY_SOLC="$SOLC_PAR" forge build

# 3. foundry.toml (validated as [profile.fast] + FOUNDRY_PROFILE=fast, see foundry.toml here)
# [profile.default]
# solc = "/absolute/path/to/solc-par"
