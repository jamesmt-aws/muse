#!/bin/bash
# Replicate all experiments from the paper 5 times each to measure variance.
#
# Usage: ./scripts/replicate.sh <output-dir> <conversations-dir>
#
# Example:
#   ./scripts/replicate.sh ./results ~/.muse/conversations/claude-code
#
# Conversations needed (by filename prefix):
#   4b49a340 — RFC design session (117 user turns)
#   bcfe7072 — Muse dev branch (183 user turns)
#   bd3497e5 — Context rot research (134 user turns)
#   2648ec88 — Consolidation threshold (115 user turns)
#
# Also needs: politics-and-the-english-language.md in the repo root or
# pass as ESSAY_PATH environment variable.
#
# Outputs: one directory per replica (run-1/ through run-5/) containing
# raw experiment output and extracted counts. A summary CSV at the end.

set -euo pipefail

REPLICAS=${REPLICAS:-5}
OUTPUT_DIR="${1:?Usage: replicate.sh <output-dir> <conversations-dir>}"
CONV_DIR="${2:?Usage: replicate.sh <output-dir> <conversations-dir>}"
ESSAY_PATH="${ESSAY_PATH:-./politics-and-the-english-language.md}"

# Find conversations
RFC=$(find "$CONV_DIR" -name "4b49a340*" -not -name "agent-*" | head -1)
MUSE_DEV=$(find "$CONV_DIR" -name "bcfe7072*" -not -name "agent-*" | head -1)
CONTEXT_ROT=$(find "$CONV_DIR" -name "bd3497e5*" -not -name "agent-*" | head -1)
CONSOLIDATION=$(find "$CONV_DIR" -name "2648ec88*" -not -name "agent-*" | head -1)

for conv in RFC MUSE_DEV CONTEXT_ROT CONSOLIDATION; do
    path="${!conv}"
    if [ -z "$path" ] || [ ! -f "$path" ]; then
        echo "ERROR: Could not find conversation for $conv in $CONV_DIR"
        exit 1
    fi
    echo "Found $conv: $path"
done

if [ ! -f "$ESSAY_PATH" ]; then
    echo "ERROR: Essay not found at $ESSAY_PATH (set ESSAY_PATH to override)"
    exit 1
fi
echo "Found essay: $ESSAY_PATH"

# Build
echo ""
echo "Building experiment tools..."
go build -o "$OUTPUT_DIR/bin/experiment" ./cmd/experiment/
go build -o "$OUTPUT_DIR/bin/judge" ./cmd/judge/
echo "Built successfully."

mkdir -p "$OUTPUT_DIR"

# CSV header
CSV="$OUTPUT_DIR/summary.csv"
echo "run,experiment,strategy,observations" > "$CSV"

extract_counts() {
    local file="$1"
    local run="$2"
    local experiment="$3"

    # Parse strategy names and observation counts from experiment output
    local current_strategy=""
    while IFS= read -r line; do
        if [[ "$line" =~ "--- Strategy "([0-9]+)": "(.+)" ---" ]]; then
            current_strategy="${BASH_REMATCH[2]}"
        elif [[ "$line" =~ "  Observations: "([0-9]+) ]]; then
            echo "$run,$experiment,$current_strategy,${BASH_REMATCH[1]}" >> "$CSV"
        fi
    done < "$file"
}

for run in $(seq 1 "$REPLICAS"); do
    RUN_DIR="$OUTPUT_DIR/run-$run"
    mkdir -p "$RUN_DIR"
    echo ""
    echo "========================================="
    echo "  REPLICA $run of $REPLICAS"
    echo "========================================="

    # Table 1: Conversations (strategies 1,2,5 = baseline, windowed, windowed owner-only)
    echo ""
    echo "--- Conversations ---"
    for conv_name in RFC MUSE_DEV CONTEXT_ROT CONSOLIDATION; do
        conv_path="${!conv_name}"
        echo "  $conv_name..."
        "$OUTPUT_DIR/bin/experiment" "$conv_path" \
            > "$RUN_DIR/conv-${conv_name}.txt" 2>&1 || true
        extract_counts "$RUN_DIR/conv-${conv_name}.txt" "$run" "conv-${conv_name}"
    done

    # Table 2: Essay (strategies 1,2,3 = baseline, triage, windowed)
    echo "  ESSAY..."
    "$OUTPUT_DIR/bin/experiment" --essay "$ESSAY_PATH" \
        > "$RUN_DIR/essay.txt" 2>&1 || true
    extract_counts "$RUN_DIR/essay.txt" "$run" "essay"

    # Table 3: Context-size control (already included in conversation run as strategies 6,7)
    # Counts extracted from the RFC conversation run above

    echo "  Replica $run complete."
done

echo ""
echo "========================================="
echo "  SUMMARY"
echo "========================================="
echo ""
echo "Results in: $OUTPUT_DIR"
echo "CSV: $CSV"
echo ""

# Print summary statistics
python3 - "$CSV" << 'PYTHON'
import csv, sys
from collections import defaultdict

data = defaultdict(list)
with open(sys.argv[1]) as f:
    reader = csv.DictReader(f)
    for row in reader:
        key = (row['experiment'], row['strategy'])
        data[key].append(int(row['observations']))

print(f"{'Experiment':<25} {'Strategy':<45} {'Mean':>6} {'Min':>5} {'Max':>5} {'Runs':>5}")
print("-" * 95)

for (exp, strat), counts in sorted(data.items()):
    if len(counts) == 0:
        continue
    mean = sum(counts) / len(counts)
    print(f"{exp:<25} {strat:<45} {mean:>6.1f} {min(counts):>5} {max(counts):>5} {len(counts):>5}")
PYTHON
