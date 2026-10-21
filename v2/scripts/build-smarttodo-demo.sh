#!/usr/bin/env bash
set -euo pipefail

OUTPUT_DIR="${1:-dist/smarttodo-demo}"
mkdir -p "$OUTPUT_DIR"
cp -R examples/smarttodo/web/. "$OUTPUT_DIR/"
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$OUTPUT_DIR/"
(
  cd examples/smarttodo
  GOOS=js GOARCH=wasm go build -o "../../$OUTPUT_DIR/smarttodo.wasm" ./cmd/smarttodo-wasm
)
echo "Built Smart Todo demo into $OUTPUT_DIR"
