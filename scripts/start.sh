#!/bin/bash

# 告警路由服务管理脚本（Go 版）
# 支持启动、停止、重启、状态查看、优雅关闭

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_NAME="alert-router"
BINARY="${PROJECT_ROOT}/alert-router"
PID_FILE="${SCRIPT_DIR}/${PROJECT_NAME}.pid"
CONFIG_FILE="${CONFIG_FILE:-${PROJECT_ROOT}/config.yaml}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

get_pid() {
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            echo "$PID"
            return 0
        fi
        rm -f "$PID_FILE"
    fi
    return 1
}

check_binary() {
    if [ ! -f "$BINARY" ]; then
        log_error "未找到可执行文件: $BINARY"
        log_info "请先执行 scripts/build.sh 或 scripts/build-linux.sh 编译"
        exit 1
    fi
}

check_config() {
    if [ ! -f "$CONFIG_FILE" ]; then
        log_error "未找到配置文件: $CONFIG_FILE"
        exit 1
    fi
}

start_service() {
    check_binary
    check_config
    if get_pid > /dev/null; then
        log_warn "服务已在运行中 (PID: $(get_pid))"
        return 1
    fi
    log_info "正在启动 ${PROJECT_NAME} 服务..."
    log_info "配置文件: ${CONFIG_FILE}"
    mkdir -p "${PROJECT_ROOT}/logs"
    cd "$PROJECT_ROOT"
    export CONFIG_FILE
    nohup "$BINARY" >> "${PROJECT_ROOT}/logs/alert-router.log" 2>&1 &
    echo $! > "$PID_FILE"
    sleep 2
    if get_pid > /dev/null; then
        log_info "服务启动成功 (PID: $(get_pid))"
        log_info "查看日志: tail -f ${PROJECT_ROOT}/logs/alert-router.log"
        return 0
    else
        log_error "服务启动失败，请查看日志"
        rm -f "$PID_FILE"
        return 1
    fi
}

stop_service() {
    if ! PID=$(get_pid); then
        log_warn "服务未运行"
        return 1
    fi
    log_info "正在停止服务 (PID: $PID)，发送 SIGTERM 优雅关闭..."
    kill -TERM "$PID" 2>/dev/null || true
    for i in $(seq 1 35); do
        if ! ps -p "$PID" > /dev/null 2>&1; then
            log_info "服务已优雅关闭"
            rm -f "$PID_FILE"
            return 0
        fi
        sleep 1
    done
    log_warn "优雅关闭超时，强制终止..."
    kill -KILL "$PID" 2>/dev/null || true
    sleep 1
    rm -f "$PID_FILE"
    log_info "服务已强制关闭"
    return 0
}

restart_service() {
    log_info "正在重启服务..."
    stop_service || true
    sleep 2
    start_service
}

status_service() {
    if PID=$(get_pid); then
        log_info "服务运行中 (PID: $PID)"
        ps -p "$PID" -o pid,ppid,user,%cpu,%mem,etime,cmd 2>/dev/null | head -2
        CONFIG_PORT=$(grep -A2 '^server:' "$CONFIG_FILE" 2>/dev/null | grep 'port:' | awk '{print $2}' || echo "9600")
        if command -v ss > /dev/null; then
            echo ""; echo "端口监听:"; ss -tlnp 2>/dev/null | grep ":$CONFIG_PORT " || true
        elif command -v netstat > /dev/null; then
            echo ""; echo "端口监听:"; netstat -tlnp 2>/dev/null | grep ":$CONFIG_PORT " || true
        fi
        return 0
    else
        log_warn "服务未运行"
        return 1
    fi
}

view_logs() {
    LOG="${PROJECT_ROOT}/logs/alert-router.log"
    if [ -f "$LOG" ]; then
        tail -f "$LOG"
    else
        log_error "日志文件不存在: $LOG"
        return 1
    fi
}

reload_service() {
    if ! get_pid > /dev/null; then
        log_error "服务未运行，无法重载"
        return 1
    fi
    log_warn "Go 版不支持热重载，正在执行重启..."
    restart_service
}

main() {
    case "${1:-}" in
        start)   start_service   ;;
        stop)    stop_service    ;;
        restart) restart_service ;;
        status)  status_service  ;;
        logs)    view_logs       ;;
        reload)  reload_service  ;;
        *)
            echo "用法: $0 {start|stop|restart|status|logs|reload}"
            echo ""
            echo "  start   - 启动服务"
            echo "  stop    - 停止服务（SIGTERM 优雅关闭）"
            echo "  restart - 重启服务"
            echo "  status  - 查看服务状态"
            echo "  logs    - 实时查看日志"
            echo "  reload  - 重载配置（通过重启实现）"
            echo ""
            echo "环境变量: CONFIG_FILE 指定配置文件路径（默认: 项目根目录/config.yaml）"
            exit 1
            ;;
    esac
}

main "$@"
