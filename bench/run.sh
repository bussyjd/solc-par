#!/usr/bin/env bash
# Benchmark solc-par against stock solc on Foundry projects.
#
#   bench/run.sh <solc binary> <project dir>...
#
# Per project, in a scratch out/cache so the project's own build is untouched:
#   stock       forge build --force --use <solc>          (NSTOCK runs, default 2)
#   par-cold    forge build --force --use solc-par, empty learned-costs file (1 run)
#   par-warm    same again, reusing the costs file learned by the previous run (NPAR runs, default 2)
# then compares every artifact of the stock and par-warm builds (abi, bytecode,
# deployedBytecode, metadata) with bench/compare-artifacts.py.
#
# Env: SOLC_PAR (binary, default ../solc-par), SOLC_PAR_JOBS (default: all CPUs),
# NSTOCK, NPAR, and any FOUNDRY_* overrides you want (for example FOUNDRY_VIA_IR=true).
# Results: results.tsv (project, variant, run, seconds, exit code) and a summary on stdout.
set -u
here=$(cd "$(dirname "$0")" && pwd)
solc=$1; shift
par=${SOLC_PAR:-$here/../solc-par}
jobs=${SOLC_PAR_JOBS:-$(getconf _NPROCESSORS_ONLN)}
scratch=${BENCH_SCRATCH:-$(mktemp -d -t solc-par-bench)}
tsv=${BENCH_TSV:-$here/results.tsv}
echo -e "project\tvariant\trun\tseconds\trc" > "$tsv"

now() { python3 -c 'import time;print(time.time())'; }
timed() { # <project> <variant> <run> <out dir> <cmd...>
  local p=$1 v=$2 r=$3 o=$4; shift 4
  rm -rf "$o"; mkdir -p "$o"
  local s; s=$(now)
  FOUNDRY_OUT=$o/out FOUNDRY_CACHE_PATH=$o/cache "$@" > "$o.log" 2>&1; local rc=$?
  local e; e=$(python3 -c "import time;print(round(time.time()-$s,1))")
  echo -e "$p\t$v\t$r\t$e\t$rc" | tee -a "$tsv"
}

echo "# $(sysctl -n machdep.cpu.brand_string 2>/dev/null || grep -m1 'model name' /proc/cpuinfo) / $(getconf _NPROCESSORS_ONLN) cpus, jobs=$jobs"
echo "# $(forge --version | head -1); solc $("$solc" --version | tail -1)"
for dir in "$@"; do
  p=$(basename "$dir"); cd "$dir" || exit 1
  costs=$scratch/$p.costs.json; rm -f "$costs"
  for r in $(seq 1 "${NSTOCK:-2}"); do
    timed "$p" stock "$r" "$scratch/$p-stock" forge build --force --use "$solc"
  done
  timed "$p" par-cold 1 "$scratch/$p-par" env SOLC_PAR_SOLC="$solc" SOLC_PAR_JOBS="$jobs" SOLC_PAR_COSTS="$costs" forge build --force --use "$par"
  for r in $(seq 1 "${NPAR:-2}"); do
    timed "$p" par-warm "$r" "$scratch/$p-par" env SOLC_PAR_SOLC="$solc" SOLC_PAR_JOBS="$jobs" SOLC_PAR_COSTS="$costs" forge build --force --use "$par"
  done
  echo "## $p artifacts (stock vs par-warm):"
  python3 "$here/compare-artifacts.py" "$scratch/$p-stock/out" "$scratch/$p-par/out" || fail=1
  rm -rf "$scratch/$p-stock" "$scratch/$p-par"
done
exit "${fail:-0}"
