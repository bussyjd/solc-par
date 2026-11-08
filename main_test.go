package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake solc when FAKE_SOLC=1.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_SOLC") == "1" {
		os.Exit(fakeSolc())
	}
	os.Exit(m.Run())
}

// fakeSolc logs each call's args and selected files to FAKE_LOG and answers
// like solc: ids for every source, ast + contracts for selected files only,
// one warning shared by every run, one error per selected file and a
// once-per-compilation 2394 warning at the first selected file. A call whose
// selection has the "*" key is an unsharded (stock) run.
//
//	FAKE_FAIL=<file>  "boom" and exit 7 when <file> is selected (exit 0 with FAKE_FAIL_OK=1);
//	                  with FAKE_FAIL_STOCK=1 the stock run prints "stock boom" and exits 7
//	FAKE_ERROR=<file> only an error of severity "error" when <file> is selected or in a stock run,
//	                  the shape solc has for a code-generation error
//	FAKE_EXTRA=1      a source "extra.sol" that was not in the input, as solc's import callback would add
//	FAKE_BAD_ID=1     d.sol gets a different id in the shard that selects it
//	FAKE_HANG=1       a compile that never finishes
//	FAKE_TOP=same     an extra top-level key "extra" that every run reports alike;
//	FAKE_TOP=shard    the same key with a value that depends on the selection
func fakeSolc() int {
	logf, _ := os.OpenFile(os.Getenv("FAKE_LOG"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	defer logf.Close()
	if !slices.Contains(os.Args[1:], "--standard-json") {
		os.Stdout.WriteString("fake solc " + strings.Join(os.Args[1:], " "))
		return 3
	}
	if os.Getenv("FAKE_HANG") == "1" {
		time.Sleep(time.Minute)
		return 1
	}
	var in struct {
		Sources  map[string]json.RawMessage
		Settings struct {
			OutputSelection map[string]map[string][]string
		}
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		panic(err)
	}
	selected := slices.Sorted(maps.Keys(in.Settings.OutputSelection))
	stock := slices.Contains(selected, "*")
	fmt.Fprintf(logf, "%s | %s\n", strings.Join(os.Args[1:], " "), strings.Join(selected, ","))
	if f := os.Getenv("FAKE_FAIL"); f != "" {
		if slices.Contains(selected, f) {
			os.Stdout.WriteString("boom")
			if os.Getenv("FAKE_FAIL_OK") == "1" {
				return 0
			}
			return 7
		}
		if stock && os.Getenv("FAKE_FAIL_STOCK") == "1" {
			os.Stdout.WriteString("stock boom")
			return 7
		}
	}
	if f := os.Getenv("FAKE_ERROR"); f != "" && (stock || slices.Contains(selected, f)) {
		os.Stdout.WriteString(codegenError)
		return 0
	}
	names := slices.Sorted(maps.Keys(in.Sources))
	var b strings.Builder
	b.WriteString(`{`)
	switch os.Getenv("FAKE_TOP") {
	case "same":
		b.WriteString(`"extra":1,`)
	case "shard":
		fmt.Fprintf(&b, `"extra":%q,`, strings.Join(selected, ","))
	}
	b.WriteString(`"errors":[{"message":"shared <warning>","severity":"warning"}`)
	for _, f := range selected {
		fmt.Fprintf(&b, `,{"message":"err %s","severity":"warning"}`, f)
	}
	fmt.Fprintf(&b, `,{"errorCode":"2394","severity":"warning","file":%q}],"sources":{`, selected[0])
	for i, f := range names {
		id := i
		if f == "d.sol" && slices.Contains(selected, "d.sol") && os.Getenv("FAKE_BAD_ID") == "1" {
			id += 100
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `%q:{"id":%d`, f, id)
		if _, ok := in.Settings.OutputSelection[f][""]; ok {
			fmt.Fprintf(&b, `,"ast":{"src":%q}`, f)
		}
		b.WriteString("}")
	}
	if os.Getenv("FAKE_EXTRA") == "1" {
		b.WriteString(`,"extra.sol":{"id":99}`)
	}
	b.WriteString(`},"contracts":{`)
	first := true
	for _, f := range selected {
		if in.Settings.OutputSelection[f]["*"] == nil {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, `%q:{"C":{"evm":{"bytecode":{"object":"0x%s"}},"n":1.00000000000000000001}}`, f, f)
	}
	b.WriteString("}}")
	os.Stdout.WriteString(b.String())
	return 0
}

const codegenError = `{"errors":[{"severity":"error","message":"stack too deep"}]}`

func setup(t *testing.T, jobs string) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := t.TempDir() + "/log"
	t.Setenv("FAKE_SOLC", "1")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("SOLC_PAR_SOLC", exe)
	t.Setenv("SOLC_PAR_JOBS", jobs)
	t.Setenv("SOLC_PAR_COSTS", t.TempDir()+"/costs.json")
	for _, k := range []string{"SOLC_PAR_CHILD", "FAKE_FAIL", "FAKE_FAIL_OK", "FAKE_FAIL_STOCK", "FAKE_ERROR", "FAKE_EXTRA", "FAKE_BAD_ID", "FAKE_HANG", "FAKE_TOP"} {
		t.Setenv(k, "")
	}
	return log
}

func calls(log string) []string {
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// lastIsStock reports whether the final solc call was an unsharded run.
func lastIsStock(log string) bool {
	c := calls(log)
	return strings.HasSuffix(c[len(c)-1], "| *") || strings.Contains(c[len(c)-1], "| *,")
}

const inputTmpl = `{"language":"Solidity","sources":{
 "a.sol":{"content":"import \"./b.sol\";\nimport {X} from 'lib/c.sol';\ncontract A {}"},
 "b.sol":{"content":"contract B {}"},
 "lib/c.sol":{"content":"contract C {}"},
 "d.sol":{"content":"contract D {}"}},
 "settings":{"optimizer":{"runs":1.00000000000000000001},"outputSelection":%SEL%}}`

func stdjson(sel string) *strings.Reader {
	return strings.NewReader(strings.Replace(inputTmpl, "%SEL%", sel, 1))
}

const fullSel = `{"*":{"":["ast"],"*":["abi","evm.bytecode.object"]}}`

func TestShardedMerge(t *testing.T) {
	log := setup(t, "2")
	var out, errb bytes.Buffer
	if code := run([]string{"--allow-paths", "x", "--standard-json"}, stdjson(fullSel), &out, &errb); code != 0 {
		t.Fatalf("code %d: %s %s", code, out.String(), errb.String())
	}
	c := calls(log)
	if len(c) != 2 {
		t.Fatalf("want 2 shards, got %q", c)
	}
	// LPT: a.sol (imports b and c) is heaviest and gets a shard to itself.
	if c[0] != "--allow-paths x --standard-json | a.sol" && c[1] != "--allow-paths x --standard-json | a.sol" {
		t.Fatalf("partition: %q", c)
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("}\n")) {
		t.Error("no trailing newline")
	}
	var res struct {
		Contracts map[string]json.RawMessage
		Sources   map[string]map[string]json.RawMessage
		Errors    []map[string]string
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Contracts) != 4 || len(res.Sources) != 4 {
		t.Fatalf("merge: %s", out.String())
	}
	for f, s := range res.Sources {
		if s["ast"] == nil {
			t.Errorf("%s: ast lost", f)
		}
	}
	if !strings.Contains(out.String(), `"n":1.00000000000000000001`) || !strings.Contains(out.String(), "<warning>") {
		t.Errorf("raw bytes not preserved: %s", out.String())
	}
	var msgs []string
	for _, e := range res.Errors {
		msgs = append(msgs, e["message"])
	}
	if msgs[0] != "shared <warning>" || len(msgs) != 6 || strings.Count(out.String(), "shared") != 1 ||
		strings.Count(out.String(), "2394") != 1 {
		t.Errorf("errors not deduped in order: %q", msgs)
	}
}

func TestPassthrough(t *testing.T) {
	for name, tc := range map[string]struct {
		jobs, sel string
		args      []string
	}{
		"jobs=1":            {"1", fullSel, nil},
		"jobs=0":            {"0", fullSel, nil},
		"no codegen":        {"4", `{"*":{"":["ast"],"*":["abi"]}}`, nil},
		"methodIdentifiers": {"4", `{"*":{"*":["abi","evm.methodIdentifiers"]}}`, nil},
		"one file":          {"4", `{"a.sol":{"*":["evm.bytecode.object"]},"*":{"":["ast"]}}`, nil},
		"mixed selection":   {"4", `{"a.sol":{"*":["evm.bytecode.object"]},"b.sol":{"*":["abi"]},"*":{"":["ast"]}}`, nil},
		"ast-only file":     {"4", `{"a.sol":{"*":["evm.bytecode.object"]},"b.sol":{"":["ast"]},"d.sol":{"*":["*"]}}`, nil},
		"stopAfter":         {"4", fullSel + `,"stopAfter":"parsing"`, nil},
		"modelChecker":      {"4", fullSel + `,"modelChecker":{"engine":"chc"}`, nil},
		"pretty-json":       {"4", fullSel, []string{"--pretty-json", "--standard-json"}},
		"json-indent":       {"4", fullSel, []string{"--standard-json", "--json-indent=4"}},
		"input file":        {"4", fullSel, []string{"--base-path", ".", "--standard-json", "in.json"}},
		"--input-file":      {"4", fullSel, []string{"--standard-json", "--input-file", "in.json"}},
	} {
		t.Run(name, func(t *testing.T) {
			log := setup(t, tc.jobs)
			args := tc.args
			if args == nil {
				args = []string{"--standard-json"}
			}
			var out, errb bytes.Buffer
			if code := run(args, stdjson(tc.sel), &out, &errb); code != 0 {
				t.Fatalf("code %d %s", code, errb.String())
			}
			if c := calls(log); len(c) != 1 { // a sharded run makes one call per shard
				t.Errorf("not passed through: %q", c)
			}
		})
	}

	if s, _, _ := plan([]byte("not json"), 4, nil); s != nil {
		t.Error("unparseable input was sharded")
	}
	for _, lang := range []string{`"Yul"`, `"SolidityAST"`, `""`} {
		in := strings.Replace(strings.Replace(inputTmpl, `"Solidity"`, lang, 1), "%SEL%", fullSel, 1)
		if s, _, _ := plan([]byte(in), 4, nil); s != nil {
			t.Errorf("language %s was sharded", lang)
		}
	}
	setup(t, "4")
	var out bytes.Buffer
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &out); code != 3 || out.String() != "fake solc --version" {
		t.Errorf("--version: code %d out %q", code, out.String())
	}

	t.Setenv("SOLC_PAR_SOLC", "")
	out.Reset()
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &out); code != 1 || !strings.Contains(out.String(), "SOLC_PAR_SOLC") {
		t.Errorf("unset solc: code %d out %q", code, out.String())
	}
	t.Setenv("SOLC_PAR_SOLC", t.TempDir()+"/no-such-solc")
	out.Reset()
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &out); code != 1 || !strings.Contains(out.String(), "no-such-solc") {
		t.Errorf("missing solc: code %d out %q", code, out.String())
	}
	setup(t, "x")
	out.Reset()
	if code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &out); code != 1 || !strings.Contains(out.String(), `SOLC_PAR_JOBS="x"`) {
		t.Errorf("bad jobs: code %d out %q", code, out.String())
	}
}

func TestHasInputFile(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"--standard-json"}, false},
		{[]string{"--standard-json", "-"}, false},
		{[]string{"--allow-paths", "a,b", "--base-path", "x", "--standard-json"}, false},
		{[]string{"--base-path=x", "--standard-json"}, false},
		{[]string{"--standard-json", "in.json"}, true},
		{[]string{"in.json", "--standard-json"}, true},
		{[]string{"--base-path", "x", "--standard-json", "in.json"}, true},
		{[]string{"--standard-json", "--no-such-option", "x"}, true}, // unknown: taken for a file
		{[]string{"--standard-json", "--input-file", "in.json"}, true},
		{[]string{"--standard-json", "--input-file=in.json"}, true},
		{[]string{"--standard-json", "--input-file", "-"}, false},
		{[]string{"--standard-json", "--input-file=-"}, false},
		{[]string{"--standard-json", "--input-file"}, true}, // solc rejects it at once, without reading stdin
	} {
		if got := hasInputFile(tc.args); got != tc.want {
			t.Errorf("hasInputFile(%q) = %v", tc.args, got)
		}
	}
}

// SIGTERM kills the shards and solc-par exits with the shell's status for it.
func TestSignalKillsShards(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows")
	}
	setup(t, "2")
	t.Setenv("FAKE_HANG", "1")
	var out, errb bytes.Buffer
	done := make(chan int, 1)
	start := time.Now()
	go func() { done <- run([]string{"--standard-json"}, stdjson(fullSel), &out, &errb) }()
	time.Sleep(500 * time.Millisecond) // let the shards start
	self, _ := os.FindProcess(os.Getpid())
	if err := self.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 128+int(syscall.SIGTERM) || out.Len() != 0 {
			t.Errorf("code %d out %q err %q", code, out.String(), errb.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("still running after %s: the shards were not killed", time.Since(start))
	}
}

func TestNamedContractKeys(t *testing.T) {
	log := setup(t, "2")
	var out, errb bytes.Buffer
	sel := `{"a.sol":{"*":["evm.bytecode.object"]},"b.sol":{"B":["ir"]}}`
	if code := run([]string{"--standard-json"}, stdjson(sel), &out, &errb); code != 0 || len(calls(log)) != 2 {
		t.Errorf("code %d calls %q %s", code, calls(log), errb.String())
	}
}

func TestRecursionGuard(t *testing.T) {
	setup(t, "4")
	t.Setenv("SOLC_PAR_CHILD", "1")
	var out bytes.Buffer
	if code := run([]string{"--version"}, strings.NewReader(""), &out, &out); code != 1 || !strings.Contains(out.String(), "itself") {
		t.Errorf("code %d out %q", code, out.String())
	}
}

// Every way a shard can fail ends in one stock run whose output is returned verbatim.
func TestFallbackToStock(t *testing.T) {
	for name, tc := range map[string]struct {
		env       map[string]string
		code      int
		out       string // exact when set, else a merged-looking stock answer is expected
		stockFail bool
	}{
		"shard exit code": {map[string]string{"FAKE_FAIL": "b.sol", "FAKE_FAIL_STOCK": "1"}, 7, "stock boom", true},
		"shard garbage":   {map[string]string{"FAKE_FAIL": "b.sol", "FAKE_FAIL_OK": "1"}, 0, "", false},
		"source id":       {map[string]string{"FAKE_BAD_ID": "1"}, 0, "", false},
		"codegen error":   {map[string]string{"FAKE_ERROR": "b.sol"}, 0, codegenError, false},
		"foreign source":  {map[string]string{"FAKE_EXTRA": "1"}, 0, "", false},
		"top-level key":   {map[string]string{"FAKE_TOP": "shard"}, 0, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			log := setup(t, "4")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var out, errb bytes.Buffer
			code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &errb)
			if code != tc.code || !lastIsStock(log) || len(calls(log)) != 5 {
				t.Errorf("code %d calls %q err %q", code, calls(log), errb.String())
			}
			if tc.out != "" && out.String() != tc.out {
				t.Errorf("out %q", out.String())
			}
			if tc.out == "" && (!strings.Contains(out.String(), `"contracts":{"*"`) || strings.HasSuffix(out.String(), "\n")) {
				t.Errorf("not the stock output verbatim: %q", out.String())
			}
			if strings.Contains(errb.String(), "solc-par") {
				t.Errorf("fallback is noisy: %q", errb.String())
			}
		})
	}
}

// A top-level key solc-par does not know is kept when every shard agrees on it.
func TestTopLevelKeyKept(t *testing.T) {
	log := setup(t, "2")
	t.Setenv("FAKE_TOP", "same")
	var out, errb bytes.Buffer
	if code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &errb); code != 0 ||
		len(calls(log)) != 2 || !strings.Contains(out.String(), `"extra":1`) {
		t.Errorf("code %d calls %q out %q", code, calls(log), out.String())
	}
}

func TestPartitionLPT(t *testing.T) {
	cost := map[string]int{"a": 10, "b": 9, "c": 3, "d": 3, "e": 2}
	got := partition([]string{"e", "d", "c", "b", "a"}, cost, 2)
	want := [][]string{{"a", "d"}, {"b", "c", "e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if len(partition([]string{"a"}, cost, 8)) != 1 {
		t.Error("more shards than files")
	}
}

func TestImports(t *testing.T) {
	for src, want := range map[string][]string{
		`import "a.sol";`:                                     {"a.sol"},
		"\n\n  \t import 'a.sol';":                            {"a.sol"},
		"\r\n\fimport {X} from \"a.sol\"; import \"b.sol\";":  {"a.sol"}, // second one is not first on its line
		"import {import} from 'a.sol';\n\timport\n\"b.sol\";": {"a.sol", "b.sol"},
		"// import \"a.sol\";\n/*\nimport 'b.sol'\n*/":        {"b.sol"}, // comments: not first on its line / is
		"importx \"a.sol\";\nreimport \"b.sol\";\nimport":     nil,
		`import "a.sol" import 'b.sol';`:                      {"a.sol"},
		"contract A {}":                                       nil,
		"":                                                    nil,
	} {
		if got := imports(src); !reflect.DeepEqual(got, want) {
			t.Errorf("imports(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestCosts(t *testing.T) {
	src := map[string]string{
		"src/A.sol":           "import \"./B.sol\";\nimport {S} from \"forge-std/S.sol\";",
		"src/B.sol":           "import '../src/A.sol';", // cycle
		"lib/fs/src/S.sol":    "xx",
		"lib/other/src/S.sol": "not me",
	}
	got := costs(src, []string{"forge-std/=lib/other/src/", "src/:forge-std/=lib/fs/src/"})
	a := len(src["src/A.sol"]) + len(src["src/B.sol"]) + 2
	if got["src/A.sol"] != a || got["src/B.sol"] != a || got["lib/fs/src/S.sol"] != 2 {
		t.Errorf("costs %v", got)
	}
}

func TestWeights(t *testing.T) {
	closure := map[string]int{"a": 1000, "b": 1000, "c": 500}
	// Nothing learned yet: the import closure alone decides.
	if got := weights([]string{"a", "b", "c"}, closure, nil); got["a"] != 1001 || got["c"] != 501 {
		t.Errorf("cold %v", got)
	}
	// Learned code size dominates; an unknown file's closure is scaled to learned
	// units (10000 code bytes for 2000 closure bytes here).
	got := weights([]string{"a", "b", "c"}, closure, map[string]int{"a": 10000, "b": 0})
	if got["a"] != 1001001 || got["b"] != 1001 || got["c"] != 250000+501 {
		t.Errorf("warm %v", got)
	}
}

// A run records each owned file's generated-code size; the next run packs by it.
func TestLearnedCosts(t *testing.T) {
	log := setup(t, "2")
	costsFile := os.Getenv("SOLC_PAR_COSTS")
	var out bytes.Buffer
	if code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	var learned map[string]int
	b, _ := os.ReadFile(costsFile)
	if err := json.Unmarshal(b, &learned); err != nil || len(learned) != 4 ||
		learned["d.sol"] != len(`{"object":"0xd.sol"}`) {
		t.Fatalf("learned %s (%v)", b, err)
	}
	// d.sol now looks like the heaviest file by far, so it gets a shard to itself.
	os.WriteFile(costsFile, []byte(`{"a.sol":10,"b.sol":10,"lib/c.sol":10,"d.sol":100000}`), 0o644)
	os.Remove(log)
	if code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &out); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	if c := calls(log); !slices.Contains(c, "--standard-json | d.sol") {
		t.Errorf("partition ignores learned costs: %q", c)
	}
	b, _ = os.ReadFile(costsFile)
	if json.Unmarshal(b, &learned) != nil || learned["d.sol"] != len(`{"object":"0xd.sol"}`) {
		t.Errorf("costs not updated: %s", b)
	}

	// SOLC_PAR_COSTS="" turns the cache off.
	t.Setenv("SOLC_PAR_COSTS", "")
	out.Reset()
	if code := run([]string{"--standard-json"}, stdjson(fullSel), &out, &out); code != 0 {
		t.Fatalf("no cache: code %d: %s", code, out.String())
	}
}

func TestCostsPath(t *testing.T) {
	t.Setenv("SOLC_PAR_COSTS", "")
	if costsPath(nil) != "" {
		t.Error(`"" should disable the cache`)
	}
	os.Unsetenv("SOLC_PAR_COSTS")
	wd, _ := os.Getwd()
	a, b, c := costsPath([]string{"--base-path", wd}), costsPath([]string{"--base-path=" + wd}), costsPath([]string{"--base-path", "."})
	if a == "" || a != b || a != c || a != costsPath(nil) {
		t.Errorf("base-path forms differ: %q %q %q %q", a, b, c, costsPath(nil))
	}
}

func TestDefaultJobs(t *testing.T) {
	cpus := runtime.GOMAXPROCS(0)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := defaultJobs(dir + "/missing"); got != cpus {
		t.Errorf("missing = %d, want %d", got, cpus)
	}
	if got := defaultJobs(write("max", "max\n")); got != cpus {
		t.Errorf("max = %d, want %d", got, cpus)
	}
	if got := defaultJobs(write("v1-unlim", "9223372036854771712\n")); got != cpus {
		t.Errorf("v1 unlimited = %d, want %d", got, cpus)
	}
	if got := defaultJobs(write("2g", fmt.Sprintf("%d\n", 2<<30))); got != min(cpus, 2) {
		t.Errorf("2 GiB = %d, want %d", got, min(cpus, 2))
	}
	if got := defaultJobs(write("512m", fmt.Sprintf("%d\n", 512<<20))); got != 1 {
		t.Errorf("512 MiB = %d, want 1", got)
	}
}
