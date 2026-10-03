# Architecture

solc-par is a single Go binary (`main.go`, standard library only) that sits where a
tool expects `solc`. Every diagram below names the function in `main.go` that owns the
box, so the picture and the code can be checked against each other. [DESIGN.md](DESIGN.md)
has the measurements behind each decision; this file only shows the shape.

## 1. One compile, end to end

A tool runs `solc-par --standard-json` exactly as it would run `solc`. `run()` reads the
whole input, `plan()` either hands it to stock solc unchanged or cuts it into N shard
inputs that differ only in `settings.outputSelection`, and the shards run as N concurrent
real-solc processes. Each shard's response is decoded the moment that process exits, while
the others still run; `merge()` then copies the owned bodies verbatim into one response.

```mermaid
flowchart LR
  tool["forge / hardhat / slither"] -->|"solc --standard-json"| par["solc-par run()"]
  par --> plan["plan()"]
  plan -->|"cannot shard"| stock["stock solc, one run"]
  plan -->|"N inputs, narrowed outputSelection"| s1
  plan --> s2
  plan --> sn
  subgraph shards ["N concurrent real solc processes"]
    direction LR
    s1["solc shard 1"] --> d1["decode()"]
    s2["solc shard 2"] --> d2["decode()"]
    sn["solc shard N"] --> dn["decode()"]
  end
  d1 --> merge["merge()"]
  d2 --> merge
  dn --> merge
  merge --> out["one standard-json response, back to the tool"]
  stock --> out
```

Anything that is not a `--standard-json` compile (`--version`, the legacy CLI) never
reaches `plan()`: `run()` forwards the arguments and stdin to the real solc as-is.

## 2. Shards in flight, and the fallback

The shards are independent processes with no shared state, so they run fully concurrently
(`sync.WaitGroup`, one goroutine per shard). The merge accepts only a clean, error-free
response from every shard. Anything else - a non-zero exit, a non-JSON body, a diagnostic of
severity `error`, a source that was not in the input, a top-level key solc-par does not
merge that differs between shards - makes `run()` discard all shard
output and compile the untouched input once more with stock solc, returning that output and
exit code verbatim.

```mermaid
sequenceDiagram
    participant T as Tool
    participant P as solc-par
    participant S1 as solc shard 1
    participant S2 as solc shard 2
    participant SN as solc shard N
    participant S0 as stock solc
    T->>P: --standard-json input on stdin
    P->>P: plan() packs files into N shards
    par run concurrently
        P->>S1: input, outputSelection of shard 1
        S1-->>P: response, exit 0
        P->>P: decode() shard 1
    and
        P->>S2: input, outputSelection of shard 2
        S2-->>P: response, exit 0
        P->>P: decode() shard 2
    and
        P->>SN: input, outputSelection of shard N
        SN-->>P: response, exit 0
        P->>P: decode() shard N
    end
    P->>P: merge()
    alt every shard clean and error-free
        P-->>T: merged response, exit 0
        P->>P: saveCosts()
    else any shard failed or reported an error
        P->>S0: original input, unchanged
        S0-->>P: response and exit code
        P-->>T: stock output verbatim
    end
```

The price of the fallback is one extra stock run on the error path; the benefit is that an
error response is byte-for-byte solc's, including its shape and diagnostics.

## 3. The learned-costs loop

Per-file compile cost spans two orders of magnitude, so balanced shards need a cost
estimate. The size of the code solc generated for a file last time tracks its compile cost
closely (Pearson 0.92-0.99), so every successful build records it and the next build packs
with it. The first build of a project has only the import closure of each file, a much
weaker proxy, which is why the second build is faster than the first. A file never seen
before gets its closure scaled to the ratio of learned size to closure over the known files.

```mermaid
flowchart LR
  costs[("costs file, one per project in the user cache dir")]
  costs -->|"loadCosts()"| w["weights(): 100 x last bytecode bytes + import closure bytes + 1 (closure weighs 1 %)"]
  closure["costs(): bytes of each file plus its transitive imports"] --> w
  w --> lpt["partition(): LPT, heaviest file first into the least-loaded shard"]
  lpt --> build["N concurrent solc runs"]
  build -->|"decode(): bytes of evm.bytecode + deployedBytecode per owned file"| sizes["sizes"]
  sizes -->|"saveCosts(): write temp file, rename into place"| costs
```

`SOLC_PAR_COSTS` names the file (`""` disables it); otherwise it lives under the user cache
dir, keyed by a hash of `--base-path` or the working directory. A missing or damaged file
only costs balance, never output.

## 4. Passthrough and fallback decisions

Two gates, in code order. The first box is the argument check in `run()`; after it, `plan()`
returns `nil` (passthrough: one stock run) when the input cannot be sharded safely; `merge()` returns an error (fallback: one stock run) when the
shards did not all succeed. Both paths end in the same `solcRun(solc, args, in)` on the
original bytes.

```mermaid
flowchart TD
  subgraph P ["plan(): can the input be sharded?"]
    a0["run(): has --standard-json, no --pretty-json or --json-indent?"]
    a0 -->|no| fwd["forward args and stdin to stock solc"]
    a0 -->|yes| a1["SOLC_PAR_JOBS greater than 1?"]
    a1 -->|yes| a2["input is a JSON object with settings, language Solidity?"]
    a2 -->|yes| a3["settings.stopAfter unset?"]
    a3 -->|yes| a4["modelChecker.engine unset or none?"]
    a4 -->|yes| a5["every selected file has a codegen output?"]
    a5 -->|yes| a6["at least 2 selected files?"]
    a1 -->|no| pass["passthrough: one stock run"]
    a2 -->|no| pass
    a3 -->|no| pass
    a4 -->|no| pass
    a5 -->|no| pass
    a6 -->|no| pass
  end
  a6 -->|yes| run["N shard inputs, run concurrently"]
  run --> m1
  subgraph M ["merge(): did every shard succeed?"]
    m1["every shard exited 0?"]
    m1 -->|yes| m2["every shard output is a JSON object?"]
    m2 -->|yes| m3["no diagnostic of severity error?"]
    m3 -->|yes| m4["other top-level keys identical across shards?"]
    m4 -->|yes| m5["every listed source was in the input?"]
    m5 -->|yes| m6["source ids agree across shards?"]
    m1 -->|no| fb["fallback: one stock run, output and exit code verbatim"]
    m2 -->|no| fb
    m3 -->|no| fb
    m4 -->|no| fb
    m5 -->|no| fb
    m6 -->|no| fb
  end
  m6 -->|yes| ok["one merged response, exit 0, saveCosts()"]
```

"Codegen output" is the set solc itself uses to decide whether to generate code: `*`,
`evm.*` except `evm.methodIdentifiers`, `ir*`, `yulCFGJson`, `ethdebug` (`isCodegen()`).
The `merge()` checks run per shard in shard order; the first failure wins and
`SOLC_PAR_DEBUG=1` prints it.

## 5. What LPT packing buys

Illustrative, from the numbers in DESIGN.md: aave-v3-origin takes 333 s in one stock process;
with learned costs and 12 shards the shards finish within +/-1 % of each other at about 53 s,
so no core waits for a straggler. The gap between 53 s and the ideal 333 / 12 = 28 s is work
every shard repeats (parse and analysis of all sources, re-optimising contracts deployed with
`new`) and the lower clock of a fully loaded machine, not packing.

```mermaid
gantt
    title aave-v3-origin, via-IR, 12 jobs (seconds, illustrative)
    dateFormat X
    axisFormat %s
    section stock solc
    one process        :s0, 0, 333
    section solc-par warm
    shard 1            :p1, 0, 53
    shard 2            :p2, 0, 53
    shard 3            :p3, 0, 53
    shard 4            :p4, 0, 53
    shard 5            :p5, 0, 52
    shard 6            :p6, 0, 53
    shard 7            :p7, 0, 53
    shard 8            :p8, 0, 52
    shard 9            :p9, 0, 53
    shard 10           :p10, 0, 53
    shard 11           :p11, 0, 52
    shard 12           :p12, 0, 53
```

## Where each box lives

| Box                      | `main.go`                                   |
|--------------------------|---------------------------------------------|
| forward / passthrough    | `run()` (argument check), `plan()` returning `nil`, `solcRun()`, `emit()` |
| N shard inputs           | `plan()`, `object()`, `marshal()`           |
| cost estimate            | `costs()`, `resolve()`, `weights()`         |
| LPT packing              | `partition()`                               |
| concurrent shards        | `run()` (`sync.WaitGroup`), `solcRun()`     |
| decode as they finish    | `decode()`, `size`                          |
| merge                    | `merge()`, `object()`                       |
| fallback                 | `run()` after `merge()` returns an error    |
| costs file               | `costsPath()`, `loadCosts()`, `saveCosts()` |
