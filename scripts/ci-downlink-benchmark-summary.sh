#!/usr/bin/env bash
set -euo pipefail

CONNS="${SUDOKU_DOWNLINK_CONCURRENT_CONNS:-200}"
BYTES_PER_CONN="${SUDOKU_DOWNLINK_CONCURRENT_BYTES:-1048576}"
OUT="${RUNNER_TEMP:-/tmp}/sudoku-downlink-benchmark.txt"

SUDOKU_DOWNLINK_CONCURRENT_CONNS="$CONNS" \
SUDOKU_DOWNLINK_CONCURRENT_BYTES="$BYTES_PER_CONN" \
go test -run '^$' \
  -bench 'BenchmarkDownlinkThroughputConcurrentMatrix/(pure|packed)/httpmask_(off|stream|ws)/mux_(off|auto|on)$' \
  -benchtime=1x \
  -benchmem \
  ./tests | tee "$OUT"

python3 - "$OUT" "$CONNS" "$BYTES_PER_CONN" <<'PY'
import os
import re
import sys

path, conns, bytes_per_conn = sys.argv[1], sys.argv[2], sys.argv[3]
prefix = "BenchmarkDownlinkThroughputConcurrentMatrix/"
rows = []

with open(path, "r", encoding="utf-8") as f:
    for line in f:
        if not line.startswith(prefix):
            continue
        fields = line.split()
        name = fields[0][len(prefix):]
        name = re.sub(r"-\d+$", "", name)

        def metric(label):
            idx = fields.index(label)
            return fields[idx - 1]

        mb_s = float(metric("MB/s"))
        b_op = int(metric("B/op"))
        allocs_op = int(metric("allocs/op"))
        rows.append((name, mb_s * 8, b_op, allocs_op))

summary = [
    "## Downlink Throughput Benchmark",
    "",
    f"Concurrent connections: `{conns}`; bytes per connection: `{bytes_per_conn}`.",
    "",
    "| Config | Total Mbps | B/op | allocs/op |",
    "|---|---:|---:|---:|",
]
for name, mbps, b_op, allocs_op in rows:
    summary.append(f"| `{name}` | {mbps:.2f} | {b_op} | {allocs_op} |")

summary.append("")
summary.append("_Single CI run; use local 3-run median sampling for resource comparisons._")
summary_text = "\n".join(summary) + "\n"

step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
if step_summary:
    with open(step_summary, "a", encoding="utf-8") as f:
        f.write(summary_text)
else:
    print(summary_text)
PY
