#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$ROOT_DIR/build"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC}  $*"; }
error() { echo -e "${RED}[错误]${NC} $*" >&2; }

mkdir -p "$BUILD_DIR" "$ROOT_DIR/mobile/android/app/libs"
cd "$ROOT_DIR"

export ANDROID_HOME="${ANDROID_HOME:-/opt/android-sdk}"
export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/26.1.10909125}"
export GRADLE_USER_HOME="${GRADLE_USER_HOME:-$ROOT_DIR/.gradle-user-home}"
export GOCACHE="${GOCACHE:-$ROOT_DIR/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$ROOT_DIR/.cache/go-mod}"
export GOSUMDB="${GOSUMDB:-off}"
export PATH="$PATH:$(go env GOPATH)/bin:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools"

mkdir -p "$GRADLE_USER_HOME" "$GOCACHE" "$GOMODCACHE"

if ! command -v gomobile >/dev/null 2>&1; then
    error "容器中缺少 gomobile。"
    exit 1
fi

ANDROID_API="${CC_ANDROID_API:-26}"

info "第一步：使用 gomobile 编译 Go 核心逻辑为 engine.aar..."
gomobile bind -target=android -androidapi "$ANDROID_API" -javapkg bridge -o mobile/android/app/libs/engine.aar ./fyne_mobile/bridge
info "✅ Go 引擎编译成功: mobile/android/app/libs/engine.aar"

info "第二步：使用 Gradle 组装 Android Kotlin 壳应用..."
cd mobile/android
chmod +x ./gradlew
./gradlew --no-daemon clean assembleDebug assembleRelease
cd "$ROOT_DIR"

APK_DEBUG_SRC="mobile/android/app/build/outputs/apk/debug/app-debug.apk"
APK_RELEASE_UNSIGNED_SRC="mobile/android/app/build/outputs/apk/release/app-release-unsigned.apk"
APK_DEBUG_DEST="$BUILD_DIR/ClipCascade-Android-Debug.apk"
APK_RELEASE_UNSIGNED_DEST="$BUILD_DIR/ClipCascade-Android-Release-Unsigned.apk"
APK_INSTALLABLE_DEST="$BUILD_DIR/ClipCascade-Android-Installable.apk"

if [[ -f "$APK_DEBUG_SRC" ]]; then
    cp "$APK_DEBUG_SRC" "$APK_DEBUG_DEST"
    cp "$APK_DEBUG_SRC" "$APK_INSTALLABLE_DEST"
fi

if [[ -f "$APK_RELEASE_UNSIGNED_SRC" ]]; then
    cp "$APK_RELEASE_UNSIGNED_SRC" "$APK_RELEASE_UNSIGNED_DEST"
fi

if [[ ! -f "$APK_DEBUG_DEST" && ! -f "$APK_RELEASE_UNSIGNED_DEST" ]]; then
    error "找不到构建产物 APK 文件。"
    exit 1
fi

info "🎉 Android 安装包构建完毕"
if [[ -f "$APK_DEBUG_DEST" ]]; then
    info "📦 可直接安装包（Debug）: $APK_DEBUG_DEST"
    info "📦 便捷安装别名: $APK_INSTALLABLE_DEST"
fi
if [[ -f "$APK_RELEASE_UNSIGNED_DEST" ]]; then
    info "📦 发布包（Unsigned）: $APK_RELEASE_UNSIGNED_DEST"
fi
