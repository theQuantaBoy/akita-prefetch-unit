#!/usr/bin/env bash
set -e

echo -e "\033[1;36m>>> [1/3] Building updated Linux binaries...\033[0m"
go build -o ./matrixmultiplication_bin ./samples/matrixmultiplication
go build -o ./stencil2d_bin ./samples/stencil2d
go build -o ./bfs_bin ./samples/bfs
go build -o ./nw_bin ./samples/nw
go build -o ./matrixtranspose_bin ./samples/matrixtranspose
go build -o ./spmv_bin ./samples/spmv

MASTER_CSV="Project4_Final_Summary.csv"
echo "Benchmark,Config,Time_s,HitRate_Pct,MissRate_Pct,MSHRHitRate_Pct,Coverage_Pct,Accuracy_Pct,Pollution_Pct,PrefetchIssued" > "$MASTER_CSV"

echo -e "\033[1;36m>>> [2/3] Running benchmark configurations...\033[0m"
echo "----------------------------------------------------------------------------------------------------------------------------------"
printf "%-16s | %-10s | %-13s | %-8s | %-8s | %-9s | %-9s | %-9s | %-9s | %-8s\n" \
  "Benchmark" "Config" "Time(s)" "HitRate%" "MissRate%" "MSHRHit%" "Coverage%" "Accuracy%" "Pollut.%" "Prefetch"
echo "----------------------------------------------------------------------------------------------------------------------------------"

# Arguments: Name | Config | Binary & Flags
# run_test maps config to the real prefetch CLI flags, runs the binary, and
# uses analyze_metrics.py to compute Coverage/Accuracy/Hit-Miss rates from
# its .sqlite3 output.
run_test() {
    local bench_name="$1"
    local config="$2"
    shift 2
    local cmd=("$@")

    local prefetch_flags=()
    case "$config" in
        base) prefetch_flags=() ;;
        stride_t1) prefetch_flags=(-enable-prefetch -prefetch-algorithm=stride -prefetch-stride-degree=4 -prefetch-stride-threshold=1) ;;
        stride_t2) prefetch_flags=(-enable-prefetch -prefetch-algorithm=stride -prefetch-stride-degree=4 -prefetch-stride-threshold=2) ;;
        stride_t4) prefetch_flags=(-enable-prefetch -prefetch-algorithm=stride -prefetch-stride-degree=4 -prefetch-stride-threshold=4) ;;
        nextline) prefetch_flags=(-enable-prefetch -prefetch-algorithm=nextline -prefetch-nextline-degree=2) ;;
        *) echo "run_test: unknown config '$config'" >&2; exit 1 ;;
    esac

    local db_name="${bench_name}_${config}"
    "${cmd[@]}" "${prefetch_flags[@]}" -metric-file-name="$db_name" > /dev/null 2>&1

    local line
    line=$(python3 analyze_metrics.py --line "${db_name}.sqlite3")
    IFS=',' read -r hit_rate miss_rate mshr_hit_rate coverage accuracy pollution_rate prefetch_issued kernel_time <<< "$line"

    printf "%-16s | %-10s | %-13s | %-8.2f | %-8.2f | %-9.2f | %-9.2f | %-9.2f | %-9.2f | %-8.0f\n" \
      "$bench_name" "$config" "$kernel_time" "$hit_rate" "$miss_rate" "$mshr_hit_rate" "$coverage" "$accuracy" "$pollution_rate" "$prefetch_issued"

    echo "$bench_name,$config,$kernel_time,$hit_rate,$miss_rate,$mshr_hit_rate,$coverage,$accuracy,$pollution_rate,$prefetch_issued" >> "$MASTER_CSV"
}

# Runs one binary across a list of configs, so each benchmark states its
# binary/size args once.
run_benchmark() {
    local bench_name="$1"; shift
    local configs_str="$1"; shift
    local cmd=("$@")
    local configs
    read -r -a configs <<< "$configs_str"
    for config in "${configs[@]}"; do
        run_test "$bench_name" "$config" "${cmd[@]}"
    done
}

# 1. GEMM Tests
# Regular numeric kernel: Stride rarely predicts anything past threshold 1;
# NextLine handles this access pattern better.
run_benchmark "GEMM" "base stride_t1 stride_t2 nextline" ./matrixmultiplication_bin -timing -x 128 -y 128 -z 128 -report-all

# 2. Stencil2D Tests
# Same story as GEMM: NextLine clearly outperforms Stride here.
run_benchmark "Stencil2D" "base stride_t1 stride_t2 nextline" ./stencil2d_bin -timing -row 128 -col 128 -report-all

# 3. BFS Tests
# Irregular graph traversal: Stride fires a bit more than on GEMM/Stencil2D,
# though still rarely.
run_benchmark "BFS" "base stride_t1 stride_t2 nextline" ./bfs_bin -timing -node 10000 -report-all

# 4. NW (Needleman-Wunsch) Tests
# Clearest Stride threshold demo in the suite: accuracy rises sharply as the
# confidence threshold increases, at the cost of fewer prefetches issued.
run_benchmark "NW" "base stride_t1 stride_t2 stride_t4 nextline" ./nw_bin -timing -length 128 -report-all

# 5. MatrixTranspose Tests
# Baseline miss rate is close to 100%. NextLine roughly halves it; Stride
# does essentially nothing.
run_benchmark "MatrixTranspose" "base stride_t1 nextline" ./matrixtranspose_bin -timing -width 64 -report-all

# 6. SPMV Tests
# Same input, opposite outcomes: Stride wastes prefetches here while
# NextLine gets real, useful coverage.
run_benchmark "SPMV" "base stride_t1 nextline" ./spmv_bin -timing -dim 128 -report-all

echo "----------------------------------------------------------------------------------------------------------------------------------"
echo -e "\033[1;32m>>> [3/3] All tests completed! Summary saved to: $MASTER_CSV\033[0m"
