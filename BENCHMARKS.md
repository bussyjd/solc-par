# Benchmarks

Four real Foundry projects, compiled from scratch (`forge build --force`) with **solc
0.8.37**, stock vs `--use solc-par`. Wall time of the whole `forge build`, median of the
listed number of runs, on a quiet machine. Every run's artifacts were compared with the
stock build: `abi`, `bytecode`, `deployedBytecode`, `metadata` and every other key are
byte-identical in all projects.

Machine: Apple M4 Pro (8 performance + 4 efficiency cores), 48 GB, macOS 26, forge 1.8.3,
`SOLC_PAR_JOBS=12`. Measured 2026-10-02 with the released binary.

| project | files | settings | stock | solc-par cold | solc-par warm | speed-up (warm) |
|---|--:|---|--:|--:|--:|--:|
| aave-v3-origin | 456 | via-IR, optimizer | 327.5 s (n=1) | 55.5 s | 53.2 s (n=2) | **6.2×** |
| openzeppelin-contracts | 421 | via-IR, optimizer | 25.9 s (n=2) | 4.1 s | 4.8 s (n=2) | **5.5×** |
| uniswap v4-core | 160 | via-IR, optimizer runs 200 ¹ | 30.8 s (n=2) | 6.5 s | 4.9 s (n=2) | **6.3×** |
| gmx-synthetics | 430 | legacy codegen, optimizer runs 10 ² | 26.6 s (n=2) | 4.6 s | 4.7 s (n=2) | **5.7×** |

*cold* = first build of the project (shards packed by import size); *warm* = any later
build (packed by the bytecode sizes learned from the previous one). An earlier full run
of the same suite the same day (pre-review build, identical timing paths) agreed within
±0.5 s on the small projects and ±4 s on aave-v3; run-to-run noise on the small
projects is about the size of the cold/warm difference.

¹ v4-core pins `optimizer_runs = 44444444`, at which solc 0.8.37 via-IR fails with
"stack too deep" in `Pool.swap` (0.8.36 compiles it; the threshold is 217 runs — a solc
0.8.37 regression). Runs were set to 200, and its exact `pragma solidity 0.8.26` relaxed
to `>=0.8.26`, to compile on the latest solc; both apply equally to stock and solc-par.
² gmx-synthetics does not compile under via-IR on any recent solc (stack too deep), so it
is the legacy-codegen data point.

## Why not 12×?

On 12 cores the ideal would be ~28 s for aave-v3. The measured floor for process-level
sharding of stock solc on this machine is ~50 s, for two reasons quantified in
[DESIGN.md §5](DESIGN.md#5-where-the-remaining-time-goes-and-what-did-not-help): work
every shard repeats (parsing and analysis of all sources, optimising the contracts its
files deploy with `new`: ~93 CPU-seconds in total) and, larger, the clock-frequency and
IPC drop of a fully loaded machine (~147 CPU-seconds). Smarter packing (dependency-aware,
efficiency-core-aware, fewer shards) was measured and does not move the floor.

## Comparison with oksolc

[oksolc](https://github.com/okcontract/oksolc) takes the other route: a reimplementation
of solc (0.8.36, via-IR only) in Zig with in-process parallelism, incremental compilation
and a persistent cache. Measured on the same machine on 2026-09-30 (aave-v3-origin, via-IR,
`forge build --force`, parallel mode):

| compiler | wall time | notes |
|---|--:|---|
| solc 0.8.37 (stock) | 333 s | |
| solc 0.8.37 + solc-par | 64 s (cold, earlier build) | identical artifacts |
| oksolc (solc 0.8.36 semantics) | 28–33 s | 1 of 1657 contracts did not compile (optimiser hang on a constant pattern); AST differs from solc's in places; not 0.8.37 |

oksolc is faster because it executes ~40 % fewer CPU instructions than solc for the same
output — a compiler-level gain that no scheduling of stock solc can match. The two tools
suit different stages: oksolc for a fast dev loop where a 0.8.36 compiler is acceptable,
solc-par wherever the deployment pins a solc version and the bytecode must be exactly
that solc's.

## solc version compatibility

solc-par depends on three stable things: the standard-json shape (unchanged since solc
0.4.11, 2017), the fact that a contract's bytecode and metadata do not depend on
`outputSelection`, and its own stock fallback, which turns any unexpected shard
behaviour into one plain solc run. `bench/compat.sh` checks every version for sharding
of a clean multi-file compile and byte-identical stdout and exit code on five cases
(clean, warnings, type error, parse error, code-generation error). Measured 2026-10-02:

| solc | `--standard-json` | shards a clean compile | parity with stock |
|---|---|---|---|
| 0.4.0 | no (predates it) | n/a | identical: both fail the same way |
| 0.4.12 | yes | yes | identical, 5/5 cases |
| 0.5.0, 0.5.16, 0.6.12, 0.7.0 | yes | yes | identical, 5/5 cases |
| 0.8.0, 0.8.4, 0.8.10, 0.8.16, 0.8.19, 0.8.24, 0.8.33, 0.8.37 | yes | yes | identical, 5/5 cases |

Support policy: **solc ≥ 0.5.0 is supported and checked in CI** (`solc-matrix` workflow,
nine released versions plus the latest release, weekly); older versions are best effort —
correct through the fallback, possibly without speed-up. Whole-project artifact identity
is additionally verified on the four projects above with 0.8.37 (and on Aave v3 with 0.8.27).

## Reproduce

```sh
go build -o solc-par .
# any Foundry project(s); env FOUNDRY_* overrides apply to both stock and solc-par runs
FOUNDRY_VIA_IR=true bench/run.sh /path/to/solc-0.8.37 /path/to/project ...
python3 bench/render.py bench/results.tsv       # markdown table
bench/errpath.sh /path/to/solc-0.8.37           # error-path parity on crafted inputs
```

`bench/run.sh` builds each project into a scratch `out/`, so the project's own build is
untouched, and runs `bench/compare-artifacts.py` on every artifact of the stock and
solc-par builds. The raw results of the run above are in `bench/results-2026-10-02.tsv`.
