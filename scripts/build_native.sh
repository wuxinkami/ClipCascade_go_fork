#!/usr/bin/env bash
# ClipCascade 本地原生构建脚本（不依赖 Docker）
# 用途：在 macOS / Linux / Windows(MSYS2) 上直接构建桌面端和服务端
#
# 用法:
#   ./scripts/build_native.sh desktop        # 构建当前平台桌面端
#   ./scripts/build_native.sh server         # 构建当前平台服务端
#   ./scripts/build_native.sh all            # 构建桌面端 + 服务端
#   ./scripts/build_native.sh test           # 运行测试
#   ./scripts/build_native.sh clean          # 清理产物
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT_DIR/build"
VERSION="${VERSION:-$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || echo "dev")}"
LDFLAGS="-s -w -X main.Version=$VERSION"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC}  $*"; }
error() { echo -e "${RED}[错误]${NC} $*" >&2; }

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        darwin)  GOOS=darwin ;;
        linux)   GOOS=linux ;;
        mingw*|msys*|cygwin*) GOOS=windows ;;
        *)       error "不支持的操作系统: $os"; exit 1 ;;
    esac

    case "$arch" in
        x86_64|amd64)   GOARCH=amd64 ;;
        arm64|aarch64)  GOARCH=arm64 ;;
        *)              error "不支持的架构: $arch"; exit 1 ;;
    esac

    EXT=""
    if [[ "$GOOS" == "windows" ]]; then
        EXT=".exe"
    fi

    export GOOS GOARCH EXT
    info "检测到平台: ${GOOS}/${GOARCH}"
}

check_go() {
    if ! command -v go &>/dev/null; then
        error "未找到 Go 编译器。请先安装: https://go.dev/dl/"
        exit 1
    fi
    local go_version
    go_version="$(go version)"
    info "Go 版本: $go_version"
}

build_server() {
    info "构建服务端 (${GOOS}/${GOARCH})..."
    mkdir -p "$BUILD_DIR"

    local output="$BUILD_DIR/clipcascade-server-${GOOS}-${GOARCH}${EXT}"

    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -ldflags "$LDFLAGS" \
        -o "$output" \
        "$ROOT_DIR/server"

    info "✅ 服务端构建成功 → $output"
}

build_desktop() {
    info "构建桌面端 (${GOOS}/${GOARCH})..."
    mkdir -p "$BUILD_DIR"

    local output="$BUILD_DIR/clipcascade-desktop-${GOOS}-${GOARCH}${EXT}"
    local cgo=1
    local extra_ldflags="$LDFLAGS"

    # macOS 和 Linux 桌面端需要 CGO（systray、clipboard、热键）
    # Windows 可以 CGO_ENABLED=0 构建（热键/托盘使用纯 syscall）
    if [[ "$GOOS" == "windows" ]]; then
        cgo=0
        extra_ldflags="$LDFLAGS -H=windowsgui"
    fi

    CGO_ENABLED="$cgo" GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -ldflags "$extra_ldflags" \
        -o "$output" \
        "$ROOT_DIR/desktop"

    info "✅ 桌面端构建成功 → $output"

    # macOS 提示
    if [[ "$GOOS" == "darwin" ]]; then
        echo ""
        info "💡 macOS 使用提示:"
        info "   首次运行需要授权辅助功能权限（用于全局热键和自动粘贴）"
        info "   系统偏好设置 → 隐私与安全性 → 辅助功能 → 添加终端/iTerm2"
        echo ""
        info "   启动方式:"
        info "   $output"
        info "   首次启动会自动打开浏览器引导配置连接信息 (端口 6666)"
    fi
}

run_test() {
    info "运行测试..."
    (cd "$ROOT_DIR" && go test ./pkg/... ./server/... ./desktop/... -count=1 -timeout 120s)
    info "✅ 测试完成"
}

do_clean() {
    info "清理构建产物..."
    rm -rf "$BUILD_DIR"
    info "✅ 已删除 $BUILD_DIR"
}

show_help() {
    cat <<'EOF'
ClipCascade 本地原生构建脚本（不依赖 Docker）

用法: ./scripts/build_native.sh <目标>

目标:
  desktop    构建当前平台桌面端（托盘 + Web 控制面板）
  server     构建当前平台服务端
  all        构建桌面端 + 服务端
  test       运行全量测试
  clean      清理构建产物

环境要求:
  - Go 1.22+
  - macOS: Xcode Command Line Tools (xcode-select --install)
  - Linux: libx11-dev, libxcursor-dev, libxrandr-dev, libxinerama-dev,
           libxi-dev, libglx-dev, libgl1-mesa-dev, libxxf86vm-dev
  - Windows: MSYS2/MinGW (可选, CGO_ENABLED=0 也可构建)

示例:
  # macOS M1/M2/M3 上一键构建桌面端
  ./scripts/build_native.sh desktop

  # 构建后直接运行
  ./build/clipcascade-desktop-darwin-arm64
EOF
}

main() {
    [[ $# -eq 0 ]] && show_help && exit 0

    check_go
    detect_platform

    for target in "$@"; do
        case "$target" in
            desktop)  build_desktop ;;
            server)   build_server ;;
            all)      build_server; build_desktop ;;
            test)     run_test ;;
            clean)    do_clean ;;
            *)        show_help; exit 1 ;;
        esac
    done
}

main "$@"
