// solc-par is a drop-in solc wrapper that shards a --standard-json compile by
// source file and runs the shards as concurrent solc processes.
//
//	SOLC_PAR_SOLC   path of the real solc (required)
//	SOLC_PAR_JOBS   number of shards (default: the CPUs available to the process)
//	SOLC_PAR_COSTS  file that remembers each source's generated-code size for the next
//	                plan (default: one file per project under the user cache dir; "" = off)
//	SOLC_PAR_DEBUG  when set, says on stderr why a compile fell back to stock solc
//	SOLC_PAR_CHILD  set by solc-par for the solc it runs; solc-par refuses to start with it
//
// Each shard gets the original input verbatim except outputSelection, which is
// limited to the files that shard owns. solc then only generates code for those
// files (plus contracts they create with `new`), and bytecode is identical to a
// single full run. Outputs are merged back into one standard-json response.
//
// Shards are packed longest-processing-time first. A file's weight is the size of
// the code solc generated for it last time (Pearson 0.92-0.99 against measured
// instructions on four projects) plus 1% of its transitive import bytes; on a
// first run only the import bytes are known, a much weaker proxy.
//
// ponytail: every shard repeats the parse of all sources, the analysis of its
// files' imports and the optimisation of contracts they create with `new` (~130G
// instructions per shard on aave-v3, whose tests all deploy the protocol). Packing
// such files together was measured and does not pay when they are most of the work.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// run is main without os: it returns the process exit code and writes only to
// stdout, stderr and the costs file.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Getenv("SOLC_PAR_CHILD") != "" {
		fmt.Fprintln(stderr, "solc-par: SOLC_PAR_SOLC points at solc-par itself")
		return 1
	}
	solc := os.Getenv("SOLC_PAR_SOLC")
	if solc == "" {
		fmt.Fprintln(stderr, "solc-par: SOLC_PAR_SOLC is not set; point it at the real solc binary")
		return 1
	}
	solc, err := exec.LookPath(solc)
	if err != nil {
		fmt.Fprintln(stderr, "solc-par: SOLC_PAR_SOLC:", err)
		return 1
	}
	// Every solc dies with solc-par: a shard that outlived a cancelled build would
	// keep a core busy until it tried to write to the closed pipe.
	ctx, status, stop := signalContext()
	defer stop()
	// Pretty-printed shard bodies would be glued back together compactly, and solc
	// ignores stdin when it is given an input file.
	if !slices.Contains(args, "--standard-json") || slices.Contains(args, "--pretty-json") ||
		slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--json-indent") }) ||
		hasInputFile(args) {
		cmd := exec.CommandContext(ctx, solc, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		cmd.Env = childEnv()
		code, err := exitCode(cmd.Run())
		if s := status(); s != 0 {
			return s
		}
		if err != nil {
			fmt.Fprintln(stderr, "solc-par:", err)
		}
		return code
	}
	jobs := defaultJobs("/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes")
	if v := os.Getenv("SOLC_PAR_JOBS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(stderr, "solc-par: SOLC_PAR_JOBS=%q is not an integer\n", v)
			return 1
		}
		jobs = n
	}
	in, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "solc-par: reading stdin:", err)
		return 1
	}

	costsFile := costsPath(args)
	learned := loadCosts(costsFile)
	shards, owner, inputs := plan(in, jobs, learned)
	if shards == nil {
		return emit(stdout, stderr, solcRun(ctx, solc, args, in), status)
	}

	// Each shard's output is decoded as soon as it exits, while the others still run.
	res := make([]result, len(shards))
	var wg sync.WaitGroup
	for i, s := range shards {
		wg.Go(func() {
			res[i] = solcRun(ctx, solc, args, s)
			if res[i].code == 0 {
				if res[i].dec, res[i].decErr = decode(res[i].stdout, i, owner); res[i].decErr == nil {
					res[i].stdout = nil // decode kept copies of what the merge needs
				}
			}
		})
	}
	wg.Wait()
	if s := status(); s != 0 {
		return s
	}

	merged, sizes, err := merge(res, owner, inputs)
	if err != nil {
		// Anything but a clean, error-free run from every shard is compiled again by
		// stock solc, whose output and exit code are returned verbatim: the shape of
		// an error response and the diagnostics in it are then exactly solc's.
		if os.Getenv("SOLC_PAR_DEBUG") != "" {
			fmt.Fprintln(stderr, "solc-par: compiling again with stock solc:", err)
		}
		return emit(stdout, stderr, solcRun(ctx, solc, args, in), status)
	}
	var seen [][]byte // anything solc writes to stderr is written once per shard
	for _, r := range res {
		if len(r.stderr) > 0 && !slices.ContainsFunc(seen, func(b []byte) bool { return bytes.Equal(b, r.stderr) }) {
			seen = append(seen, r.stderr)
			stderr.Write(r.stderr)
		}
	}
	if _, err := stdout.Write(append(merged, '\n')); err != nil { // solc ends its JSON with a newline
		fmt.Fprintln(stderr, "solc-par: writing stdout:", err)
		return 1
	}
	saveCosts(costsFile, learned, sizes)
	return 0
}

// signalContext returns a context that SIGINT or SIGTERM cancels, a function
// reporting the exit status a shell gives a process killed by the signal received
// (0 while none was), and a function that releases the handler.
func signalContext() (ctx context.Context, status func() int, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	var got atomic.Int32
	go func() {
		if s, ok := <-sigs; ok {
			if n, ok := s.(syscall.Signal); ok {
				got.Store(int32(n))
			}
			cancel()
		}
	}()
	status = func() int {
		if n := got.Load(); n != 0 {
			return 128 + int(n)
		}
		return 0
	}
	stop = func() { signal.Stop(sigs); close(sigs); cancel() }
	return ctx, status, stop
}

// defaultJobs returns the CPUs available to the process, capped on Linux at one
// shard per GiB of any cgroup memory limit so that a memory-limited container
// does not OOM a shard and fall back to stock solc.
func defaultJobs(cgroupFiles ...string) int {
	n := runtime.GOMAXPROCS(0) // unlike NumCPU, honours a cgroup CPU quota
	for _, f := range cgroupFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if lim, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil && lim > 0 && lim < 1<<60 {
			return min(n, max(1, int(lim>>30)))
		}
	}
	return n
}

// valueOptions are the solc options that take their value as the next argument.
var valueOptions = []string{
	"--base-path", "--include-path", "--allow-paths", "-o", "--output-dir",
	"--evm-version", "--revert-strings", "--debug-info", "--stop-after", "--machine",
	"--yul-dialect", "--libraries", "--json-indent", "--combined-json", "--metadata-hash",
	"--optimize-runs", "--yul-optimizations", "--model-checker-contracts",
	"--model-checker-engine", "--model-checker-ext-calls", "--model-checker-invariants",
	"--model-checker-solvers", "--model-checker-targets", "--model-checker-timeout",
	"--model-checker-bmc-loop-iterations",
}

// hasInputFile reports whether args name an input file (any argument that is not
// an option or an option's value, or the value of --input-file; "-" is stdin), in
// which case solc does not read stdin. An option unknown here is taken for a file,
// which only costs the sharding.
func hasInputFile(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--input-file": // a missing value is solc's error to report
			if i+1 >= len(args) || args[i+1] != "-" {
				return true
			}
			i++
		case strings.HasPrefix(a, "--input-file="):
			if a != "--input-file=-" {
				return true
			}
		case slices.Contains(valueOptions, a):
			i++
		case !strings.HasPrefix(a, "-"):
			return true
		}
	}
	return false
}

// result is one solc run: its stdout and stderr, its exit code, the error when it
// could not be run to an exit code (not started, killed) and, for a shard that
// exited 0, its decoded output.
type result struct {
	stdout, stderr []byte
	code           int
	err            error
	dec            decoded
	decErr         error
}

func solcRun(ctx context.Context, solc string, args []string, in []byte) result {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, solc, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &stdout, &stderr
	cmd.Env = childEnv()
	code, err := exitCode(cmd.Run())
	return result{stdout: stdout.Bytes(), stderr: stderr.Bytes(), code: code, err: err}
}

// childEnv marks solc's environment so that solc-par refuses to run itself.
func childEnv() []string { return append(os.Environ(), "SOLC_PAR_CHILD=1") }

// emit writes a stock run's stderr and stdout verbatim and returns its exit code,
// or the signal's status when the run was interrupted.
func emit(stdout, stderr io.Writer, r result, status func() int) int {
	if s := status(); s != 0 {
		return s
	}
	stderr.Write(r.stderr)
	if r.err != nil {
		fmt.Fprintln(stderr, "solc-par:", r.err)
	}
	if _, err := stdout.Write(r.stdout); err != nil && r.code == 0 {
		fmt.Fprintln(stderr, "solc-par: writing stdout:", err)
		return 1
	}
	return r.code
}

// exitCode maps cmd.Run's error to solc's exit code. An error that is not an exit
// status (solc could not be started, or was killed) is returned too, with code 1.
func exitCode(err error) (int, error) {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ee.ExitCode(), nil
	default:
		return 1, err
	}
}

// input is the part of a standard-json input that decides the plan.
type input struct {
	Language string `json:"language"`
	Sources  map[string]struct {
		Content string `json:"content"`
	} `json:"sources"`
	Settings struct {
		StopAfter    json.RawMessage `json:"stopAfter"`
		Remappings   []string        `json:"remappings"`
		ModelChecker struct {
			Engine string `json:"engine"`
		} `json:"modelChecker"`
		OutputSelection map[string]map[string][]string `json:"outputSelection"`
	} `json:"settings"`
}

// plan returns one standard-json input per shard, the owning shard of each
// selected file and the set of input sources, or nil when the input should go to
// solc unchanged: a language other than Solidity (Yul and AST inputs are untested),
// nothing to generate (stopAfter, no code-generation output), fewer than two files
// to spread, a file whose selection would make its shard skip code generation that
// a full run performs, or an SMT run (its diagnostics are not deterministic across
// processes).
func plan(in []byte, jobs int, learned map[string]int) ([][]byte, map[string]int, map[string]bool) {
	var inp input
	var top, settings map[string]json.RawMessage
	if jobs <= 1 || json.Unmarshal(in, &inp) != nil || inp.Language != "Solidity" ||
		json.Unmarshal(in, &top) != nil || json.Unmarshal(top["settings"], &settings) != nil ||
		(len(inp.Settings.StopAfter) > 0 && string(inp.Settings.StopAfter) != "null") ||
		(inp.Settings.ModelChecker.Engine != "" && inp.Settings.ModelChecker.Engine != "none") {
		return nil, nil, nil
	}

	// Per-file selection: the "*" wildcard entry merged with the file's own entry.
	all := inp.Settings.OutputSelection
	sel := map[string]map[string][]string{}
	for f := range inp.Sources {
		m := map[string][]string{}
		for _, key := range []string{"*", f} {
			for c, outs := range all[key] {
				m[c] = append(m[c], outs...)
			}
		}
		codegen := false
		for c, outs := range m {
			if len(outs) == 0 {
				delete(m, c)
				continue
			}
			if c != "" {
				codegen = codegen || slices.ContainsFunc(outs, isCodegen)
			}
		}
		if len(m) == 0 {
			continue
		}
		if !codegen {
			return nil, nil, nil
		}
		sel[f] = m
	}
	if len(sel) < 2 {
		return nil, nil, nil
	}

	src, inputs := map[string]string{}, map[string]bool{}
	for f, s := range inp.Sources {
		src[f], inputs[f] = s.Content, true
	}
	files := slices.Collect(maps.Keys(sel))
	groups := partition(files, weights(files, costs(src, inp.Settings.Remappings), learned), jobs)

	owner := map[string]int{}
	shards := make([][]byte, len(groups))
	for i, g := range groups {
		s := map[string]map[string][]string{}
		for _, f := range g {
			s[f] = sel[f]
			owner[f] = i
		}
		settings["outputSelection"] = marshal(s)
		top["settings"] = object(settings)
		shards[i] = object(top)
	}
	return shards, owner, inputs
}

// isCodegen reports whether a contract-level output makes solc generate code, the
// same set as solc's isBinaryRequested (evm.methodIdentifiers needs no code).
func isCodegen(o string) bool {
	return o != "evm.methodIdentifiers" && (o == "*" || strings.HasPrefix(o, "evm") ||
		strings.HasPrefix(o, "ir") || o == "yulCFGJson" || o == "ethdebug")
}

// weights returns each file's packing weight in hundredths of a byte: the bytecode
// JSON solc generated for it last time plus 1% of its import closure, which spreads
// files without code instead of piling them into one shard. Files never compiled
// before get their import closure scaled to the learned sizes (or alone, when
// nothing is learned yet).
// ponytail: the 1% blend beat 10% and 0.1% on v4-core, openzeppelin and gmx.
func weights(files []string, closure, learned map[string]int) map[string]int {
	known, knownClosure := 0, 0
	for _, f := range files {
		if n, ok := learned[f]; ok {
			known, knownClosure = known+n, knownClosure+closure[f]
		}
	}
	w := map[string]int{}
	for _, f := range files {
		n, ok := learned[f]
		if !ok && knownClosure > 0 {
			n = int(float64(closure[f]) * float64(known) / float64(knownClosure))
		}
		w[f] = 100*n + closure[f] + 1 // never 0: LPT would pile zeros into one shard, leaving others empty
	}
	return w
}

// partition packs files into at most n groups, longest-processing-time first.
func partition(files []string, cost map[string]int, n int) [][]string {
	slices.SortFunc(files, func(a, b string) int {
		if cost[a] != cost[b] {
			return cost[b] - cost[a]
		}
		return strings.Compare(a, b)
	})
	n = min(n, len(files))
	groups, load := make([][]string, n), make([]int, n)
	for _, f := range files {
		k := 0
		for i := range load {
			if load[i] < load[k] {
				k = i
			}
		}
		groups[k] = append(groups[k], f)
		load[k] += cost[f]
	}
	return groups
}

// ponytail: regex import scan, good enough for a cost proxy (may catch imports in comments).
var importRe = regexp.MustCompile(`^import\s[^;]*?["']([^"']+)["']`)

// imports returns the paths of the import statements in c: an "import" that is
// first on its line, then the first quoted string before the ";". The word is
// located with strings.Index and the regex anchored at it, which is 11x faster
// than one multiline regex tried at every line start.
func imports(c string) []string {
	var paths []string
	for i := 0; i < len(c); {
		j := strings.Index(c[i:], "import")
		if j < 0 {
			break
		}
		j += i
		i = j + len("import")
		k := j
		for k > 0 && strings.IndexByte(" \t\r\f", c[k-1]) >= 0 {
			k--
		}
		if k > 0 && c[k-1] != '\n' {
			continue
		}
		if m := importRe.FindStringSubmatchIndex(c[j:]); m != nil {
			paths = append(paths, c[j+m[2]:j+m[3]])
			i = j + m[1]
		}
	}
	return paths
}

// costs returns, per file, the bytes of the file plus everything it transitively imports.
func costs(src map[string]string, remappings []string) map[string]int {
	deps := map[string][]string{}
	for f, c := range src {
		for _, p := range imports(c) {
			deps[f] = append(deps[f], resolve(p, f, remappings))
		}
	}
	cost := map[string]int{}
	for f := range src {
		seen, stack := map[string]bool{}, []string{f}
		for len(stack) > 0 {
			g := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			c, ok := src[g]
			if seen[g] || !ok {
				continue
			}
			seen[g] = true
			cost[f] += len(c)
			stack = append(stack, deps[g]...)
		}
	}
	return cost
}

// resolve maps an import path to a source unit name like solc does: relative
// paths against the importer, then the longest-context, longest-prefix remapping.
func resolve(imp, from string, remappings []string) string {
	if strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../") {
		imp = path.Join(path.Dir(from), imp)
	}
	best, bestCtx, bestPre := imp, -1, -1
	for _, r := range remappings {
		lhs, target, ok := strings.Cut(r, "=")
		if !ok {
			continue
		}
		ctx, prefix, hasCtx := strings.Cut(lhs, ":")
		if !hasCtx {
			ctx, prefix = "", lhs
		}
		if strings.HasPrefix(from, ctx) && strings.HasPrefix(imp, prefix) &&
			(len(ctx) > bestCtx || len(ctx) == bestCtx && len(prefix) > bestPre) {
			best, bestCtx, bestPre = target+imp[len(prefix):], len(ctx), len(prefix)
		}
	}
	return best
}

// decoded is one shard's output split up for merging; bodies stay raw bytes.
type decoded struct {
	top, contracts, sources map[string]json.RawMessage
	ids                     map[string]string
	errors                  []json.RawMessage
	hasError                bool           // any diagnostic of severity "error"
	sizes                   map[string]int // owned file -> bytes of its bytecode JSON
}

// size unmarshals to the length of a JSON value without copying it.
type size int

func (s *size) UnmarshalJSON(b []byte) error { *s = size(len(b)); return nil }

// decode splits shard i's output, keeping the contracts of the files it owns.
func decode(out []byte, i int, owner map[string]int) (decoded, error) {
	var d decoded
	if err := json.Unmarshal(out, &d.top); err != nil {
		return d, fmt.Errorf("output is not JSON: %w", err)
	} else if d.top == nil {
		return d, errors.New("output is not a JSON object")
	}
	var c map[string]json.RawMessage
	if err := errors.Join(unmarshalOpt(d.top["contracts"], &c), unmarshalOpt(d.top["sources"], &d.sources),
		unmarshalOpt(d.top["errors"], &d.errors)); err != nil {
		return d, err
	}
	// The merge rebuilds these three from every shard's parts; dropping the whole
	// shard's copy here frees it while the other shards still run.
	for _, k := range []string{"contracts", "sources", "errors"} {
		delete(d.top, k)
	}
	for _, e := range d.errors {
		var x struct {
			Severity string `json:"severity"`
		}
		if json.Unmarshal(e, &x) == nil && x.Severity == "error" {
			d.hasError = true
		}
	}
	d.contracts, d.sizes, d.ids = map[string]json.RawMessage{}, map[string]int{}, map[string]string{}
	for f, own := range owner {
		if own == i {
			d.sizes[f] = 0
		}
	}
	for f, v := range c {
		if _, owned := d.sizes[f]; !owned {
			continue
		}
		d.contracts[f] = v
		var cs map[string]struct {
			EVM struct {
				Bytecode         size `json:"bytecode"`
				DeployedBytecode size `json:"deployedBytecode"`
			} `json:"evm"`
		}
		json.Unmarshal(v, &cs) // sizes only steer packing
		for _, x := range cs {
			d.sizes[f] += int(x.EVM.Bytecode + x.EVM.DeployedBytecode)
		}
	}
	for f, v := range d.sources {
		var x struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(v, &x); err != nil {
			return d, fmt.Errorf("sources[%q]: %w", f, err)
		}
		d.ids[f] = string(x.ID)
	}
	return d, nil
}

// merge combines decoded shard outputs. contracts and sources entries come from
// the owning shard; source ids must agree across shards; errors are de-duplicated
// in first-seen order. It also returns the bytecode size of every owned file.
//
// It fails, and the caller falls back to one stock run, when a shard did not exit
// cleanly or produced no JSON object; when a shard reports an error, since a
// code-generation error (stack too deep, ...) makes solc emit only that error and
// only the shard owning the contract sees it; when solc loaded a source that was
// not in the input (import callback), which no shard was told to select; and when
// a top-level key other than the three merged ones differs between shards, which
// no solc emits today and a future one might (the merge takes it from shard 0).
//
// ponytail: solc keeps only the first 255 warnings of a compilation and then adds
// 4591 "There are more than 256 warnings". Each shard applies that cap to its own
// list, and warnings come out in the order solc visits files and generates
// contracts, which differs per shard. Every warning of a capped stock run is in the
// merge, but which 255 stock keeps depends on that global order, which shard
// outputs do not carry; so past the cap the merge has more warnings (aave-v3: 268
// vs 256), whose set and order depend on the partition. Below the cap the set
// matches stock; only the order may differ.
func merge(res []result, owner map[string]int, inputs map[string]bool) ([]byte, map[string]int, error) {
	contracts, sources, ids := map[string]json.RawMessage{}, map[string]json.RawMessage{}, map[string]string{}
	sizes := map[string]int{}
	var errs [][]byte
	seen := map[string]bool{}
	for i, r := range res {
		switch {
		case r.err != nil:
			return nil, nil, fmt.Errorf("shard %d: %w", i, r.err)
		case r.code != 0:
			return nil, nil, fmt.Errorf("shard %d: solc exited with code %d", i, r.code)
		case r.decErr != nil:
			return nil, nil, fmt.Errorf("shard %d: %w", i, r.decErr)
		case r.dec.hasError:
			return nil, nil, fmt.Errorf("shard %d: compilation error", i)
		}
		if i > 0 {
			if len(r.dec.top) != len(res[0].dec.top) {
				return nil, nil, fmt.Errorf("shard %d: different top-level keys", i)
			}
			for k, v := range r.dec.top {
				if !bytes.Equal(v, res[0].dec.top[k]) {
					return nil, nil, fmt.Errorf("shard %d: top-level %q differs", i, k)
				}
			}
		}
		maps.Copy(contracts, r.dec.contracts)
		maps.Copy(sizes, r.dec.sizes)
		for f, v := range r.dec.sources {
			if !inputs[f] {
				return nil, nil, fmt.Errorf("source %q was loaded by solc and is not in the input", f)
			}
			if prev, ok := ids[f]; ok && prev != r.dec.ids[f] {
				return nil, nil, fmt.Errorf("source id mismatch for %q: %s vs %s (shard %d)", f, prev, r.dec.ids[f], i)
			}
			ids[f] = r.dec.ids[f]
			if own, ok := owner[f]; ok && own == i || !ok && sources[f] == nil {
				sources[f] = v
			}
		}
		for _, v := range r.dec.errors {
			key := string(v)
			// solc reports 2394 (transient storage) once per compilation, at the first
			// tstore in its analysis order, so every shard has one at a different place.
			// ponytail: keep the first; its location can differ from an unsharded run.
			var x struct {
				ErrorCode string `json:"errorCode"`
				Severity  string `json:"severity"`
			}
			if json.Unmarshal(v, &x) == nil && x.ErrorCode == "2394" && x.Severity == "warning" {
				key = "2394"
			}
			if !seen[key] {
				seen[key] = true
				errs = append(errs, v)
			}
		}
	}
	top := res[0].dec.top
	set := func(k string, v json.RawMessage, n int) {
		if n > 0 {
			top[k] = v
		} else {
			delete(top, k)
		}
	}
	set("contracts", object(contracts), len(contracts))
	set("sources", object(sources), len(sources))
	set("errors", append(append([]byte("["), bytes.Join(errs, []byte(","))...), ']'), len(errs))
	return object(top), sizes, nil
}

// unmarshalOpt unmarshals b into v, or does nothing for a key that was absent.
func unmarshalOpt(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// encoder returns a JSON encoder into b without HTML escaping, so raw bodies and
// keys keep their exact bytes.
func encoder(b *bytes.Buffer) *json.Encoder {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	return e
}

// marshal encodes v without HTML escaping or the trailing newline.
func marshal(v any) []byte {
	var b bytes.Buffer
	if err := encoder(&b).Encode(v); err != nil {
		panic(fmt.Sprintf("solc-par: internal: marshal: %v", err)) // only maps, slices and RawMessage from valid JSON reach here
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// object encodes m with sorted keys, copying the (already valid) raw values as
// they are: re-encoding 100+ MB of shard output through encoding/json costs seconds.
// The buffer is sized up front (plus the newline run appends) so nothing is copied twice.
func object(m map[string]json.RawMessage) []byte {
	n := 3
	for k, v := range m {
		n += len(k) + 4 + len(v)
	}
	var b bytes.Buffer
	b.Grow(n)
	e := encoder(&b)
	b.WriteByte('{')
	for i, k := range slices.Sorted(maps.Keys(m)) {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := e.Encode(k); err != nil {
			panic(fmt.Sprintf("solc-par: internal: object: %v", err))
		}
		b.Truncate(b.Len() - 1) // Encode's newline
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// costsPath is where learned sizes live: SOLC_PAR_COSTS if set ("" disables),
// else one file per project (--base-path, or the working directory) in the
// user cache directory.
func costsPath(args []string) string {
	if v, ok := os.LookupEnv("SOLC_PAR_COSTS"); ok {
		return v
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	root, _ := os.Getwd()
	for i, a := range args {
		if a == "--base-path" && i+1 < len(args) {
			root = args[i+1]
		} else if v, ok := strings.CutPrefix(a, "--base-path="); ok {
			root = v
		}
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	h := fnv.New64a()
	h.Write([]byte(root))
	return filepath.Join(dir, "solc-par", fmt.Sprintf("%016x.json", h.Sum64()))
}

// loadCosts reads the sizes saved by the last run; a missing or damaged file is empty.
func loadCosts(name string) map[string]int {
	m := map[string]int{}
	if b, err := os.ReadFile(name); err == nil {
		json.Unmarshal(b, &m) // a damaged file only costs balance
	}
	return m
}

// saveCosts records this run's sizes over the old ones. Best effort: concurrent
// runs of the same project may drop each other's updates, which only costs balance.
func saveCosts(name string, old, sizes map[string]int) {
	if name == "" || len(sizes) == 0 {
		return
	}
	maps.Copy(old, sizes)
	tmp := fmt.Sprintf("%s.%d", name, os.Getpid())
	if os.MkdirAll(filepath.Dir(name), 0o755) == nil && os.WriteFile(tmp, marshal(old), 0o644) == nil {
		if os.Rename(tmp, name) != nil {
			os.Remove(tmp)
		}
	}
}
