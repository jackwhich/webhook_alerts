#!/bin/bash

# 本地编译 alert-router（当前系统架构）
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"
BINARY="${PROJECT_ROOT}/alert-router"
echo "[INFO] 编译 alert-router (本地)..."
go build -o "$BINARY" ./cmd/alert-router
echo "[INFO] 完成: $BINARY"
