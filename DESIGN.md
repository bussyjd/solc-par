# Design: what solc-par changes, what it optimises, and what was measured

This document maps every decision in `main.go` to the measurement that motivated it.
All numbers are from an Apple M4 Pro (8 performance + 4 efficiency cores, 48 GB) with
solc 0.8.37, via-IR, unless stated otherwise.

## 1. The parallelisation axis: processes, not threads

solc's pipeline is parse → analysis → code generation → optimisation, and only the last
two are expensive on real projects. They are also *per contract*: optimising contract A
does not need the optimised output of contract B. The one exception is creation code —
when A does `new B()`, B's optimised creation bytecode is embedded in A, so compiling A
means also optimising B.

Three axes were considered:

| Axis                                       | Change to solc | Result            |
|--------------------------------------------|----------------|-------------------|
| Threads inside solc (parallel optimiser)   | large, invasive | not attempted     |
| A new compiler (e.g. oksolc, Zig rewrite)  | everything      | different bytecode risk, see §7 |
| **Process-level sharding of standard-json** | **none**       | **chosen**        |

Sharding needs no change to solc, works with every solc version Foundry can download,
and its correctness argument is short (§2). Its cost is the work every shard repeats (§5).

## 2. Why narrowing `outputSelection` gives identical bytecode

A shard's input is the original standard-json with one edit: `settings.outputSelection`
keeps only the entries for the files that shard owns. solc then

- parses and analyses *all* sources (needed for types, inheritance, imports);
- generates and optimises code only for contracts with a requested output, plus the
  contracts they deploy with `new` (their creation code is a dependency);
- skips everything else.

Because every setting that influences code generation (`optimizer`, `viaIR`,
`evmVersion`, `libraries`, `metadata`, `remappings`, …) is passed through verbatim, the
code generated for an owned file is exactly what a full run generates for it. The
`metadata` output embeds compiler settings (`compilationTarget`, `evmVersion`,
`libraries`, `metadata`, `optimizer`, `remappings`) but not `outputSelection`, so the
shard's narrowed selection cannot leak into it. This is verified empirically on
every artifact of four projects (`bench/compare-artifacts.py`): abi, bytecode,
deployedBytecode and metadata are byte-identical, and so is every other key.

What solc-par will not shard (passthrough to stock):

- not a `--standard-json` invocation (`--version`, legacy CLI), or `--pretty-json` /
  `--json-indent` (shard bodies would be glued back together compactly);
- `--standard-json <file>` or `--input-file <file>`: solc then ignores stdin, so solc-par
  does not read it either (an argument that is neither an option nor an option's value
  is taken for a file; `-` is stdin);
- input that is not a JSON object with a `settings` object, or whose `language` is not
  `Solidity` (`Yul` and `SolidityAST` inputs are untested, so they are not sharded);
- `SOLC_PAR_JOBS` of 1 or less;
- `settings.stopAfter` set (nothing to generate);
- `settings.modelChecker.engine` set: the SMT checker would run in every shard and its
  timeout-bound diagnostics are not deterministic across processes;
- fewer than two files with code-generation outputs (`evm.*` except
  `evm.methodIdentifiers`, `ir*`, `yulCFGJson`, `ethdebug`, `*`), or any selected file
  without one — stock would still compile such a file (binary outputs are requested
  globally) while its owner shard would not, dropping its codegen warnings and
  post-codegen AST fields.

What makes solc-par discard the shards and compile once more with stock solc, returning
that output verbatim (the "fallback" in `merge`):

- a shard exits non-zero or returns something that is not a JSON object;
- a shard reports a diagnostic of severity `error`. A code-generation error (stack too
  deep, unimplemented feature) is thrown for one contract and makes solc emit *only* that
  error — and only the shard owning the contract sees it. Type errors are reported
  identically by every shard, but take the same path so that error responses are always
  byte-for-byte solc's;
- a shard's `sources` lists a unit that was not in the input: solc loaded it through its
  import callback (`--base-path`/`--include-path`, `urls` sources), it matched the `*`
  wildcard in stock, and no shard was told to select it;
- a top-level key other than `errors`, `sources` and `contracts` differs between shards.
  No solc emits one today; the merge copies such keys from shard 0, and this check keeps
  a future solc from changing the contract silently (`bench/compat.sh` and the
  `solc-matrix` CI workflow test every supported release and the latest one).

## 3. Packing shards: longest-processing-time with learned costs

Per-file compile cost varies by two orders of magnitude, so round-robin packing leaves
most shards idle while one finishes. Files are packed longest-first (LPT). The weight is:

```
weight(f) = 100 × bytecode_json_bytes(f, last build) + import_closure_bytes(f) / 100 + 1
```

- **Generated-code size** (bytes of the contract's `evm.bytecode` + `deployedBytecode` JSON
  in the previous build) correlates with measured compile instructions at Pearson
  0.92–0.99 across the four projects. It is recorded after every build in the costs file.
- **Import closure** (bytes of the file plus everything it transitively imports, resolved
  through the remappings the way solc does) is the cold-start proxy, and a 1 % blend keeps
  files without code spread out instead of piled into one shard. 1 % beat 10 % and 0.1 % on
  v4-core, OpenZeppelin and GMX.
- A file never seen before gets its closure scaled to the learned units of the known files.
- `+1` so no file weighs zero: LPT would otherwise stack all zero-weight files into one shard.

Measured on aave-v3-origin (456 files, 12 shards): the shards finish within ±1 % of each
other once costs are learned, i.e. packing is no longer the bottleneck.

## 4. Merging outputs as raw bytes

Shard outputs are large (aave-v3: ~100 MB in total). Early versions decoded every shard
into Go values and re-encoded them: 1.9 s of pure overhead and a risk of changing number
formatting. The merge now

- decodes each shard **as it exits**, concurrently with the shards still running, keeping
  only the raw `json.RawMessage` bodies of the contracts and sources it owns (0.1 s);
- writes the final object with sorted keys and the bodies copied verbatim (no HTML
  escaping, no float re-encoding);
- takes `sources` entries (ids, ASTs) from the owning shard, checks that source ids agree
  across shards, de-duplicates `errors` in first-seen order, and collapses solc's
  once-per-compilation warning 2394 (transient storage), which every shard reports at a
  different location.

## 5. Where the remaining time goes (and what did not help)

End-to-end on aave-v3-origin: stock 333 s → solc-par 64 s cold, ~53 s warm (12 jobs).
The ideal 12× would be ~28 s. Measuring instructions and wall time per shard:

| Component                                             | ≈ CPU seconds |
|-------------------------------------------------------|---------------|
| Stock single run                                      | 329           |
| Sum of all 12 shards                                  | ~570          |
| of which: parsing + analysis repeated per shard       | ~37           |
| of which: re-optimising `new`-deployed dependencies   | ~56           |
| of which: frequency/IPC drop with 12 busy cores       | ~147          |

So most of the inflation is the machine, not the sharding: with all cores busy the M4 Pro
runs each at a lower clock and lower IPC. The duplicated solc work is ~93 s spread across
12 processes, i.e. under 8 s of wall time.

Measured and dropped:

- **Dependency-aware packing** (co-locating a contract with the files it deploys with
  `new`, so the dependency is optimised once): on aave-v3 the test suite deploys the whole
  protocol, so almost every shard needs almost every dependency. Co-location made shards
  *less* balanced and gained nothing.
- **Weighting efficiency cores** (giving E-core shards less work): no measurable gain; the
  frequency drop is global.
- **Heterogeneous shard counts** (fewer, larger shards): the 12-shard partition is already
  within ±1 %; fewer shards only idle cores.
- **Per-shard statistics instrumentation** (`SOLC_PAR_STATS`): useful for the analysis
  above, removed from the released code (it also broke the Windows build).

Floor: ~50 s on aave-v3 for *any* process-level sharding of stock solc on this machine;
the stock instruction count alone, run at full-load throughput, is ~39 s. Going below that
requires solc to do less work, not more cores.

## 6. Determinism and safety

- Output bytes depend only on the input and on the partition; the partition depends only
  on the input and the costs file. Identical artifacts were verified across cold/warm
  runs and across job counts.
- solc-par never emits a partial merge: anything but a clean, error-free response from
  every shard triggers the stock fallback above.
- solc-par refuses to run when `SOLC_PAR_CHILD` is set in its environment, which it sets
  for every solc it spawns — pointing `SOLC_PAR_SOLC` at solc-par itself is an error,
  not a fork bomb.
- The costs file is written to a temporary file and renamed into place; a damaged or
  missing file only affects packing balance, never output.
- solc-par executes only the binary named by `SOLC_PAR_SOLC`, with the arguments it was
  given, and writes only the costs file.
- SIGINT or SIGTERM kills every solc that solc-par started (a shard left behind would
  keep a core busy until it tried to write to the closed pipe) and solc-par exits with
  the shell's status for the signal, without writing to stdout.
- The default job count (`defaultJobs()`) starts from `runtime.GOMAXPROCS(0)`, which
  unlike `runtime.NumCPU` honours a Linux cgroup CPU quota, and caps it on Linux at one
  shard per GiB of any cgroup memory limit (`/sys/fs/cgroup/memory.max` or cgroup v1
  `memory.limit_in_bytes`). A memory-capped container on a many-core host (such as
  `solc-bench-cli`'s default 8 GiB sandbox on a 12-core machine) would otherwise spawn
  enough via-IR `solc` shards to trigger the cgroup OOM-killer and fall back to a single
  stock `solc` run.

## 7. Relation to oksolc

[oksolc](https://github.com/okcontract/oksolc) is a from-scratch reimplementation of solc
0.8.36 in Zig with in-process parallelism, incremental compilation and a persistent cache.
On aave-v3 it compiles in ~28–33 s because it executes ~40 % fewer instructions than solc
for the same output — that is a compiler improvement, not a scheduling one, and it is the
only way below the floor in §5. It is pinned to one solc version and (per its own README)
not yet production-ready; its ASTs also diverge from solc's in places. solc-par is the
complementary tool: whatever solc version your deployment pins, same bytecode, on all cores.
