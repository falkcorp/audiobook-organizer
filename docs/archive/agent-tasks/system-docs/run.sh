#!/usr/bin/env bash
# file: docs/agent-tasks/system-docs/run.sh
# version: 1.0.0
# guid: 2c930415-6172-4293-984b-697081
# last-edited: 2026-06-28
#
# Thin wrapper over ../run-sweep.sh for the system-docs workstream.
#   ./run.sh 01                  # index first
#   ./run.sh 02 03 04 05 06 07   # area docs in parallel
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WS="$(basename "$HERE")"

cat <<'EOF'
system-docs waves (DOCS-1 target: >=9 files, >=7 Mermaid diagrams):
  Wave 1: 01 index (merge first)
  Wave 2 (parallel): 02 architecture, 03 pipelines, 04 storage, 05 api,
                     06 runbooks, 07 components+incidents
EOF

if [[ $# -gt 0 ]]; then
  exec "$HERE/../run-sweep.sh" "$WS" "$@"
fi
echo; echo "Setting up Wave 1 (01 index)…"; echo
exec "$HERE/../run-sweep.sh" "$WS" 01
