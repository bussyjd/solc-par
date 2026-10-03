# Using solc-par with common Solidity tools

## Quick start

```sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"   # the real solc (macOS svm: "$HOME/Library/Application Support/svm/0.8.37/solc-0.8.37")
# then point the tool's "solc path" at solc-par:
FOUNDRY_SOLC=/path/to/solc-par forge build             # Foundry, and Slither/Echidna/Medusa on a Foundry project
#   Hardhat: solidity.path = "/path/to/solc-par" (v3) or the subtask override (v2), see below
# optional: SOLC_PAR_JOBS=<shards, default CPUs>  SOLC_PAR_COSTS=<file|"">  SOLC_PAR_DEBUG=1
```

That is the whole integration. solc-par is a drop-in `solc`: it shards a `--standard-json`
compile across cores and merges the result; anything else (`--version`, `--combined-json`,
the legacy CLI) is forwarded to the real solc untouched. Output, errors and exit codes are
solc's. How the sharding works is drawn in [ARCHITECTURE.md](ARCHITECTURE.md); copy-ready
configs are under [`examples/`](examples/).

| the tool runs                                                        | solc-par does                                                             | speed-up |
|----------------------------------------------------------------------|---------------------------------------------------------------------------|----------|
| `solc --standard-json` with all sources inlined (forge, hardhat, ape)| shards by source file, merges                                             | **yes**  |
| `solc --standard-json` whose imports are left to solc's import callback | runs the shards, sees sources it did not plan for, recompiles once with stock solc (correct, slower than stock) | no |
| `solc --combined-json …`, `solc file.sol`, `solc --version`          | forwards to the real solc                                                 | no       |

All results below: 2026-10-02, solc 0.8.37, 12-core Apple M4 Pro, every build pointed at a
scratch output dir and compared artifact-by-artifact with a stock build of the same input.
Two independent validation passes ran; where both measured the same thing, both numbers are
given.

## Foundry

Validated with forge 1.8.3. forge inlines every source into one standard-json input, so
every `forge build` is sharded. Three equivalent ways to select solc-par
([`examples/foundry/build.sh`](examples/foundry/build.sh),
[`examples/foundry/foundry.toml`](examples/foundry/foundry.toml)):

```sh
# 1. one-off
forge build --use /path/to/solc-par

# 2. environment: every forge command in the shell (build / test / script / coverage),
#    and every tool that runs forge for you (slither, crytic-compile, echidna, medusa)
export FOUNDRY_SOLC=/path/to/solc-par

# 3. foundry.toml: the `solc` key (alias solc_version) accepts a path; a profile keeps
#    the stock build as the default
[profile.default]
solc = "0.8.37"
[profile.fast]
solc = "/path/to/solc-par"
```

```sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
FOUNDRY_PROFILE=fast forge build
```

`FOUNDRY_SOLC` is the environment form of the `solc` key and overrides the project's
`solc_version` (openzeppelin pins `0.8.35`; the log still says `Compiling 421 files with
Solc 0.8.37` because forge probes the version with `solc --version`, which solc-par
forwards to the real binary). `SOLC_PAR_SOLC` has to be exported: foundry.toml cannot set
environment variables, so it goes in the shell profile, `.envrc` or the CI step.

Validated on openzeppelin-contracts (`forge build --force`, `FOUNDRY_VIA_IR=true`,
`FOUNDRY_OUT` / `FOUNDRY_CACHE_PATH` in a scratch dir; `forge config --json` checked that
`solc`, `via_ir` and `out` resolved as intended for both the env var and the profile):

| run                                                      | wall, pass 1 | wall, pass 2 | artifacts vs stock (`bench/compare-artifacts.py`)   |
|----------------------------------------------------------|-------------:|-------------:|-----------------------------------------------------|
| `FOUNDRY_SOLC=<real solc>`                               | 30.1 s       | 27.3 s       | reference                                           |
| `FOUNDRY_SOLC=solc-par`, first build (no learned costs)  | 4.6 s        | 5.3 s        | IDENTICAL: 519/519 artifacts, 243 with creation bytecode, 0 differing keys |
| `FOUNDRY_SOLC=solc-par`, second build                    | 4.7 s        | 5.2 s        | IDENTICAL                                           |
| `[profile.fast] solc = ".../solc-par"`, `FOUNDRY_PROFILE=fast` | 8.5 s ¹ | –          | IDENTICAL                                           |

¹ includes forge startup and the profile's `via_ir = true` re-resolution; validated with
`FOUNDRY_CONFIG` pointing at a scratch copy of foundry.toml plus the `[profile.fast]`
section, the project file untouched. `forge build --force --use <solc-par>` was run once
(not timed under controlled conditions): same `Compiling 421 files with Solc 0.8.37`,
artifacts IDENTICAL to the `FOUNDRY_SOLC` build.

Caveats: forge's incremental cache is unaffected (solc-par only speeds up the compiles
forge actually runs; `--force` was used to measure). forge passes `--base-path`, so the
learned-costs file is keyed per project; the first and second builds were within 0.1 s of
each other on this project.

## Hardhat

Hardhat inlines all sources and runs `<solc> --standard-json --no-import-callback`, so its
compiles are sharded. Hardhat reads the compiler version from `<solc> --version`, which
solc-par forwards, so `version` in the config must be the real binary's version.
`SOLC_PAR_SOLC` is one variable per environment: a project that compiles with several
solc versions in one run needs a two-line wrapper per version
(`#!/bin/sh` / `SOLC_PAR_SOLC=/path/to/solc-0.8.20 exec /path/to/solc-par "$@"`) and
points each compiler entry at its wrapper.

### Hardhat 3 — `solidity.path`

Validated with 3.18.1. Every compiler entry (`solidity`, `compilers: [...]`,
`overrides`, `profiles.<name>.compilers[i]`) accepts `path`; Hardhat then skips the
download and runs that binary. Full file:
[`examples/hardhat3/hardhat.config.js`](examples/hardhat3/hardhat.config.js).

```js
// hardhat.config.js (ESM; package.json has "type": "module")
export default {
  solidity: {
    version: "0.8.37",
    path: "/path/to/solc-par",
    settings: { optimizer: { enabled: true, runs: 200 } },
  },
};
```

```sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
npx hardhat compile
```

Validated on a scratch project holding the 367 files of `openzeppelin-contracts/contracts`
(optimizer 200, evmVersion osaka, legacy codegen), `npx hardhat compile --force`:

| run                              | wall   | artifacts vs stock ([`examples/hardhat3/compare-artifacts.py`](examples/hardhat3/compare-artifacts.py)) |
|----------------------------------|-------:|--------------------------------------------------------------------------------------------------------|
| `path: <real solc>`              | 6.0 s  | reference                                                                                              |
| `path: solc-par`, first build    | 2.3 s  | IDENTICAL: 445/445 artifacts, 0 differing abi / bytecode / deployedBytecode / linkReferences; build-info `output.contracts` (189 with bytecode) deep-equal |
| `path: solc-par`, second build   | 2.2 s  | IDENTICAL                                                                                              |

12 shard processes were counted per compile (`SOLC_PAR_JOBS` unset, 12 CPUs). A second
pass on a three-contract project (optimizer + viaIR) gave the same result: `diff -r` of the
two `artifacts/` trees differs only in `buildInfoId`, because Hardhat 3 hashes the compiler
path into the build-info id; `input`, `output.contracts`, `output.sources` and
`output.errors` are equal. Hardhat 3 does not use the Hardhat 2 subtask below.

### Hardhat 2 — `TASK_COMPILE_SOLIDITY_GET_SOLC_BUILD`

Validated with 2.29.1. Hardhat 2 has no `path` field; override the subtask that resolves
the compiler build. Full file:
[`examples/hardhat2/hardhat.config.js`](examples/hardhat2/hardhat.config.js).

```js
// hardhat.config.js (CommonJS)
const { subtask } = require("hardhat/config");
const { TASK_COMPILE_SOLIDITY_GET_SOLC_BUILD } = require("hardhat/builtin-tasks/task-names");

subtask(TASK_COMPILE_SOLIDITY_GET_SOLC_BUILD, async (args, hre, runSuper) => {
  if (args.solcVersion !== "0.8.37") return runSuper();     // other versions: Hardhat's download
  return {
    compilerPath: "/path/to/solc-par",
    isSolcJs: false,
    version: args.solcVersion,
    longVersion: "0.8.37+commit.f401782d",                    // only written into build-info
  };
});

module.exports = { solidity: { version: "0.8.37", settings: { optimizer: { enabled: true, runs: 200 } } } };
```

Validated on the same 367 files, `npx hardhat compile --force`:

| run                                    | wall   | artifacts vs stock                                   |
|----------------------------------------|-------:|------------------------------------------------------|
| `compilerPath: <real solc>`            | 5.4 s  | reference                                            |
| `compilerPath: solc-par`, first build  | 1.8 s  | IDENTICAL: 445/445 artifacts, build-info `output.contracts` deep-equal |
| `compilerPath: solc-par`, second build | 1.7 s  | IDENTICAL                                            |

On the three-contract project the `artifacts/` trees were identical *including*
`build-info/` (same id, equal `input`, `output`, `solcLongVersion`). Hardhat 2.29.1 prints
"Solidity 0.8.37 is not fully supported yet" in both the stock and the solc-par run;
unrelated to solc-par.

Comparison recipe for either version:

```sh
npx hardhat compile --force && mv artifacts artifacts-stock      # stock compiler
npx hardhat compile --force                                       # solc-par
python3 examples/hardhat3/compare-artifacts.py artifacts-stock artifacts   # -> IDENTICAL
```

## Slither / crytic-compile

Validated with Slither 0.11.6 and crytic-compile 0.4.2. Slither compiles through
crytic-compile, which picks a *platform* from the target; the platform decides which solc
call is made and therefore whether there is a speed-up
([`examples/slither/run.sh`](examples/slither/run.sh),
[`examples/crytic-compile/README.md`](examples/crytic-compile/README.md)):

| target                                     | platform    | solc call                                                                 | sharded |
|--------------------------------------------|-------------|---------------------------------------------------------------------------|---------|
| Foundry project directory                  | `Foundry`   | `forge build --build-info --deny never --skip ./test/** ./script/** --force`, env inherited | **yes** via `FOUNDRY_SOLC` |
| Hardhat project directory                  | `Hardhat`   | `npx hardhat compile --force`, env inherited                              | **yes** via the Hardhat config above |
| `file.sol --solc solc-par`                 | `solc`      | `solc file.sol --combined-json abi,ast,bin,bin-runtime,srcmap,srcmap-runtime,userdoc,devdoc,hashes --allow-paths …` | no (passthrough) |
| `<dir> --solc-standard-json --solc solc-par` | `Solc-json` | `solc --standard-json --allow-paths <cwd>`; `sources` = the directory's files only, imports via solc's callback | only if the directory has no outside imports |
| `--solc-solcs-bin` / `--solc-solcs-select` | `solc`      | `--combined-json` as above                                                | no (passthrough) |

### Slither on a Foundry project (the common case)

crytic-compile spawns forge with `os.environ` (plus `FOUNDRY_DYNAMIC_TEST_LINKING=false`),
so the forge variables carry through unchanged:

```sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
FOUNDRY_SOLC=/path/to/solc-par slither . [--json out.json]
```

Validated on openzeppelin-contracts (`FOUNDRY_VIA_IR=true`, scratch `FOUNDRY_OUT`), wall
time of the whole `slither . --json` run (forge build + analysis):

| detectors                                        | stock solc                 | solc-par                        | findings                                   |
|--------------------------------------------------|---------------------------:|--------------------------------:|--------------------------------------------|
| project `slither.config.json` (45 detectors), pass 1 | 44.9 s                 | 28.2 s (first), 17.9 s (second) | 1 / 1 / 1 (`arbitrary-send-erc20`), `(check, description)` sets identical |
| project `slither.config.json`, pass 2            | 34.2 s                     | 19.4 s                          | 1 / 1, full lists identical                |
| all default detectors (3 stock runs, 2 solc-par) | 32.2 s, 34.7 s, 39.9 s     | 42.4 s ², 22.2 s                | 889 in every run; 872 distinct `(check, first line)` heads identical across all 5 runs ³ |

² measured while an `npm install` ran on the same machine; 22.2 s is the quiet rerun.
³ The full `(check, description)` sets differ by 2–6 entries between *any* two runs,
stock-vs-stock included: `reentrancy-events` lists external calls in varying order. That
is Slither's own nondeterminism, not the compiler.

Caveats: Slither's analysis is single-threaded, so the saving is exactly the forge build's
(~26 s of a 45 s run here). Slither runs `forge clean` first, which removes `FOUNDRY_OUT`
(the scratch dir in these runs; the project's `out/` and `cache_forge` were untouched).
Exit code 255 is Slither's normal "findings present". Related crytic-compile flags, not
exercised in these runs: `--foundry-compile-all` (keep tests/scripts), `--ignore-compile`
(reuse an `out/` already built with solc-par), `--compile-force-framework foundry`.

### Slither on a Hardhat project

Configure Hardhat as above and run `slither .`; crytic-compile's Hardhat platform runs
`npx hardhat compile --force` with the inherited environment. Validated on the
three-contract Hardhat 3 project: 2 findings with and without solc-par, identical.

### crytic-compile on bare `.sol` files — works, no speed-up

```sh
slither Token.sol --solc /path/to/solc-par
crytic-compile Token.sol --solc /path/to/solc-par --export-format solc
```

The `solc` platform runs `solc Token.sol --combined-json … --allow-paths …` (after a
`--version` probe); solc-par forwards both to the real solc. Traced with an argv-logging
shim ([`examples/crytic-compile/solc-trace`](examples/crytic-compile/solc-trace)) and a
second shim in place of `SOLC_PAR_SOLC` counting children: exactly one real-solc process
per compile. Validated on a copy of `contracts/token/ERC20/ERC20.sol` (outside the Foundry
project, otherwise crytic-compile routes it to the Foundry platform): the
`--export-format solc` output (5 sources, 7 contracts) is equal with `--solc <real solc>`
and `--solc solc-par`. Note crytic-compile needs `forge` on `PATH` for a `.sol` target when
a foundry.toml exists above the file.

### `crytic-compile <dir> --solc-standard-json` — correct, usually slower than stock

This platform does send `--standard-json`, but `sources` holds only the directory's
`*.sol` files; everything they import is pulled in by solc's import callback. The shards
then return source units solc-par never planned for, solc-par discards them and compiles
once more with stock solc. Validated on a copy of `contracts/token/ERC20/extensions`
(16 listed files, 72 source units after imports): with `SOLC_PAR_JOBS=4` the child shim
counted 5 `--standard-json` runs (4 shards + 1 stock fallback), `SOLC_PAR_DEBUG=1` printed
`solc-par: compiling again with stock solc: source "contracts/interfaces/IERC4337.sol" was
loaded by solc and is not in the input`, and the export (72 sources, 80 contracts) is equal
to stock. A self-contained directory is sharded normally: a three-file directory with no
outside imports gave identical findings with `slither src --solc <real|solc-par>
--solc-standard-json` (2 / 2) and, without the flag, via the combined-json passthrough
(4 / 4). For whole projects use the Foundry or Hardhat route.

## Echidna & Medusa — untested (not installed here)

Neither tool was run; both are crytic-compile front ends, and the invocation below is read
from their source (Echidna `lib/Echidna/Solidity.hs` `compileContracts`, Medusa
`compilation/platforms/crytic_compile.go` and `solc.go`, both GitHub master). The
crytic-compile paths they end up in are the ones validated above.

**Echidna** runs `crytic-compile <target> --solc-disable-warnings --export-format solc
<cryticArgs> <solcArgs> [--libraries …]` through `readCreateProcessWithExitCode (proc …)`
with no environment override, then reads `crytic-export/`:

```sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
FOUNDRY_SOLC=/path/to/solc-par echidna . --contract X        # Foundry project -> forge build, sharded
echidna .                                                     # Hardhat project with the config above, sharded
echidna Token.sol --crytic-args "--solc /path/to/solc-par"   # .sol -> combined-json passthrough, no speed-up
```

**Medusa** (`"platform": "crytic-compile"`, the default) runs
`crytic-compile <target> --export-format solc [--export-dir …] <args…>` with `cmd.Env`
unset (Go inherits `os.Environ()`), or `os.Environ()` plus `SOLC_VERSION=<solcVersion>`
when `platformConfig.solcVersion` is set (meant for solc-select; harmless, solc-par passes
its environment to the child):

```sh
FOUNDRY_SOLC=/path/to/solc-par SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37" medusa fuzz
```

For a `.sol` target use `"args": ["--solc", "/path/to/solc-par"]` (combined-json
passthrough). Medusa's other platform, `"platform": "solc"`, runs the binary literally
named `solc` from `PATH` with `--combined-json`: a passthrough even if solc-par is on
`PATH` as `solc`.

Caveats: the environment-inheritance claim is from source, not a run. In both tools the
gain is limited to the compile phase of a fuzzing campaign.

## solc-select

Validated. solc-select stores binaries at `~/.solc-select/artifacts/solc-<ver>/solc-<ver>`
(22 versions on this machine, including `solc-0.8.37/solc-0.8.37`). Either the artifact
or the `solc` shim works as `SOLC_PAR_SOLC`
([`examples/solc-select/env.sh`](examples/solc-select/env.sh)):

```sh
export SOLC_PAR_SOLC="$HOME/.solc-select/artifacts/solc-0.8.37/solc-0.8.37"
# or the shim, which follows `solc-select use` / $SOLC_VERSION:
export SOLC_PAR_SOLC="$(command -v solc)"; export SOLC_VERSION=0.8.37
```

Validated: `solc-par --version` with both forms prints
`Version: 0.8.37+commit.f401782d.Darwin.appleclang` (exit 0); a three-file
`--standard-json` compile through both forms is byte-identical (`cmp`) to the artifact run
directly. The solc-select installed here has no `install-dir` subcommand (`install`,
`use`, `versions`, `upgrade` only), hence the literal artifacts path.

## Direct CLI and CI scripts

Validated ([`examples/cli/compile.sh`](examples/cli/compile.sh)):

```sh
SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37" SOLC_PAR_JOBS=3 SOLC_PAR_COSTS=./costs.json SOLC_PAR_DEBUG=1 \
  solc-par --standard-json < input.json > output.json
```

Three-file input (A imports and `new`-deploys B, C standalone; optimizer + viaIR; outputs
`abi`, `evm.bytecode.object`, `evm.deployedBytecode.object`, `metadata`, file-level `ast`):
stdout byte-identical to stock solc 0.8.37 (`cmp` clean, sha256 `d498990c…` on both,
17807 bytes), exit 0 on both, empty stderr; costs file written as
`{"A.sol":1732,"B.sol":538,"C.sol":666}`. `SOLC_PAR_JOBS=1` and `--version` reproduce the
stock bytes. With a type error in C: stderr `solc-par: compiling again with stock solc:
shard 2: compilation error`, stdout byte-identical to stock, exit 0 on both (standard-json
error responses exit 0 in solc too). With `SOLC_PAR_SOLC` unset: `solc-par: SOLC_PAR_SOLC
is not set; point it at the real solc binary`, exit 1. `SOLC_PAR_SOLC` goes through
`exec.LookPath`, so a bare name on `PATH` works. `bench/errpath.sh <solc>` runs the clean /
warnings / type-error / parse-error / stack-too-deep parity cases in one go.

CI: a step is just the export plus the tool's flag. Not run on a CI runner; these are the
locally validated commands:

```yaml
# GitHub Actions, Foundry project
- run: |
    export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"      # wherever the runner keeps solc
    export FOUNDRY_SOLC="$GITHUB_WORKSPACE/bin/solc-par"
    forge build
    slither .            # inherits both variables
```

`SOLC_PAR_COSTS` defaults to a file under the user cache dir keyed by project; on an
ephemeral runner every build is a first build (packed by import size, 4.6 s vs 4.7 s on
openzeppelin above), or cache that file / set `SOLC_PAR_COSTS` to a cached path.

## Ape & Remix (status)

### Ape — works

Validated with ape 0.8.52, ape-solidity 0.8.6, py-solc-x 2.0.5. Ape has no compiler-path
setting; ape-solidity resolves binaries with `solcx.get_executable(version)`, which
returns `$SOLCX_BINARY_PATH/solc-v<version>` (default `~/.solcx`) and skips the download
when that file exists. So: a directory holding an executable named `solc-v0.8.37` that is
solc-par in front of the real 0.8.37 ([`examples/ape/`](examples/ape/)):

```sh
mkdir -p ~/.solcx-par
cat > ~/.solcx-par/solc-v0.8.37 <<'EOF'
#!/bin/sh
export SOLC_PAR_SOLC="$HOME/.svm/0.8.37/solc-0.8.37"
exec /path/to/solc-par "$@"
EOF
chmod +x ~/.solcx-par/solc-v0.8.37
SOLCX_BINARY_PATH=~/.solcx-par ape compile --force
```

Validated on the three-contract project (`ape-config.yaml`: `solidity: {version: 0.8.37,
optimize: true, optimization_runs: 200, via_ir: true}`): `.build/__local__.json` equal to
the stock compile in every `contractTypes` entry (abi, deploymentBytecode,
runtimeBytecode, sourcemap, devdoc, userdoc, methodIdentifiers) and in `compilers`,
`sources`, `manifest`. Ape invokes `solc-par --standard-json --base-path <project>`.
Caveats: `SOLCX_BINARY_PATH` replaces the whole solcx directory, so other versions your
pragmas select get downloaded into it (or symlink real binaries there as `solc-v<ver>`);
the mechanism is py-solc-x's, not documented by Ape.

### Remix — not applicable

Remix compiles in the browser with a `soljson.js` (WASM) build; its "custom compiler"
option takes the URL of such a build, and remixd only shares the filesystem. Nothing in
Remix executes a native binary, so there is nothing to configure (read from the Remix
docs, nothing runnable). Compile with Foundry/Hardhat + solc-par and load the artifacts.

### Brownie, Mythril, Truffle — untested

Not installed, derived from how each locates solc: Brownie and Mythril use py-solc-x, so
the Ape wrapper (`SOLCX_BINARY_PATH` + `solc-v<ver>`) should apply; Truffle's
`compilers.solc.version: "native"` runs `solc` from `PATH`, so a wrapper named `solc`
would be needed.

## Troubleshooting

- **`SOLC_PAR_DEBUG=1`** prints why a compile fell back to stock solc:
  `solc-par: compiling again with stock solc: <reason>`. Reasons seen in the runs above:
  `source "…" was loaded by solc and is not in the input` (imports resolved through solc's
  import callback: crytic-compile `--solc-standard-json`, hand-written inputs that list
  only some files) and `shard N: compilation error` (a real compile error; the stock run
  then returns solc's exact diagnostics). Also possible, never seen with any released
  solc: `shard N: top-level "…" differs` (a solc that emits a new top-level output key
  with per-shard content; solc-par then plays safe). Inputs that are never sharded print
  nothing even with `SOLC_PAR_DEBUG=1` (`plan()` hands them to stock solc directly):
  a `language` other than `Solidity`, `settings.stopAfter`, the SMT checker
  (`modelChecker.engine`), fewer than two files with code-generating outputs,
  `SOLC_PAR_JOBS=1`. If `SOLC_PAR_DEBUG=1` prints nothing and
  there is still no speed-up, the call was a passthrough: one of these, or not
  `--standard-json` at all (next item).
- **No speed-up, nothing on stderr**: the tool is not calling `--standard-json`.
  `--combined-json`, `solc file.sol`, `--version` and `--pretty-json` / `--json-indent`
  inputs are forwarded verbatim. Put [`examples/crytic-compile/solc-trace`](examples/crytic-compile/solc-trace)
  in front of solc-par to log the exact argv the tool sends. Known passthrough cases:
  `crytic-compile file.sol --solc solc-par`, `--solc-solcs-bin`, `--solc-solcs-select`,
  Medusa `"platform": "solc"`, Echidna `.sol` targets.
- **`solc-par: SOLC_PAR_SOLC points at solc-par itself`** (exit 1): `SOLC_PAR_SOLC` (or
  `solc` on `PATH`, if you set it to a bare name) resolves to solc-par, e.g. after
  symlinking solc-par as `solc` and setting `SOLC_PAR_SOLC=solc`. solc-par marks its
  children with `SOLC_PAR_CHILD=1` and refuses to recurse; set `SOLC_PAR_SOLC` to the real
  binary's full path.
- **`solc-par: SOLC_PAR_SOLC is not set`** / **`SOLC_PAR_SOLC: … not found`**: the variable
  must be in the environment of the process that runs solc, which is the tool's child.
  forge, hardhat, slither, crytic-compile, echidna, medusa and ape all inherit their
  environment, so an `export` in the shell or CI step is enough; config files
  (foundry.toml, hardhat.config.js) cannot set it, except through a wrapper script as in
  the Ape and multi-version Hardhat cases.
- **Wrong version reported / "not fully supported"**: `--version` is forwarded, so the
  tool sees the real solc's version. Hardhat's `version` must match it; Hardhat 2's notice
  about 0.8.37 appears with stock solc too.
- **The build was already short**: solc-par only shrinks the compile. On openzeppelin a
  slither run is ~26–30 s of compile and ~15–18 s of analysis; the second number does not
  move. Hardhat and Ape projects of a few files compile in a second either way.
- **Different `buildInfoId` in Hardhat 3**: expected; Hardhat 3 hashes the compiler path
  into the id. The `input`/`output` inside are equal.
