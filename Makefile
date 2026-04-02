# ClipCascade Go - 构建入口
#
# Docker 构建（适用于 CI/CD 和 Linux 环境）:
#   make server | make desktop | make cross | make all
#
# 本地原生构建（适用于 macOS/Linux/Windows 直接构建，不依赖 Docker）:
#   make native-desktop | make native-server | make native-all
#
# 在 macOS 上一键构建桌面端:
#   make native-desktop

.PHONY: all server server-cross desktop desktop-ui desktop-ui-cross cross \
        mobile-android mobile-android-native mobile-ios docker \
        native-desktop native-server native-all native-test \
        tidy test fmt clean

# ==================== Docker 构建 ====================

server:
	./scripts/build.sh server

server-cross:
	./scripts/build.sh server-cross

desktop:
	./scripts/build.sh desktop

desktop-ui:
	./scripts/build.sh desktop-ui

desktop-ui-cross:
	./scripts/build.sh desktop-ui-cross

cross:
	./scripts/build.sh cross

mobile-android:
	./scripts/build.sh mobile-android

mobile-android-native:
	./scripts/build.sh mobile-android-native

mobile-ios:
	./scripts/build.sh mobile-ios

all:
	./scripts/build.sh all

docker:
	./scripts/build.sh docker

# ==================== 本地原生构建（不依赖 Docker） ====================

native-desktop:
	./scripts/build_native.sh desktop

native-server:
	./scripts/build_native.sh server

native-all:
	./scripts/build_native.sh all

native-test:
	./scripts/build_native.sh test

# ==================== 工具 ====================

tidy:
	./scripts/build.sh tidy

test:
	./scripts/build.sh test

fmt:
	./scripts/build.sh fmt

clean:
	./scripts/build.sh clean
	./scripts/build_native.sh clean
