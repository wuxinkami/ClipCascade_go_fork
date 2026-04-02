#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC}  $*"; }
error() { echo -e "${RED}[错误]${NC} $*" >&2; }

docker_compose() {
    if docker compose version >/dev/null 2>&1; then
        docker compose -f "$COMPOSE_FILE" "$@"
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1; then
        docker-compose -f "$COMPOSE_FILE" "$@"
        return 0
    fi
    error "未找到 docker compose / docker-compose。"
    exit 1
}

if ! command -v docker >/dev/null 2>&1; then
    error "未找到 Docker。"
    exit 1
fi

if ! docker info >/dev/null 2>&1; then
    error "Docker daemon 未就绪，请先启动 Docker。"
    exit 1
fi

mkdir -p "$ROOT_DIR/build" "$ROOT_DIR/.cache/go-build" "$ROOT_DIR/.cache/go-mod" "$ROOT_DIR/.gradle-user-home"

info "准备 Android Docker 构建环境..."
docker_compose build android-builder

info "在容器中执行 Android 原生构建..."
# 以 root 运行容器以避免 AAPT2 daemon 权限问题，构建完成后 chown 产物
docker_compose run --rm -T \
    -e HOME=/tmp/docker-home \
    -e HOST_UID="$(id -u)" \
    -e HOST_GID="$(id -g)" \
    android-builder \
    bash -c "mkdir -p /tmp/docker-home && /workspace/scripts/build_android_in_container.sh && chown -R \${HOST_UID}:\${HOST_GID} /workspace/build /workspace/.gradle-user-home /workspace/.cache 2>/dev/null || true"

info "✅ Android 原生保活版构建完成"
