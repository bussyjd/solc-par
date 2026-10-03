# crytic-compile platforms and solc-par

crytic-compile (used by Slither, Echidna and Medusa) chooses a platform from the target.
Observed with `solc-trace` (crytic-compile 0.4.2):

| command                                             | platform  | solc invocation                                                       | sharded |
|-----------------------------------------------------|-----------|-----------------------------------------------------------------------|---------|
| `crytic-compile <foundry dir>`                      | Foundry   | `forge build --build-info --deny never --skip ./test/** ./script/** --force` → `solc --standard-json` (all sources inlined) | yes, with `FOUNDRY_SOLC=solc-par` |
| `crytic-compile <hardhat dir>`                      | Hardhat   | `npx hardhat compile --force` → `solc --standard-json --no-import-callback` | yes, with the Hardhat config in `../hardhat3/` or `../hardhat2/` |
| `crytic-compile Token.sol --solc solc-par`          | solc      | `solc Token.sol --combined-json abi,ast,bin,bin-runtime,srcmap,srcmap-runtime,userdoc,devdoc,hashes --allow-paths …` | no, forwarded to the real solc |
| `crytic-compile <dir> --solc-standard-json --solc solc-par` | Solc-json | `solc --standard-json --allow-paths <cwd>`, `sources` = only the directory's files, imports via solc's import callback | no: shards see sources that were not in the input, solc-par recompiles once with stock solc (correct, slower than stock) |

Export with `--export-format solc --export-dir <dir>` and diff the JSON to check identity;
both the `solc` and `Solc-json` routes produced equal exports (JSON-equal after parsing)
with the real solc and with solc-par.

```sh
export SOLC_PAR_SOLC=~/.svm/0.8.37/solc-0.8.37
FOUNDRY_SOLC=/path/to/solc-par crytic-compile . --export-format solc   # Foundry project, sharded
crytic-compile Token.sol --solc /path/to/solc-par --export-format solc   # single file, passthrough
```
