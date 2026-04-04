# ClipCascade Go

一个面向多设备协作的跨平台剪贴板同步工具。
目标不是“再造一个聊天工具”，而是让跨设备复制粘贴尽量接近本机体验。

## 项目介绍

在日常开发和办公场景里，Mac、Windows、Linux 经常混用。
文本、截图、文件在不同设备之间来回传，常见痛点是：

- 需要手动打开中转工具，打断工作流。
- 不同平台剪贴板行为差异大，体验不一致。
- 文件传输要么太重，要么太慢，且难统一管理。
- 截图、图片路径、文件粘贴在不同平台上经常语义不统一。

ClipCascade 的定位是：以独立剪贴板为中心，做一个可自部署、可扩展、可跨平台的同步系统。



## 核心能力

- 文本同步：系统原生 `Ctrl+C` / `Cmd+C` 后自动广播；异机立即进入系统剪贴板，自发自收只记历史不二次改写剪贴板。
- 图片同步：截图、剪贴板图片、单个图片文件即时广播；异机可直接原生粘贴图片本身，也可按热键取 tmp 路径或真实图片文件。
- 文件同步：普通文件、文件夹、多文件先广播 `file_stub`，按 `Ctrl+Alt+Shift+V` / `Cmd+Option+Shift+V` 时再发起或复用真实传输。
- 幂等传输：重复复制、重复请求、重复热键都优先复用已有传输和 tmp 结果，不重复落盘副本。
- 传输通道：常规剪贴板消息走 `P2P + STOMP`，文件控制消息走“定向 P2P，失败回退 STOMP”。
- 安全能力：支持 E2EE、路径限制、Zip Slip 防御、解压字节上限、WS 帧大小限制。
- 自动发现：局域网 mDNS 自动发现服务地址。
- 多用户管理：支持管理员在 Web 页面直接新增用户。


## 支持平台

| 模块 | 平台 |
| --- | --- |
| Server | Linux / macOS / Windows |
| Desktop（终端 + Web 控制面板） | Linux / macOS / Windows |


## 解决什么问题

### 1)网络问题
不需要切换软件就能直接同步到另一台电脑
不同平台的剪切板不够不互通
不需要安装什么奇怪的输入法登录同步
不需要联网敏感
数据也能发



---

## 启动服务端

### 二进制直接运行

```bash
./clipcascade-server-linux-amd64 \
  --port 8080 \
  --db ./data/clipcascade.db \
  --p2p=true \
  --stun "stun:stun.qq.com:18123" \
  --signup=false
```

默认账号：`admin / admin123`

### 服务端环境变量（完整参数）

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `CC_PORT` | 监听端口 | `8080` |
| `CC_MAX_MESSAGE_SIZE_IN_MiB` | 最大消息大小（MiB） | `20` |
| `CC_MAX_MESSAGE_SIZE_IN_BYTES` | 最大消息大小（字节，设置后覆盖 MiB） | `0`（不覆盖） |
| `CC_P2P_ENABLED` | 启用 P2P 点对点传输 | `true` |
| `CC_P2P_STUN_URL` | STUN 服务器地址 | `stun:stun.l.google.com:19302` |
| `CC_ALLOWED_ORIGINS` | CORS 允许的来源 | `*` |
| `CC_SIGNUP_ENABLED` | 是否开放注册 | `false` |
| `CC_MAX_USER_ACCOUNTS` | 最大用户数（-1 不限） | `-1` |
| `CC_ACCOUNT_PURGE_TIMEOUT_SECONDS` | 用户自动清理超时（秒，-1 禁用） | `-1` |
| `CC_SESSION_TIMEOUT` | Session 过期时间（分钟） | `525960`（≈1 年） |
| `CC_DATABASE_PATH` | SQLite 数据库路径 | `./database/clipcascade.db` |
| `CC_MAX_UNIQUE_IP_ATTEMPTS` | 暴力攻击防护：最大唯一 IP 数 | `15` |
| `CC_MAX_ATTEMPTS_PER_IP` | 暴力攻击防护：单 IP 最大尝试次数 | `30` |
| `CC_LOCK_TIMEOUT_SECONDS` | 暴力攻击防护：锁定初始超时（秒） | `60` |
| `CC_LOCK_TIMEOUT_SCALING_FACTOR` | 暴力攻击防护：锁定缩放倍数 | `2` |
| `CC_BFA_CACHE_ENABLED` | 暴力攻击防护：启用缓存 | `true` |
| `CC_EXTERNAL_BROKER_ENABLED` | 启用外部 STOMP Broker | `false` |
| `CC_BROKER_HOST` | 外部 Broker 地址 | `localhost` |
| `CC_BROKER_PORT` | 外部 Broker 端口 | `61613` |

### Docker Compose 部署（完整参数）

```yaml
services:
  clipcascade:
    image: ghcr.io/wuxinkami/clipcascade:latest
    container_name: clipcascade
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    environment:
      CC_PORT: "8080"
      CC_DATABASE_PATH: /data/database/clipcascade.db
      CC_SIGNUP_ENABLED: "false"
      CC_MAX_USER_ACCOUNTS: "-1"
      CC_ACCOUNT_PURGE_TIMEOUT_SECONDS: "-1"
      CC_SESSION_TIMEOUT: "525960"
      CC_P2P_ENABLED: "true"
      CC_P2P_STUN_URL: "stun:stun.qq.com:18123"
      CC_ALLOWED_ORIGINS: "*"
      CC_MAX_MESSAGE_SIZE_IN_MiB: "20"
      CC_MAX_UNIQUE_IP_ATTEMPTS: "15"
      CC_MAX_ATTEMPTS_PER_IP: "30"
      CC_LOCK_TIMEOUT_SECONDS: "60"
      CC_LOCK_TIMEOUT_SCALING_FACTOR: "2"
      CC_BFA_CACHE_ENABLED: "true"
```



### docker run 命令（完整参数）

```bash
docker run -d \
  --name clipcascade \
  --restart unless-stopped \
  -p 8080:8080 \
  -v "$(pwd)/data:/data" \
  -e CC_PORT=8080 \
  -e CC_DATABASE_PATH=/data/database/clipcascade.db \
  -e CC_SIGNUP_ENABLED=false \
  -e CC_MAX_USER_ACCOUNTS=-1 \
  -e CC_ACCOUNT_PURGE_TIMEOUT_SECONDS=-1 \
  -e CC_SESSION_TIMEOUT=525960 \
  -e CC_P2P_ENABLED=true \
  -e CC_P2P_STUN_URL='stun:stun.qq.com:18123' \
  -e CC_ALLOWED_ORIGINS='*' \
  -e CC_MAX_MESSAGE_SIZE_IN_MiB=20 \
  -e CC_MAX_UNIQUE_IP_ATTEMPTS=15 \
  -e CC_MAX_ATTEMPTS_PER_IP=30 \
  -e CC_LOCK_TIMEOUT_SECONDS=60 \
  -e CC_LOCK_TIMEOUT_SCALING_FACTOR=2 \
  -e CC_BFA_CACHE_ENABLED=true \
  ghcr.io/wuxinkami/clipcascade:latest
```

---

## 启动桌面端

### 客户端 CLI 参数（完整）

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `--server <url>` | 服务器地址 | `http://localhost:8080` |
| `--username <user>` | 登录用户名 | *(空)* |
| `--password <pass>` | 登录密码 | *(空)* |
| `--e2ee` / `--no-e2ee` | 启用/禁用端到端加密 | 启用 |
| `--p2p` / `--no-p2p` | 启用/禁用 P2P 传输 | 启用 |
| `--stun <url>` | STUN 服务器地址 | `stun:stun.qq.com:18123` |
| `--auto-reconnect` / `--no-auto-reconnect` | 启用/禁用自动重连 | 启用 |
| `--reconnect-delay <sec>` | 重连延迟（秒） | `5` |
| `--file-memory-threshold-mib <n>` | 文件内存归档阈值（MiB） | `1024` (最大 `5120`) |
| `--web-port <port>` | 控制面板监听端口 | `16666` |
| `--save` | 将当前参数保存到配置文件 | - |
| `--debug` | 启用调试日志 | - |

密码也可以通过环境变量注入：`CLIPCASCADE_PASSWORD=xxx`

### 首次配置（完整命令）

**Linux 托盘版**:

```bash
./clipcascade-desktop-linux-amd64 \
  --server http://127.0.0.1:8080 \
  --username admin \
  --password admin123 \
  --e2ee \
  --p2p \
  --stun "stun:stun.qq.com:18123" \
  --auto-reconnect \
  --reconnect-delay 5 \
  --file-memory-threshold-mib 1024 \
  --web-port 16666 \
  --save
```

**Windows 托盘版**:

```powershell
.\clipcascade-desktop-windows-amd64.exe `
  --server http://127.0.0.1:8080 `
  --username admin `
  --password admin123 `
  --e2ee `
  --p2p `
  --stun "stun:stun.qq.com:18123" `
  --auto-reconnect `
  --reconnect-delay 5 `
  --file-memory-threshold-mib 1024 `
  --web-port 16666 `
  --save
```

**macOS 版（终端 + Web 控制面板）**:

```bash
./clipcascade-desktop-darwin-arm64 \
  --server http://127.0.0.1:8080 \
  --username admin \
  --password admin123 \
  --e2ee \
  --p2p \
  --stun "stun:stun.qq.com:18123" \
  --auto-reconnect \
  --reconnect-delay 5 \
  --file-memory-threshold-mib 1024 \
  --web-port 16666 \
  --save
```

### 最简启动（使用默认值）

```bash
# 首次保存基本配置
./clipcascade-desktop-linux-amd64 --server http://127.0.0.1:8080 --username admin --password admin123 --save

# 后续直接运行（自动读取配置文件）
./clipcascade-desktop-linux-amd64
```

### 生成的配置文件

首次运行时会自动生成配置文件，内容示例：

```json
{
  "server_url": "http://localhost:8080",
  "username": "",
  "password_encrypted": "",
  "e2ee_enabled": true,
  "p2p_enabled": true,
  "stun_url": "stun:stun.qq.com:18123",
  "auto_reconnect": true,
  "reconnect_delay_sec": 5,
  "file_memory_threshold_mib": 1024,
  "web_port": 16666
}
```

配置文件路径：
- **Linux**: `~/.config/ClipCascade/config.json`
- **macOS**: `~/Library/Application Support/ClipCascade/config.json`
- **Windows**: `%APPDATA%\ClipCascade\config.json`

调试日志：

```bash
./clipcascade-desktop-linux-amd64 --debug
```

### 通俗使用方式

装好客户端、配好服务器地址和账号之后，平时只要记住下面 4 个动作：

| 动作 | 作用 | 现在的实际语义 |
|------|------|----------------|
| `Ctrl+C` / `Cmd+C` | 主发送动作 | 正常复制就是主链路。文本、截图、剪贴板图片、单个图片文件会自动广播；普通文件、文件夹、多文件只广播清单，不立刻传真实内容。 |
| `Ctrl+Alt+Shift+C` / `Cmd+Shift+Option+C` | 手动补发 | 兜底重发当前系统剪贴板。一般不用，只有自动广播漏掉时再按。 |
| `Ctrl+Alt+V` / `Cmd+Option+V` | 图片路径模式 | 只处理截图、剪贴板图片、单个图片文件。会先把 `tmp` 里的目标路径文本放进系统剪贴板，再尽量自动模拟一次粘贴。 |
| `Ctrl+Alt+Shift+V` / `Cmd+Shift+Option+V` | 真实内容模式 | 处理图片和文件。图片会放入系统文件型剪贴板并尽量自动模拟一次粘贴；文件会先启动或复用传输，完成后再把真实文件结果放入系统文件型剪贴板，不自动模拟粘贴。 |

再说得更直白一点：

- 文本：发送端 `Ctrl+C` 后，异机收到就会进系统剪贴板，通常直接 `Ctrl+V` 就能粘贴。
- 图片：发送端 `Ctrl+C` 后，异机会先收进 ClipCascade 内存，再写入系统剪贴板，所以通常也能直接 `Ctrl+V` 粘贴图片本身。
- 自发自收：自己收到自己的广播时，只记历史和 Web 控制台记录，不重复改写当前系统剪贴板。
- 普通文件 / 文件夹 / 多文件：发送端 `Ctrl+C` 后只是“告诉其他设备有这批文件”，真正要取真实文件时按 `Ctrl+Alt+Shift+V`。
- `Ctrl+Alt+V` 不是通用跨设备粘贴键，它现在只负责图片路径文本。

**首次启动：**
- 客户端启动后会自动打开浏览器控制面板（默认端口 `16666`），引导你配置服务器和账号
- 密码会使用 AES-256-GCM 加密存储在本地，不会以明文保存

---

## 用户管理

- 服务端和客户端,均有web后台.
- 服务端启动的时候有指定端口
- 客户端默认端口为16666,右键右下角任务栏软件图标,打开控制台,跳转动态唯一url


## 同步策略（当前设计）

### 自动发送

- 桌面端现在以“正常复制后自动广播”为主流程，主发送动作就是系统原生 `Ctrl+C` / `Cmd+C`。
- `Ctrl+Alt+Shift+C`（macOS 为 `Cmd+Shift+Option+C`）只是兜底补发，不是主发送键。
- 文本、图片、文件都优先走 `P2P`；没有可用 `P2P` peer 时回退到 `STOMP`。

### 文本

- 文本复制后会自动广播到所有在线设备。
- 异机接收端收到文本后，会立即覆盖本机系统剪贴板，所以通常直接按系统原生 `Ctrl+V` / `Cmd+V` 就能粘贴。
- 发送端收到自己的文本广播时，只记共享历史，不会再把同一段文本写回系统剪贴板。

### 图片 / 文件

- 截图、剪贴板图片、单张图片文件会直接同步到在线设备。
- 异机接收端收到图片后，会先把图片保存在 ClipCascade 内存里，再写入本机系统剪贴板，所以用户通常也能直接 `Ctrl+V` / `Cmd+V` 粘贴图片本身。
- 自发自收时，收到自己的图片广播只记历史，不二次改写系统剪贴板。
- 普通单文件、多个文件、单个文件夹、多个文件夹走 `file_stub` / lazy manifest，不会在接收端第一时间抢占系统剪贴板。
- `Ctrl+Alt+V`（macOS 为 `Cmd+Option+V`）：
  - 只处理截图、剪贴板图片、单个图片文件。
  - 会先规划好 `tmp` 目录中的目标路径，并立即把这个路径文本放进系统剪贴板。
  - 随后尽量自动模拟一次系统 `Ctrl+V` / `Cmd+V`；如果失败，用户可以手动粘贴。
  - 真实图片文件可以在后台继续从内存物化到同一路径，不要求先完全落盘再给路径。
  - 普通文件、文件夹、多文件按这组热键不会触发路径输出。
- `Ctrl+Alt+Shift+V`（macOS 为 `Cmd+Shift+Option+V`）：
  - 图片会确保本机 `tmp` 目录里已有真实图片文件，然后把这份真实图片文件放进系统文件型剪贴板。
  - 图片写入完成后会尽量自动再模拟一次系统 `Ctrl+V` / `Cmd+V`，方便直接把这份图片文件放进资源管理器目标目录。
  - 文件类内容第一次触发只负责启动或复用传输，把真实结果准备到本机 `tmp` 目录。
  - 文件类内容准备完成后，再次触发会把 `tmp` 里的真实文件结果放进系统文件型剪贴板；多文件会保留原目录结构和文件名。
  - 同一条共享文件内容无论重复按多少次，都只传输一次；后续只复用本机已经准备好的 `tmp` 结果。
  - 非图片文件类不会自动再模拟一次 `Ctrl+V` / `Cmd+V`；文件最终由用户自己手动粘贴。
- 如果传输失败，会有通知提示；再次按相同热键会复用同一条共享内容、同一个本机目标路径和同一份本机结果继续执行。

### 共享剪贴板语义

- 所有在线客户端共同维护"最近一次真正共享出去的内容"，也就是发送端最近一次 `Ctrl+C` 的结果。
- `Ctrl+Alt+V` 和 `Ctrl+Alt+Shift+V` 无论重复按多少次，操作目标始终都是这条共享内容，而不是本机后来临时写进去的路径文本或文件剪贴板。
- 每个客户端只维护自己的本机结果：
  - 本机占位路径
  - 本机已接收的真实文件
  - 本机多文件解压目录
- 自发自收时，图片和文件在用户主动按热键后，依然走和异机接收端相同的 `tmp` 目录准备逻辑，而不是直接复用发送端当时的原始源路径。
- 所以不同设备可以分别、多次、任意顺序地执行 `Ctrl+Alt+V` / `Ctrl+Alt+Shift+V`，最终都是幂等的：
  - 未传完时就继续传
  - 传完后只重放本机结果，不再重复传输

### 临时目录与落盘

- 目标临时目录统一为系统缓存 / 临时目录下的 `ClipCascade`。
- 截图和图片数据先保存在内存中；只有在用户按热键消费时，才会按需要物化到 `tmp` 目录。
- 无名截图使用时间戳命名，如 `20260330193725.png`。
- 单个图片、单文件最终保留发送端原文件名。
- 单文件夹最终保留发送端原目录名。
- 同名单文件或图片默认复用同一路径：同名同内容不重复落盘，同名不同内容覆盖旧文件，不再生成 `_1`、`_2` 副本。
- 多文件和多目录/混合选择最终会落到 `ClipCascade/<timestamp>/` 这样的临时目录中，目录里放的是解压后的真实文件集合。
- 文件归档优先走"内存归档模式"：
  - `P2P` 已就绪
  - 归档大小不超过 `file_memory_threshold_mib`
  - 内存申请成功
- 不满足条件时自动回退到系统 `TEMP/TMP` 下的临时归档文件。

临时文件清理：

- 接收文件时会自动清理 `24 小时`前的旧临时文件。
- 磁盘回退模式下的中间态 `payload.zip` / `payload.bin` 会在成功落地后尽快删除。
- 多文件的临时解压目录也会复用同一套清理目录。

### 平台说明

- Windows：主发送动作是系统原生 `Ctrl+C`；额外热键为 `Ctrl+Alt+Shift+C`（手动补发）、`Ctrl+Alt+V`（图片路径）、`Ctrl+Alt+Shift+V`（真实内容）。
- Linux X11：主发送动作和额外热键与 Windows 一致。
- Linux Wayland：剪贴板链路正式支持；热键注册与模拟输入会先尝试 portal，再回退到 X11 / XWayland，属于 best-effort。
- macOS：主发送动作是系统原生 `Cmd+C`；额外热键为 `Cmd+Shift+Option+C`（手动补发）、`Cmd+Option+V`（图片路径）、`Cmd+Shift+Option+V`（真实内容）。
- macOS 的全局热键和部分系统级能力依赖"辅助功能"权限。

---

## 执行构建

由于开发时间限制，当前仅支持以下 3 个平台的【终端 + Web 控制面板】版本：

| 平台 | 构建后的产物名 | 状态 |
|------|-------------|------|
| **macOS** | `clipcascade-desktop-darwin-arm64` | ✅ 支持 (需本地构建) |
| **Linux** | `clipcascade-desktop-linux-amd64` | ✅ 支持 |
| **Windows**| `clipcascade-desktop-windows-amd64.exe` | ✅ 支持 |

> **注**：服务端同理支持上述 3 个平台的发行版。

### 1) 本地一键构建 (推荐)

如果你在对应的操作系统上，可以直接使用 `Makefile` 本地构建（不依赖 Docker）：

```bash
# macOS arm64 桌面端
make native-desktop

# 构建产物会在 build/ 目录下
./build/clipcascade-desktop-darwin-arm64
```

### 2) Docker 交叉编译 (CI/CD)

```bash
# 交叉编译 Linux 和 Windows 产物
./scripts/build.sh cross
```

`cross` 会生成 Linux 和 Windows 的服务端及桌面端产物。

---

---

## 常见问题

### 1) Docker 相关构建失败

报错：`Cannot connect to the Docker daemon ...`

处理：

```bash
open -a Docker
# 或
open -a OrbStack
```

确认 `docker info` 正常后重试。

### 2) 登录页点 Create 显示 Signup is disabled

这是配置行为（默认关闭注册）。

- 开启公开注册：`CC_SIGNUP_ENABLED=true`
- 推荐方式：保持关闭，由管理员在 `/advance` 新增用户。

### 3) 文件能收到提示但粘贴不可用

请确认两端都使用最新 desktop 二进制，并检查日志是否出现：

- `应用：准备发送剪贴板更新 类型=file_stub`
- `剪贴板：收到文件懒加载占位符`
- `应用：开始请求文件传输`
- `应用：文件传输已完成`


---

## 致谢

本项目 fork 自 [Chaoleme/ClipCascade_go](https://github.com/Chaoleme/ClipCascade_go)，在其基础上进行了大量功能扩展和改进。

原始项目灵感来源于 [Sathvik-Rao/ClipCascade](https://github.com/Sathvik-Rao/ClipCascade)（Python/Java 实现）。

## 社区

[LINUX DO](https://linux.do)

## 许可证

本项目基于 [Apache License 2.0](LICENSE) 协议开源。
