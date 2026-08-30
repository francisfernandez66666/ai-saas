#!/bin/bash
# AI-SCRM 启动脚本（实时日志版）
# 用法: ./start.sh          → 编译+前台启动（实时看日志）+ 自动开浏览器
#       ./start.sh build    → 仅编译
#       ./start.sh run      → 跳过编译直接前台启动
#       ./start.sh bg       → 编译+后台启动（日志写入文件，同旧版）
#       ./start.sh noopen   → 编译+前台启动，不自动开浏览器

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$PROJECT_DIR/ai-scrm"
# L6修复(2026-08-27)：启动端口读 .env 的 SERVER_PORT，缺省 8080（避免硬编码与 .env 不一致）
PORT=8080
if [ -f "$PROJECT_DIR/.env" ]; then
  ENV_PORT=$(grep -E '^[[:space:]]*SERVER_PORT=' "$PROJECT_DIR/.env" | head -1 | cut -d= -f2 | tr -d ' "')
  [ -n "$ENV_PORT" ] && PORT="$ENV_PORT"
fi
LOG_FILE="$PROJECT_DIR/ai-scrm.log"
PID_FILE="$PROJECT_DIR/.pid"

# 颜色
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
GRAY='\033[0;37m'
NC='\033[0m'

MODE="fg"
NO_OPEN=false
if [ "$1" = "bg" ]; then
    MODE="bg"
elif [ "$1" = "noopen" ]; then
    NO_OPEN=true
elif [ "$1" = "gateway" ]; then
    MODE="gateway"
fi

# 网关监听端口（读 .env LLM_GATEWAY_LISTEN，缺省 9091）
GATEWAY_PORT=9091
if [ -f "$PROJECT_DIR/.env" ]; then
  ENV_GW=$(grep -E '^[[:space:]]*LLM_GATEWAY_LISTEN=' "$PROJECT_DIR/.env" | head -1 | cut -d= -f2 | tr -d ' ":')
  [ -n "$ENV_GW" ] && GATEWAY_PORT="${ENV_GW#:}"
fi

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}  AI-SCRM 启动${NC}"
echo -e "${GREEN}========================================${NC}"

# 检查项目目录
if [ ! -f "$PROJECT_DIR/go.mod" ]; then
    echo -e "${RED}错误: 未找到go.mod，请确认在ai-scrm项目根目录运行${NC}"
    exit 1
fi

# ===== AI 网关独立部署模式（P0-1）=====
if [ "$MODE" = "gateway" ]; then
    echo -e "${YELLOW}[1/1] 编译 AI 网关...${NC}"
    cd "$PROJECT_DIR"
    go build -o "$PROJECT_DIR/ai-gateway" ./cmd/gateway
    if [ $? -ne 0 ]; then
        echo -e "${RED}网关编译失败${NC}"
        exit 1
    fi
    echo -e "${GREEN}网关编译成功 ✓${NC}"
    echo ""
    echo -e "  ${CYAN}健康检查:${NC} http://localhost:$GATEWAY_PORT/health"
    echo -e "  ${CYAN}对话端点:${NC} http://localhost:$GATEWAY_PORT/v1/chat/completions"
    echo -e "  ${CYAN}向量端点:${NC} http://localhost:$GATEWAY_PORT/v1/embeddings"
    echo ""
    echo -e "${GRAY}Ctrl+C 停止网关 | 业务实例须配 LLM_GATEWAY_URL=http://localhost:$GATEWAY_PORT${NC}"
    echo -e "${GREEN}------------------------------------------${NC}"
    "$PROJECT_DIR/ai-gateway"
    exit 0
fi

# 停掉旧进程
if [ -f "$PID_FILE" ]; then
    OLD_PID=$(cat "$PID_FILE" 2>/dev/null)
    if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
        echo -e "${YELLOW}停止旧进程 (PID: $OLD_PID)...${NC}"
        kill "$OLD_PID" 2>/dev/null
        sleep 1
        kill -9 "$OLD_PID" 2>/dev/null
    fi
    rm -f "$PID_FILE"
fi

# 也用端口兜底检查
if lsof -i :$PORT -t > /dev/null 2>&1; then
    echo -e "${YELLOW}端口 $PORT 被占用，正在停止...${NC}"
    lsof -i :$PORT -t | xargs kill -9 2>/dev/null
    sleep 1
fi

# 编译
if [ "$1" != "run" ]; then
    echo -e "${YELLOW}[1/2] 编译中...${NC}"
    cd "$PROJECT_DIR"
    go build -o "$BINARY" ./cmd/server
    if [ $? -ne 0 ]; then
        echo -e "${RED}编译失败${NC}"
        exit 1
    fi
    echo -e "${GREEN}编译成功 ✓${NC}"
else
    if [ ! -f "$BINARY" ]; then
        echo -e "${RED}未找到编译产物，请先运行 ./start.sh${NC}"
        exit 1
    fi
    echo -e "${YELLOW}跳过编译${NC}"
fi

# 打印入口
echo -e "${YELLOW}[2/2] 启动服务...${NC}"
echo ""
echo -e "  ${CYAN}客户端:${NC}   http://localhost:$PORT/client"
echo -e "  ${CYAN}销售端:${NC}   http://localhost:$PORT/advisor"
echo -e "  ${CYAN}后台管理:${NC} http://localhost:$PORT/admin"
echo -e "  ${CYAN}API测试:${NC}  http://localhost:$PORT/api/v1/chat/test"
echo ""
echo -e "${GRAY}Ctrl+C 停止服务 | 日志同步写入: $LOG_FILE${NC}"
echo -e "${GREEN}------------------------------------------${NC}"

cd "$PROJECT_DIR"

# 后台启动服务函数
start_server_bg() {
    "$BINARY" 2>&1 | tee "$LOG_FILE" &
    SERVER_PID=$!
    echo "$SERVER_PID" > "$PID_FILE"
}

# 等服务就绪
wait_for_ready() {
    for i in $(seq 1 20); do
        if curl -s http://localhost:$PORT/api/v1/admin/config > /dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# 自动开浏览器
open_browser() {
    if [ "$NO_OPEN" = true ]; then
        return
    fi
    # macOS
    if command -v open > /dev/null 2>&1; then
        open "http://localhost:$PORT/client" 2>/dev/null
        sleep 0.3
        open "http://localhost:$PORT/advisor" 2>/dev/null
        sleep 0.3
        open "http://localhost:$PORT/admin" 2>/dev/null
    # Linux
    elif command -v xdg-open > /dev/null 2>&1; then
        xdg-open "http://localhost:$PORT/client" 2>/dev/null
        sleep 0.3
        xdg-open "http://localhost:$PORT/advisor" 2>/dev/null
        sleep 0.3
        xdg-open "http://localhost:$PORT/admin" 2>/dev/null
    fi
}

if [ "$MODE" = "bg" ]; then
    # ===== 后台模式 =====
    start_server_bg

    if wait_for_ready; then
        echo -e "${GREEN}后台启动成功 (PID: $SERVER_PID) ✓${NC}"
        open_browser
    else
        echo -e "${RED}启动超时，查看日志: tail -f $LOG_FILE${NC}"
        exit 1
    fi

    echo -e "  停止: ${YELLOW}kill $SERVER_PID${NC} 或 ${YELLOW}./stop.sh${NC}"
    echo -e "  日志: ${YELLOW}tail -f $LOG_FILE${NC}"
else
    # ===== 前台模式（实时日志）=====
    # 先后台启动，等服务就绪后再切前台
    start_server_bg

    if wait_for_ready; then
        echo -e "\n${GREEN}服务启动成功 ✓${NC}"
        open_browser
    else
        echo -e "\n${RED}启动超时，查看日志: tail -f $LOG_FILE${NC}"
        exit 1
    fi

    echo ""
    echo -e "${GRAY}--- 以下为实时日志，Ctrl+C 停止 ---${NC}"
    echo -e "${GREEN}------------------------------------------${NC}"

    # 把PID写到前台进程（方便Ctrl+C时清理）
    echo "$SERVER_PID" > "$PID_FILE"

    # 优雅退出
    cleanup() {
        echo ""
        echo -e "${YELLOW}正在停止服务 (PID: $SERVER_PID)...${NC}"
        kill $SERVER_PID 2>/dev/null
        wait $SERVER_PID 2>/dev/null
        rm -f "$PID_FILE"
        echo -e "${GREEN}已停止${NC}"
        exit 0
    }
    trap cleanup SIGINT SIGTERM

    # 等待后台进程（实时日志已经在tee中输出）
    wait $SERVER_PID
fi
