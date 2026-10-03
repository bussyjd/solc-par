# solc-select keeps binaries at ~/.solc-select/artifacts/solc-<ver>/solc-<ver>.
# Either path works as SOLC_PAR_SOLC (both validated with 0.8.37):
export SOLC_PAR_SOLC="$HOME/.solc-select/artifacts/solc-0.8.37/solc-0.8.37"
# or the `solc` shim, which follows `solc-select use` / $SOLC_VERSION:
# export SOLC_PAR_SOLC="$(command -v solc)"; export SOLC_VERSION=0.8.37
solc-par --version        # forwards: "Version: 0.8.37+commit.f401782d..."
