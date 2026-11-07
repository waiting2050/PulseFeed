#!/usr/bin/env bash
# start.sh 在本机以三进程方式启动 API、Worker 与前端开发服务器，并在退出时清理子进程。
# 适用于 Git Bash / WSL / macOS / Linux；Windows PowerShell 请直接分别运行三条命令。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PIDS=()

cleanup() {
  if ((${#PIDS[@]} > 0)); then
    echo "[start.sh] 正在停止子进程: ${PIDS[*]}"
    kill "${PIDS[@]}" 2>/dev/null || true
    wait "${PIDS[@]}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

echo "[start.sh] API 启动（:8090）"
(cd "$ROOT_DIR/apps/api" && go run ./cmd/api) &
PIDS+=($!)

# 本机 9092 可能落在 Hyper-V 保留段，默认用 19092 覆盖指标端口。
echo "[start.sh] Worker 启动（metrics :19092）"
(cd "$ROOT_DIR/apps/api" && go run ./cmd/worker -metrics-addr :19092) &
PIDS+=($!)

echo "[start.sh] Web 启动（:5173）"
(cd "$ROOT_DIR/apps/web" && npm run dev) &
PIDS+=($!)

echo "[start.sh] 已启动，Ctrl+C 结束全部进程"
wait
