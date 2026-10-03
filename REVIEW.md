# Soundness review before the first public release

Before publishing, the code was reviewed by an independent expert reviewer (an
Anthropic Claude model acting as release gate, reasoning from the source only) against the
guarantees the README makes. Verdict: *publishable after listed fixes*. The core sharding
assumption — that each contract's bytecode, ABI and metadata do not depend on
`outputSelection`, and that metadata does not embed `outputSelection` — was confirmed sound.
Every finding and its resolution:

| # | Severity | Finding | Resolution |
|--:|----------|---------|------------|
| 1 | High | Sources that solc loads itself through its import callback (`--base-path`/`--include-path` imports not listed in `sources`, `urls` sources) match the `*` wildcard in a stock run but no shard's narrowed selection: their contracts and ASTs silently disappeared. Foundry inlines its whole import graph, so it was immune. | `merge` now rejects any shard whose `sources` contains a unit absent from the input; solc-par then reruns stock solc and returns its output verbatim. Test `foreign source`. |
| 2 | High | A code-generation error (stack too deep, `UnimplementedFeature`, …) is thrown for one contract: stock emits *only* that error, while only the shard owning the contract saw it, so the merge emitted the error plus the other shards' contracts — a shape solc never produces. | Any shard reporting a diagnostic of severity `error` makes solc-par rerun stock solc and return its output verbatim. Type errors, which every shard reports identically, take the same path; the cost is one analysis-only run on the error path. Test `codegen error`. |
| 3 | Medium | A selected file with no code-generation output (`abi` only, or a file-level `ast` only) alongside files with bytecode: stock still compiles it (global binary request), its owner shard does not, so its codegen warnings and post-codegen AST fields were missing. | `plan` shards only when *every* selected file requests a code-generation output; otherwise passthrough. Foundry's `*` selection always qualifies. Tests `mixed selection`, `ast-only file`. |
| 4 | Medium | `settings.modelChecker` runs the SMT solver in every shard and its timeout-bound diagnostics are not deterministic, so merged errors could diverge. | Passthrough when `modelChecker.engine` is set and not `none`. Test `modelChecker`. |
| 5 | Low | Codegen detection treated `evm.methodIdentifiers` as code generation (solc deliberately does not) and missed `yulCFGJson`/`ethdebug`. | `isCodegen` mirrors solc's `isBinaryRequested`. Test `methodIdentifiers`. |
| 6 | Low | `SOLC_PAR_SOLC` pointing at solc-par itself was an N-ary fork bomb. | Children run with `SOLC_PAR_CHILD=1`; solc-par refuses to start when it is set. Test `TestRecursionGuard`. |
| 7 | Low | `SOLC_PAR_SOLC=solc` could hit Go's `exec.ErrDot`; `--pretty-json`/`--json-indent` bodies would be re-glued compactly; `--base-path=x` form and relative `--base-path` hashed to different cache files; a failed rename leaked the temp costs file; write errors to a closed stdout were ignored; every shard's stderr was repeated. | `exec.LookPath` up front with a clear error; passthrough on `--pretty-json`/`--json-indent`; both `--base-path` forms, made absolute; temp file removed on failure; the final write is checked; identical shard stderr is written once. |
| 8 | Tests | The fake solc never produced solc's error-only shape; no tests for callback-loaded sources, mixed selections, named-contract keys, a disabled cache, or the race detector. | All added (`TestFallbackToStock`, `TestNamedContractKeys`, `TestCostsPath`, cache off in `TestLearnedCosts`); CI runs `go test -race` on Linux, macOS and Windows. |
| 9 | Nits | `sync.WaitGroup.Go`, `fmt.Fprintf` in the fake, `slices.SortFunc`, unused parameter. | Done. |

Confirmed correct as written: the merged top level only ever holds `errors`, `sources`,
`contracts`; empty `contracts`/`errors` are omitted like stock; byte-keyed error
de-duplication is safe because analysis diagnostics derive from the identical full source
set in every shard; the once-per-compilation 2394 collapse; `generatedSources` ids are
shard-independent. Security: no input-derived filesystem path is ever opened (source
names only become JSON keys, `--base-path` is only hashed), and the only process executed
is the user-supplied solc with the user's arguments.

Memory: the dominant cost is solc's, not solc-par's. Every shard is a full solc process
that parses and analyses all sources, so a build takes about `SOLC_PAR_JOBS` × one
shard's memory: on openzeppelin-contracts (via-IR, 12 shards) 3.7 GB against 1.2 GB for
stock. solc-par itself holds each shard's response while the others run plus the raw
bodies the merge keeps; measured 81–128 MB there for a 14 MB response, a few hundred MB
on the largest project.

After the fixes the artifact identity check in `bench/` was repeated on all four
projects (see BENCHMARKS.md).

## Go review

A second pass, by three tracers (correctness, performance, idiom) working with gopls,
`go vet`, staticcheck, the race detector and the real openzeppelin-contracts build,
looked at the Go code rather than the sharding model. What changed:

- **Job count honours cgroup CPU quotas.** The default is `runtime.GOMAXPROCS(0)` instead
  of `runtime.NumCPU()`; identical on bare metal, container-aware on Linux (Go ≥ 1.25).
- **`--standard-json <file>` no longer blocks on stdin.** solc ignores stdin when given an
  input file; solc-par read stdin to EOF first (measured: 4 s behind stock with a 4 s open
  pipe, forever on a terminal). Any argument that is not an option or an option's value,
  and `--input-file`'s value, now takes the passthrough (`-` is stdin). Tests `input file`,
  `--input-file`, `TestHasInputFile`.
- **Shards die with solc-par.** SIGINT/SIGTERM cancel the context every solc runs under,
  and solc-par exits with the shell's status for the signal without writing stdout.
  Measured before: 12 shards ran to completion after their parent was killed.
  Test `TestSignalKillsShards`.
- **A shard that could not be started is reported as such** (`shard N: fork/exec …`)
  under `SOLC_PAR_DEBUG`, instead of "solc exited with code 1".
- **Import scan 11× faster.** `costs()` locates each `import` with `strings.Index` and
  runs the regex anchored there instead of a multiline regex tried at every line start:
  28 → 2.5 ms on openzeppelin, on the serial path before any shard starts. Identical
  import lists on all 421 real sources and 11 M fuzz inputs against the old regex.
- **Less retained memory**: each shard's whole-output copy is dropped as soon as it is
  decoded, and `object()` sizes its buffer up front with one encoder (merge 7.8 → 4.8 ms).
- Idiom: `result.stdout`/`stderr` fields, `decode` without a dead named result, explicit
  JSON tags on the size probe, doc comments on every function, a plain message for a
  bad `SOLC_PAR_JOBS`, a `solc-par:` line when the final stdout write fails, `t.Run`
  subtests, `gofmt -s`/staticcheck/`permissions: contents: read` in CI.

Measured and left alone: splitting the input once instead of copying it per shard
(3.5 ms and ~30 MB in a process beside 3.7 GB of solc), a single-pass decode (the
decode tail after the last shard is 20–28 ms of a 4.5 s build), and plan's double
unmarshal of the input (a single-pass prototype measured no gain).

Gates after the changes: gofmt, gopls check, go vet (darwin/linux/windows), staticcheck
and `go test -race` clean; `bench/errpath.sh` 5/5 identical; openzeppelin-contracts
artifacts IDENTICAL to stock through `bench/run.sh` (26 s stock, ~4.3 s solc-par).

## Compatibility guards (after the version matrix)

`bench/compat.sh` was run against 14 released solc binaries from 0.4.0 to 0.8.37
(BENCHMARKS.md, "solc version compatibility"): every version from 0.5.0 shards a clean
multi-file compile and matches stock byte-for-byte on all five parity cases; 0.4.x
matches stock through the fallback. Two guards were added for the cases the matrix
cannot cover:

| Guard | Why | Where |
|---|---|---|
| `language` other than `Solidity` passes through | `Yul` and `SolidityAST` inputs were never tested; correctness over speed | `plan()`, `TestPassthrough` |
| Any top-level output key other than `errors`/`sources`/`contracts` must be byte-identical across shards, else fallback | the merge copies such keys from shard 0; a future solc adding a per-shard key would otherwise change the contract silently | `merge()`, `TestFallbackToStock` ("top-level key"), `TestTopLevelKeyKept` |

The `solc-matrix` CI workflow runs the harness on nine releases from 0.5.17 to 0.8.37
plus whatever the latest release is, weekly, so a new solc is checked before any user
reports it.
