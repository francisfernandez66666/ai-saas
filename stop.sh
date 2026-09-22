#!/bin/bash
# AI-SCRM 停止脚本
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
PID_FILE="$PROJECT_DIR/.pid"

if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        kill "$PID"
        echo "AI-SCRM 已停止 (PID: $PID)"
    else
        echo "进程 $PID 已不存在"
    fi
    rm -f "$PID_FILE"
else
    # 没有pid文件，按端口杀
    # 修复(2026-09-23)：兜底端口曾硬编码 8080，而 .env SERVER_PORT=9090 —— 于是"未发现运行中的进程"
    # 与实际在跑的实例并存，旧进程带着旧二进制继续服务（改代码不生效的假象）。
    # 与 start.sh 的 L6 修复同口径：端口从 .env 读，缺省 8080。
    PORT=8080
    if [ -f "$PROJECT_DIR/.env" ]; then
        ENV_PORT=$(grep -E '^[[:space:]]*SERVER_PORT=' "$PROJECT_DIR/.env" | head -1 | cut -d= -f2 | tr -d ' "')
        [ -n "$ENV_PORT" ] && PORT="$ENV_PORT"
    fi
    if lsof -i :$PORT -t > /dev/null 2>&1; then
        lsof -i :$PORT -t | xargs kill
        echo "AI-SCRM 已停止 (端口 $PORT)"
    else
        echo "未发现运行中的AI-SCRM进程"
    fi
fi
