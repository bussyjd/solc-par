# Changelog

## 0.1.1 — 2026-10-06

- `.gitattributes` keeps LF line endings on every platform, so the gofmt check passes
  on Windows checkouts (CI only; no change to the binary).

## 0.1.0 — 2026-10-06

Initial public release.

- Shards `--standard-json` compiles by source file across concurrent solc processes;
  byte-identical artifacts verified on Aave v3, OpenZeppelin, Uniswap v4 core and GMX
  synthetics with solc 0.8.37.
- Longest-processing-time packing on learned per-file bytecode sizes (`SOLC_PAR_COSTS`),
  import-closure proxy for the first build.
- Concurrent per-shard decode and raw-byte merge; error de-duplication; source-id
  consistency check; once-per-compilation warning 2394 collapsed.
- Passthrough for everything that cannot be sharded (`stopAfter`, `modelChecker`,
  `--pretty-json`, selections without code generation for every file).
- Stock fallback: any shard error, non-zero exit, unexpected output or callback-loaded
  source makes solc-par compile once more with stock solc and return its output verbatim.
- Recursion guard (`SOLC_PAR_CHILD`), `SOLC_PAR_DEBUG`, stock-compatible trailing newline.
- Independent soundness review before release, see REVIEW.md.
- Compatibility guards: inputs whose `language` is not `Solidity` pass through; a
  top-level output key that solc-par does not merge must be identical across shards or
  the compile falls back to stock (a future solc cannot silently change the contract).
  `bench/compat.sh` and the `solc-matrix` CI workflow check nine solc releases from
  0.5.17 to the latest.
- Go review (REVIEW.md, "Go review"): default jobs from `runtime.GOMAXPROCS(0)` so cgroup
  CPU quotas are honoured; `--standard-json <file>` and `--input-file <file>` pass through
  instead of blocking on stdin; SIGINT/SIGTERM kill the running solc processes and exit 130/143; a shard that
  could not be started is reported as such; 11× faster import scan; less retained
  memory in the merge; doc and idiom fixes.
