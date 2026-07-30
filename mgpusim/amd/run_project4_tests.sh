#!/usr/bin/env bash
set -e

echo -e "\033[1;36m>>> [1/3] Building updated Linux binaries...\033[0m"
go build -o ./matrixmultiplication_bin ./samples/matrixmultiplication
go build -o ./stencil2d_bin ./samples/stencil2d
go build -o ./bfs_bin ./samples/bfs

MASTER_CSV="Project4_Final_Summary.csv"
echo "Benchmark,Time_s,Prefetch_Reqs,Useful_Hits,Demand_Misses,Pollution,Coverage_Pct,Accuracy_Pct" > "$MASTER_CSV"

echo -e "\033[1;36m>>> [2/3] Running 9 benchmark configurations...\033[0m"
echo "--------------------------------------------------------------------------------------------------"
printf "%-12s | %-8s | %-13s | %-11s | %-13s | %-9s | %-10s | %-10s\n" \
  "Benchmark" "Time(s)" "Prefetch Reqs" "Useful Hits" "Demand Misses" "Pollution" "Coverage%" "Accuracy%"
echo "--------------------------------------------------------------------------------------------------"

run_test() {
    local name="$1"
    local csv_name="$2"
    local enable="$3"
    local degree="$4"
    shift 4
    local cmd=("$@")

    local output
    # ست کردن متغیر محیطی قبل از اجرای باینری
    output=$(P4_PREFETCH_ENABLE="$enable" P4_PREFETCH_DEGREE="$degree" "${cmd[@]}" -metric-file-name="$csv_name" 2>&1)

    local reqs hits misses pollution
    reqs=$(echo "$output" | awk -F':' '/Prefetch Requests Issued/ {sum += $2} END {print sum+0}')
    hits=$(echo "$output" | awk -F':' '/Useful Prefetch Hits/     {sum += $2} END {print sum+0}')
    misses=$(echo "$output" | awk -F':' '/Demand Cache Misses/      {sum += $2} END {print sum+0}')
    pollution=$(echo "$output" | awk -F':' '/Pollution Misses/         {sum += $2} END {print sum+0}')

    local coverage accuracy
    coverage=$(awk -v h="$hits" -v m="$misses" 'BEGIN {
        if ((m + h) > 0) printf "%.2f", (h / (m + h)) * 100
        else print "0.00"
    }')
    
    accuracy=$(awk -v h="$hits" -v r="$reqs" 'BEGIN {
        if (r > 0) printf "%.2f", (h / r) * 100
        else print "0.00"
    }')

    local time_s="N/A"
    local db_file="${csv_name}.sqlite3" 
    if [ -f "$db_file" ]; then
        local raw_time
        raw_time=$(sqlite3 "$db_file" "SELECT Value FROM mgpusim_metrics WHERE What LIKE '%kernel_time%' LIMIT 1;" 2>/dev/null)
        if [ -n "$raw_time" ]; then
            time_s=$(awk -v t="$raw_time" 'BEGIN {printf "%.5f", t}')
        fi
    fi

    printf "%-12s | %-8s | %-13s | %-11s | %-13s | %-9s | %-10s | %-10s\n" \
      "$name" "$time_s" "$reqs" "$hits" "$misses" "$pollution" "$coverage" "$accuracy"

    echo "$name,$time_s,$reqs,$hits,$misses,$pollution,$coverage,$accuracy" >> "$MASTER_CSV"
}

# آرگومان‌های جدید: Name | CSV_Name | Enable (0/1) | Degree (1/4) | Binary & Flags
# 1. GEMM Tests
run_test "GEMM_Base" "gemm_base" "0" "0" ./matrixmultiplication_bin -timing -x 512 -y 512 -z 512 -report-all -report-cache-hit-rate
run_test "GEMM_Deg1" "gemm_deg1" "1" "1" ./matrixmultiplication_bin -timing -x 512 -y 512 -z 512 -report-all -report-cache-hit-rate
run_test "GEMM_Deg4" "gemm_deg4" "1" "4" ./matrixmultiplication_bin -timing -x 512 -y 512 -z 512 -report-all -report-cache-hit-rate

# 2. Stencil2D Tests
run_test "STENCIL_Base" "stencil_base" "0" "0" ./stencil2d_bin -timing -row 512 -col 512 -report-all -report-cache-hit-rate
run_test "STENCIL_Deg1" "stencil_deg1" "1" "1" ./stencil2d_bin -timing -row 512 -col 512 -report-all -report-cache-hit-rate
run_test "STENCIL_Deg4" "stencil_deg4" "1" "4" ./stencil2d_bin -timing -row 512 -col 512 -report-all -report-cache-hit-rate

# 3. BFS Tests
run_test "BFS_Base"  "bfs_base"  "0" "0" ./bfs_bin -timing -node 20000 -report-all -report-cache-hit-rate
run_test "BFS_Deg1"  "bfs_deg1"  "1" "1" ./bfs_bin -timing -node 20000 -report-all -report-cache-hit-rate
run_test "BFS_Deg4"  "bfs_deg4"  "1" "4" ./bfs_bin -timing -node 20000 -report-all -report-cache-hit-rate
echo "--------------------------------------------------------------------------------------------------"
echo -e "\033[1;32m>>> [3/3] All tests completed! Summary saved to: $MASTER_CSV\033[0m"