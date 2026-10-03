#!/usr/bin/env sh
# Validated with ape 0.8.52, ape-solidity 0.8.6, py-solc-x 2.0.5: .build/__local__.json
# (abi, deploymentBytecode, runtimeBytecode, sourcemap, devdoc, userdoc, ...) identical to
# a stock compile. solcx sees solc-v0.8.37 already present and skips the download; other
# versions your pragmas select are downloaded into the same directory as usual.
set -eu
export SOLCX_BINARY_PATH="$(cd "$(dirname "$0")" && pwd)/solcx-bin"
ape compile --force
