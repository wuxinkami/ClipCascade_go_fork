#!/usr/bin/env bash
# 完整发布脚本：编译所有平台 -> 提取 linux-x86 服务端 -> 打包极简 Docker 镜像 -> 推送到私有 Harbor
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT_DIR/build"

# 设置目标镜像 tags
IMAGE_NAME="harbor.wuxinkami.cn/app/clipcascade-server"
# 优先使用环境变量传入的 VERSION，否则使用 git tag 作为版本，获取失败则 fallback 到 latest
VERSION="${VERSION:-$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || echo "latest")}"
IMAGE_TAG="$IMAGE_NAME:$VERSION"
IMAGE_LATEST="$IMAGE_NAME:latest"

echo "==========================================="
echo "1. 触发编译所有版本"
echo "==========================================="
# 只编译服务端(全平台) + 桌面端(Linux/Windows)，不编译移动端
"$SCRIPT_DIR/build.sh" server-cross
"$SCRIPT_DIR/build.sh" cross
if [ $? -ne 0 ]; then
    echo "[错误] 编译失败，发布终止。"
    exit 1
fi

echo ""
echo "==========================================="
echo "2. 构建 Docker 最小镜像并打包 linux-amd64"
echo "==========================================="
# 检测静态编译的服务端是否存在
SERVER_BIN="$BUILD_DIR/clipcascade-server-linux-amd64"
if [ ! -f "$SERVER_BIN" ]; then
    echo "[错误] 未找到编译产物: $SERVER_BIN"
    exit 1
fi

# 写入临时的 Dockerfile.release
DOCKERFILE="$BUILD_DIR/Dockerfile.release"
cat <<EOF > "$DOCKERFILE"
# 阶段1: 拉取基础的根证书和时区数据
FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates tzdata

# 阶段2: 最终最小镜像 (scratch 代表空镜像，只占用二进制文件的体积)
FROM scratch

# 导入网络请求(如mDNS/P2P/HTTPS)需要的证书和时区
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo/

# 直接放入预构建的静态编译二进制文件 (上一步 scripts/build.sh 用 CGO_ENABLED=0 构建了此文件)
COPY clipcascade-server-linux-amd64 /clipcascade-server

# 指定配置与环境变量。标准输出 stdout / stderr 将自然的被 Docker Logs 接管，方便查看日志
ENV CC_DATABASE_PATH=/data/database/clipcascade.db
ENV CC_PORT=8080

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/clipcascade-server"]
EOF

echo "[信息] 正在构建 Docker 镜像 ($IMAGE_LATEST)..."
docker build -t "$IMAGE_LATEST" -t "$IMAGE_TAG" -f "$DOCKERFILE" "$BUILD_DIR"

echo ""
echo "==========================================="
echo "3. 推送镜像到 Harbor"
echo "==========================================="
echo "[信息] 推送 $IMAGE_LATEST ..."
docker push "$IMAGE_LATEST"

echo "[信息] 推送 $IMAGE_TAG ..."
docker push "$IMAGE_TAG"

echo ""
echo "==========================================="
echo "🎉 发布成功！"
echo "拉取命令:"
echo "docker pull $IMAGE_LATEST"
echo "==========================================="
