#!/usr/bin/env bash
set -euo pipefail

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

REPO="${1:?usage: ./run_demo.sh <REPO_PATH> [FILE]}"
FILE="${2:-packages/shared/src/makeMap.ts}"

python3 "$DEMO_DIR/blast_client.py" \
  --repo "$REPO" \
  --file "$FILE"
