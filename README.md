# akita-prefetch-unit

A configurable L2 cache prefetch unit for the [Akita](https://github.com/sarchlab/akita) / [MGPUSim](https://github.com/sarchlab/mgpusim) GPU simulator, implementing Stride and Next-Line prefetching algorithms.

> **Computer Architecture — Project 4**
> Sharif University of Technology, Spring 2025
> Group 9: Mohsen Salah, Sorouna Kia, Amirhossein Mahboudi

---

## Overview

This project adds a hardware-modeled prefetch unit to the L2 (`writeback`) cache of the Akita/MGPUSim simulator (AMD GCN3, r9nano platform). The unit intercepts L2 read misses, predicts future memory accesses, and issues speculative fetch transactions that flow through the existing cache pipeline — with no additional parallel pipeline required.

Two algorithms are implemented and evaluated on six GPU benchmarks (GEMM, Stencil2D, BFS, NW, MatrixTranspose, SPMV).

---

## Algorithms

**Stride** — Tracks fixed-distance patterns between consecutive accesses per memory stream. Uses a 3-state confidence FSM (Initial → Transient → Steady) to avoid issuing prefetches until a pattern is confirmed. Configurable via confidence threshold, lookahead degree, and stream table size.

**Next-Line** — Unconditionally prefetches the next *N* sequential cache blocks after every access. Stateless and simple; performs well on workloads with spatial locality.

Both algorithms suppress candidates that cross virtual page boundaries and respect MSHR/buffer capacity limits.

---

## Repository Structure

The repository ships only the files added or modified relative to the upstream simulators. All changes are also available under `diff/`.

```
akita/mem/cache/prefetch/       # New standalone package (prefetcher interface, Stride, Next-Line)
│   prefetcher.go               # Prefetcher interface + StreamID type
│   stride.go                   # StridePrefetcher (LRU stream table + confidence FSM)
│   nextline.go                 # NextLinePrefetcher (stateless)

akita/mem/cache/writeback/      # Modified: L2 cache integration
│   comp.go                     # Spec + State extended with prefetch fields; IsPrefetch flag
│   directorystage.go           # triggerPrefetch() called on Full Miss; pollution tracking fix
│   bankstage.go                # Silent retire of IsPrefetch transactions (Directory-hit path)
│   mshrstage.go                # Silent retire of IsPrefetch transactions (DRAM-return path)
│   prefetch.go                 # streamIDFor(), newPrefetcher() factory

timingconfig/builder.go         # WithPrefetch() method added
r9nano/builder.go               # buildL2Caches() wired to prefetch spec fields

mgpusim/amd/
│   run_project4_tests.sh       # Benchmark suite runner
│   analyze_metrics.py          # Metrics extraction from SQLite output
```

---

## Running the Benchmark Suite

From the repository root:

```bash
cd mgpusim/amd
chmod +x run_project4_tests.sh   # first time only
./run_project4_tests.sh
```

Output is printed as a live table and saved to `Project4_Final_Summary.csv`.

---

## CLI Flags

The prefetch unit is controlled entirely via command-line flags:

| Flag | Type | Default | Description |
|---|---|---|---|
| `-enable-prefetch` | bool | false | Enable the prefetch unit |
| `-prefetch-algorithm` | string | — | `stride` or `nextline` |
| `-prefetch-stride-degree` | int | 4 | Lookahead blocks (Stride) |
| `-prefetch-stride-threshold` | int | 2 | Confidence threshold before issuing (Stride) |
| `-prefetch-stride-max-streams` | int | 64 | LRU stream table capacity (Stride) |
| `-prefetch-next-line-degree` | int | 2 | Blocks to prefetch per access (Next-Line) |

Example:

```bash
./fir -timing -enable-prefetch -prefetch-algorithm=stride -prefetch-stride-threshold=2
```

---

## References

- Akita simulator: https://github.com/sarchlab/akita
- MGPUSim simulator: https://github.com/sarchlab/mgpusim