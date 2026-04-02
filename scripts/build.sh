#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT_DIR/build"
COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"
VERSION="${VERSION:-$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || echo "dev")}"
LDFLAGS="-s -w -X main.Version=$VERSION"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC}  $*"; }
error() { echo -e "${RED}[错误]${NC} $*" >&2; }

DOCKER_COMPOSE=()

init_compose() {
    if docker compose version >/dev/null 2>&1; then
        DOCKER_COMPOSE=(docker compose)
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1; then
        DOCKER_COMPOSE=(docker-compose)
        return 0
    fi
    error "未找到 docker compose / docker-compose。"
    exit 1
}

check_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        error "未找到 Docker。"
        exit 1
    fi
    if ! docker info >/dev/null 2>&1; then
        error "Docker daemon 未就绪，请先启动 Docker Desktop / OrbStack。"
        exit 1
    fi
}

prepare_workspace() {
    mkdir -p \
        "$BUILD_DIR" \
        "$ROOT_DIR/.cache/go-build" \
        "$ROOT_DIR/.cache/go-mod" \
        "$ROOT_DIR/.gradle-user-home"
}

compose_build() {
    local service="$1"
    info "准备构建环境: $service"
    "${DOCKER_COMPOSE[@]}" -f "$COMPOSE_FILE" build "$service"
}

compose_run() {
    local service="$1"
    local command="$2"
    "${DOCKER_COMPOSE[@]}" -f "$COMPOSE_FILE" run --rm -T \
        -u "$(id -u):$(id -g)" \
        -e HOME=/tmp/docker-home \
        -e VERSION="$VERSION" \
        -e LDFLAGS="$LDFLAGS" \
        "$service" \
        bash -c "mkdir -p /tmp/docker-home && $command"
}

compose_run_root() {
    local service="$1"
    local command="$2"
    "${DOCKER_COMPOSE[@]}" -f "$COMPOSE_FILE" run --rm -T \
        -e HOME=/tmp/docker-home \
        -e VERSION="$VERSION" \
        -e LDFLAGS="$LDFLAGS" \
        -e HOST_UID="$(id -u)" \
        -e HOST_GID="$(id -g)" \
        "$service" \
        bash -c "mkdir -p /tmp/docker-home && $command"
}

build_server() {
    info "使用 Docker 构建服务端 (linux/amd64)..."
    compose_build go-builder
    compose_run go-builder \
        "cd /workspace/server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags \"\$LDFLAGS\" -o /workspace/build/clipcascade-server-linux-amd64 ."
    info "✅ 服务端构建成功 → $BUILD_DIR/clipcascade-server-linux-amd64"
}

build_server_cross() {
    info "使用 Docker 交叉编译服务端..."
    compose_build go-builder
    compose_run go-builder '
        cd /workspace/server
        # 只保留 3 个核心 OS 版本
        for target in linux/amd64 darwin/arm64 windows/amd64; do
            os="${target%/*}"
            arch="${target#*/}"
            ext=""
            if [[ "$os" == "windows" ]]; then
                ext=".exe"
            fi
            echo "[构建] server -> ${os}/${arch}"
            CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -ldflags "$LDFLAGS" -o "/workspace/build/clipcascade-server-${os}-${arch}${ext}" .
        done
    '
    info "✅ 服务端交叉编译完成"
}

build_desktop() {
    info "使用 Docker 构建桌面端 (linux/amd64)..."
    compose_build desktop-builder
    compose_run desktop-builder \
        "cd /workspace/desktop && GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags \"\$LDFLAGS\" -o /workspace/build/clipcascade-desktop-linux-amd64 ."
    info "✅ 桌面端构建成功 → $BUILD_DIR/clipcascade-desktop-linux-amd64"
}

build_desktop_cross() {
    info "使用 Docker 构建桌面端跨平台产物..."
    compose_build desktop-builder
    compose_run desktop-builder '
        cd /workspace/desktop
        echo "[构建] desktop -> linux/amd64"
        GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o /workspace/build/clipcascade-desktop-linux-amd64 .
        echo "[构建] desktop -> windows/amd64"
        # Windows 默认隐藏控制台窗口（通过系统托盘和 Web 控制台交互）
        win_ldflags="$LDFLAGS -H=windowsgui"
        GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$win_ldflags" -o /workspace/build/clipcascade-desktop-windows-amd64.exe .
    '
    warn "macOS Desktop 仍依赖 Apple SDK，普通 Linux Docker 中不提供，已跳过 darwin 产物。"
    info "✅ 桌面端跨平台构建结束"
}

build_desktop_ui() {
    info "使用 Docker 构建 Fyne Desktop UI (linux/amd64)..."
    compose_build fyne-builder
    compose_run fyne-builder \
        "cd /workspace/fyne_mobile && GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags \"\$LDFLAGS\" -o /workspace/build/clipcascade-ui-desktop-linux-amd64 ."
    info "✅ Desktop UI 构建成功 → $BUILD_DIR/clipcascade-ui-desktop-linux-amd64"
}

build_desktop_ui_cross() {
    info "使用 Docker 构建 Desktop UI 跨平台产物..."
    compose_build fyne-builder
    compose_run fyne-builder '
        cd /workspace/fyne_mobile
        echo "[构建] desktop-ui -> linux/amd64"
        GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o /workspace/build/clipcascade-ui-desktop-linux-amd64 .
        echo "[构建] desktop-ui -> windows/amd64"
        CC=x86_64-w64-mingw32-gcc \
        CXX=x86_64-w64-mingw32-g++ \
        GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
        go build -ldflags "$LDFLAGS" -o /workspace/build/clipcascade-ui-desktop-windows-amd64.exe .
        [[ -f /workspace/build/clipcascade-ui-desktop-linux-amd64 ]] || { echo "Linux UI 构建产物缺失"; exit 1; }
        [[ -f /workspace/build/clipcascade-ui-desktop-windows-amd64.exe ]] || { echo "Windows UI 构建产物缺失"; exit 1; }
    '
    warn "macOS UI 仍依赖 Apple SDK，当前 Docker 构建链未覆盖 darwin 目标。"
    info "✅ Desktop UI 跨平台构建结束"
}

build_mobile_android() {
    info "使用 Docker 构建 Fyne Android APK..."
    compose_build android-builder
    compose_run android-builder '
        cd /workspace/fyne_mobile
        fyne package -os android/arm64 -app-id com.clipcascade.mobile -tags netgo -release
        cp -f fynemobile.apk /workspace/build/clipcascade-mobile.apk
    '
    info "✅ Android APK 构建成功 → $BUILD_DIR/clipcascade-mobile.apk"
}

build_mobile_android_native() {
    info "使用 Docker 构建 Android 原生保活版 APK..."
    if ! bash "$ROOT_DIR/scripts/build_android.sh"; then
        error "Android 原生保活版构建失败。"
        exit 1
    fi
}

build_mobile_ios() {
    error "iOS 产物不能在通用 Linux Docker 构建链中完成，仍需要 macOS + Apple 工具链。"
    exit 1
}

build_docker_image() {
    info "构建服务端运行镜像..."
    docker build \
        -t clipcascade-server:latest \
        -t "clipcascade-server:${VERSION}" \
        -f "$ROOT_DIR/server/Dockerfile" \
        "$ROOT_DIR"
    info "✅ Docker 运行镜像构建成功 → clipcascade-server:latest, clipcascade-server:${VERSION}"
}

tidy() {
    info "使用 Docker 执行 go mod tidy..."
    compose_build go-builder
    compose_run go-builder '
        for d in /workspace/pkg /workspace/server /workspace/desktop /workspace/fyne_mobile; do
            if [[ -d "$d" ]]; then
                (cd "$d" && go mod tidy)
            fi
        done
    '
    info "✅ 模块依赖整理完成"
}

test_all() {
    info "使用 Docker 执行 Go 测试..."
    compose_build desktop-builder
    compose_run desktop-builder \
        "cd /workspace && go test ./pkg/... ./server/... ./desktop/..."
    info "✅ 测试执行完成"
}

fmt_all() {
    info "使用 Docker 执行 gofmt..."
    compose_build go-builder
    compose_run go-builder \
        "cd /workspace && gofmt -w pkg/ server/ desktop/ fyne_mobile/"
    info "✅ 格式化完成"
}

clean() {
    info "清理构建产物..."
    rm -rf "$BUILD_DIR"
    info "✅ 已删除 $BUILD_DIR"
}

show_help() {
    cat <<'EOF'
ClipCascade Docker 构建脚本
用法: ./scripts/build.sh {server|desktop|cross|docker|tidy|test|fmt|clean}

说明:
  由于开发时间限制，当前仅支持以下 3 个平台的【终端 + Web控制面板】版本：
  - macOS (Apple Silicon arm64)
  - Linux (amd64)
  - Windows (amd64)
EOF
}

main() {
    [[ $# -eq 0 ]] && show_help && exit 0

    check_docker
    init_compose
    prepare_workspace

    for target in "$@"; do
        case "$target" in
            server)               build_server ;;
            server-cross)         build_server_cross ;;
            desktop)              build_desktop ;;
            desktop-ui)           build_desktop_ui ;;
            desktop-ui-cross)     build_desktop_ui_cross ;;
            cross)                build_server_cross; build_desktop_cross; build_desktop_ui_cross ;;
            mobile-android)       build_mobile_android ;;
            mobile-android-native) build_mobile_android_native ;;
            mobile-ios)           build_mobile_ios ;;
            docker)               build_docker_image ;;
            all)                  build_server_cross
                                  build_desktop_cross
                                  build_desktop_ui
                                  build_desktop_ui_cross
                                  build_mobile_android
                                  build_mobile_android_native
                                  build_docker_image ;;
            tidy)                 tidy ;;
            test)                 test_all ;;
            fmt)                  fmt_all ;;
            clean)                clean ;;
            *)                    show_help; exit 1 ;;
        esac
    done

    info "操作完成!"
}

main "$@"
