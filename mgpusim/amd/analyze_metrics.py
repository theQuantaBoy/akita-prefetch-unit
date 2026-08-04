#!/usr/bin/env python3
"""Computes corrected L2 prefetch metrics from mgpusim's mgpusim_metrics.sqlite3
output (produced by -report-all).

Raw read-miss/read-hit/read-mshr-hit tags don't distinguish demand traffic
from the prefetcher's own synthetic reads: an issued prefetch goes through
the same doRead() miss path a demand access does, so its own fetch is tagged
read-miss just like a real demand miss. Subtracting prefetch-issued out of
read-miss recovers the true demand-only miss count.

A demand access can also benefit from a prefetch in two different ways: the
prefetch had already completed (prefetch-hit), or the access caught it still
in flight in the MSHR (prefetch-mshr-hit). Both count as "useful" below.

MSHR Miss Rate has no single standard definition (the assignment PDF lists it
alongside MSHR Hit Rate without one). MSHRHitRate% here is deliberately kept
relative to *all* demand accesses, so that HitRate% + MissRate% + MSHRHitRate%
sum to 100% of demand traffic. MSHRMissRate% is instead defined relative only
to the population of accesses that had to interact with the MSHR at all
(i.e., excluding accesses that hit outright in the directory): of those,
how many needed a brand new MSHR entry (a "true" miss) vs. merged into one
already in flight (an MSHR hit). This is additive information, not the
complement of MSHRHitRate%, and needs no new simulator instrumentation --
demand_misses and read-mshr-hit are already tracked.

prefetch-pollution counts blocks evicted while still an unclaimed prefetch
(PDF: "number of blocks replaced due to prefetching"). prefetch-caused-miss
is a new tag: it fires when a later demand access misses on exactly one of
those evicted addresses, i.e. a miss that would not have happened had the
prefetch not displaced the block (PDF: "number of misses caused by that").
"""

import argparse
import csv
import sqlite3
import sys

TABLE = "mgpusim_metrics"

TAGS = [
    "read-hit",
    "read-miss",
    "read-mshr-hit",
    "prefetch-issued",
    "prefetch-hit",
    "prefetch-mshr-hit",
    "prefetch-pollution",
    "prefetch-caused-miss",
]


def read_l2_tag_sums(db_path):
    """Sums each tag in TAGS across every L2Cache[i] bank, plus kernel_time
    from the Driver row. Returns a dict keyed by tag name (0.0 if a tag never
    appears at all, e.g. the run didn't pass -report-all)."""
    conn = sqlite3.connect(db_path)
    try:
        cur = conn.cursor()
        sums = {}
        for tag in TAGS:
            cur.execute(
                f"SELECT COALESCE(SUM(Value), 0) FROM {TABLE} "
                f"WHERE Location LIKE '%L2Cache%' AND What = ?",
                (tag,),
            )
            sums[tag] = cur.fetchone()[0]

        cur.execute(
            f"SELECT Value FROM {TABLE} WHERE Location = 'Driver' "
            f"AND What = 'kernel_time' LIMIT 1"
        )
        row = cur.fetchone()
        sums["kernel_time"] = row[0] if row else None

        return sums
    finally:
        conn.close()


def compute_metrics(sums):
    read_hit = sums["read-hit"]
    read_miss = sums["read-miss"]
    read_mshr_hit = sums["read-mshr-hit"]
    prefetch_issued = sums["prefetch-issued"]
    prefetch_hit = sums["prefetch-hit"]
    prefetch_mshr_hit = sums["prefetch-mshr-hit"]
    prefetch_pollution = sums["prefetch-pollution"]
    prefetch_caused_miss = sums["prefetch-caused-miss"]

    # read-miss also counts the prefetcher's own synthetic fetches (they go
    # through the same doRead() miss path as a real demand access), so it is
    # NOT the true demand-miss count whenever prefetching is enabled.
    demand_misses = read_miss - prefetch_issued
    total_demand_accesses = read_hit + demand_misses + read_mshr_hit

    useful_prefetches = prefetch_hit + prefetch_mshr_hit
    unclaimed_prefetches = (
        prefetch_issued - prefetch_hit - prefetch_mshr_hit - prefetch_pollution
    )

    # Population of accesses that had to interact with the MSHR at all (i.e.
    # everything except an outright directory hit): a "true" miss allocates a
    # fresh entry, an MSHR hit merges into one already outstanding. See the
    # module docstring for why this differs from MSHRHitRate%'s denominator.
    mshr_traffic = demand_misses + read_mshr_hit

    def ratio(numerator, denominator):
        return (numerator / denominator * 100) if denominator > 0 else 0.0

    return {
        "demand_misses": demand_misses,
        "hit_rate": ratio(read_hit, total_demand_accesses),
        "miss_rate": ratio(demand_misses, total_demand_accesses),
        "mshr_hit_rate": ratio(read_mshr_hit, total_demand_accesses),
        "mshr_miss_rate": ratio(demand_misses, mshr_traffic),
        "coverage": ratio(useful_prefetches, demand_misses + useful_prefetches),
        "accuracy": ratio(useful_prefetches, prefetch_issued),
        "pollution_rate": ratio(prefetch_pollution, prefetch_issued),
        "prefetch_issued": prefetch_issued,
        "useful_prefetches": useful_prefetches,
        "unclaimed_prefetches": unclaimed_prefetches,
        "pollution_blocks": prefetch_pollution,
        "pollution_caused_misses": prefetch_caused_miss,
        "kernel_time": sums["kernel_time"],
    }


COLUMNS = [
    ("File", "{:<28}"),
    ("HitRate%", "{:>8.2f}"),
    ("MissRate%", "{:>9.2f}"),
    ("MSHRHitRate%", "{:>12.2f}"),
    ("MSHRMissRate%", "{:>13.2f}"),
    ("Coverage%", "{:>9.2f}"),
    ("Accuracy%", "{:>9.2f}"),
    ("PollutionRate%", "{:>14.2f}"),
    ("PollutedBlocks", "{:>14.0f}"),
    ("CausedMisses", "{:>12.0f}"),
    ("PrefetchIssued", "{:>14.0f}"),
    ("ExecTime(s)", "{:>13}"),
]


def format_row(name, m):
    exec_time = "n/a" if m["kernel_time"] is None else f'{m["kernel_time"]:.6e}'
    values = [
        name,
        m["hit_rate"],
        m["miss_rate"],
        m["mshr_hit_rate"],
        m["mshr_miss_rate"],
        m["coverage"],
        m["accuracy"],
        m["pollution_rate"],
        m["pollution_blocks"],
        m["pollution_caused_misses"],
        m["prefetch_issued"],
        exec_time,
    ]
    return [fmt.format(v) for (_, fmt), v in zip(COLUMNS, values)]


def main():
    parser = argparse.ArgumentParser(
        description="Compute corrected L2 prefetch metrics from one or more "
        "mgpusim_metrics.sqlite3 files."
    )
    parser.add_argument("db_files", nargs="+", help="Path(s) to .sqlite3 files")
    parser.add_argument(
        "--csv", metavar="PATH", help="Also write the results as CSV to PATH"
    )
    parser.add_argument(
        "--line",
        action="store_true",
        help="For a single input file, print one comma-separated line of "
        "raw numbers (no header, no '%%' signs), in the fixed order "
        "hit_rate,miss_rate,mshr_hit_rate,mshr_miss_rate,coverage,accuracy,"
        "pollution_rate,pollution_blocks,pollution_caused_misses,"
        "prefetch_issued,kernel_time. For scripting.",
    )
    args = parser.parse_args()

    if args.line:
        if len(args.db_files) != 1:
            print("--line requires exactly one db file", file=sys.stderr)
            sys.exit(1)
        try:
            sums = read_l2_tag_sums(args.db_files[0])
        except sqlite3.OperationalError as e:
            print(f"error reading {args.db_files[0]}: {e}", file=sys.stderr)
            sys.exit(1)
        m = compute_metrics(sums)
        kernel_time = "NA" if m["kernel_time"] is None else f'{m["kernel_time"]:.6e}'
        print(
            f'{m["hit_rate"]:.4f},{m["miss_rate"]:.4f},{m["mshr_hit_rate"]:.4f},'
            f'{m["mshr_miss_rate"]:.4f},'
            f'{m["coverage"]:.4f},{m["accuracy"]:.4f},{m["pollution_rate"]:.4f},'
            f'{m["pollution_blocks"]:.0f},{m["pollution_caused_misses"]:.0f},'
            f'{m["prefetch_issued"]:.0f},{kernel_time}'
        )
        return

    rows = []
    for db_path in args.db_files:
        try:
            sums = read_l2_tag_sums(db_path)
        except sqlite3.OperationalError as e:
            print(f"warning: skipping {db_path}: {e}", file=sys.stderr)
            continue

        m = compute_metrics(sums)
        rows.append((db_path, m))

    if not rows:
        print("no readable metrics found in any input file", file=sys.stderr)
        sys.exit(1)

    header = [name for name, _ in COLUMNS]
    widths = [len(h) for h in header]
    formatted_rows = []
    for name, m in rows:
        display_name = name.rsplit("/", 1)[-1].removesuffix(".sqlite3")
        formatted = format_row(display_name, m)
        formatted_rows.append(formatted)
        widths = [max(w, len(c)) for w, c in zip(widths, formatted)]

    def print_row(cells):
        print("  ".join(c.rjust(w) for c, w in zip(cells, widths)))

    print_row(header)
    print_row(["-" * w for w in widths])
    for formatted in formatted_rows:
        print_row(formatted)

    if args.csv:
        with open(args.csv, "w", newline="") as f:
            writer = csv.writer(f)
            writer.writerow(header)
            writer.writerows(formatted_rows)
        print(f"\nCSV written to {args.csv}")


if __name__ == "__main__":
    main()
