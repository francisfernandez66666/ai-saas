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
    if lsof -i :8080 -t > /dev/null 2>&1; then
        lsof -i :8080 -t | xargs kill
        echo "AI-SCRM 已停止"
    else
        echo "未发现运行中的AI-SCRM进程"
    fi
fi
