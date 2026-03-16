#!/bin/bash

# 交叉编译 Linux amd64 二进制（用于部署到 Linux 服务器）
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"
OUTPUT="${PROJECT_ROOT}/webhook_alerts-linux-amd64"
echo "[INFO] 交叉编译 Linux amd64: $OUTPUT"
GOOS=linux GOARCH=amd64 go build -o "$OUTPUT" ./cmd/alert-router
echo "[INFO] 完成: $OUTPUT"
