// ClipCascade Desktop Client - 一个用 Go 编写的跨平台剪贴板同步 client。
//
// 通过 STOMP-over-WebSocket 连接到 ClipCascade server，监控本地剪贴板，
// 并在不同 devices 之间同步更改。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/clipcascade/desktop/app"
	"github.com/clipcascade/desktop/config"
)

func main() {
	// 为初始设置解析命令行参数 (flags)
	serverURL := flag.String("server", "", "服务器地址 (例如 http://localhost:8080)")
	username := flag.String("username", "", "登录用户名")
	password := flag.String("password", "", "登录密码")
	noE2EE := flag.Bool("no-e2ee", false, "禁用端到端加密 (E2EE)")
	e2ee := flag.Bool("e2ee", false, "启用端到端加密 (E2EE，默认已启用)")
	p2p := flag.Bool("p2p", false, "启用 P2P 点对点传输 (默认已启用)")
	noP2P := flag.Bool("no-p2p", false, "禁用 P2P 点对点传输")
	stunURL := flag.String("stun", "", "STUN 服务器地址 (例如 stun:stun.l.google.com:19302)")
	autoReconnect := flag.Bool("auto-reconnect", false, "启用自动重连 (默认已启用)")
	noAutoReconnect := flag.Bool("no-auto-reconnect", false, "禁用自动重连")
	reconnectDelay := flag.Int("reconnect-delay", 0, "重连延迟 (秒，默认 5)")
	fileMemoryThresholdMiB := flag.Int64("file-memory-threshold-mib", 0, "文件传输内存归档阈值 (MiB，默认 1024，最大 5120)")
	webPort := flag.Int("web-port", 0, "控制面板监听端口 (默认 6666)")
	saveConfig := flag.Bool("save", false, "将命令行参数保存到配置文件")
	debugLog := flag.Bool("debug", false, "启用调试日志")
	flag.Parse()

	// 设置日志
	level := slog.LevelInfo
	if *debugLog {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})))

	// 加载 config
	cfg := config.Load()

	// 如果提供了 flags，则进行覆盖
	if *serverURL != "" {
		cfg.ServerURL = config.NormalizeServerURL(*serverURL)
	}
	if *username != "" {
		cfg.Username = *username
	}
	if *password != "" {
		cfg.Password = *password
	}
	if *noE2EE {
		cfg.E2EEEnabled = false
	}
	if *e2ee {
		cfg.E2EEEnabled = true
	}
	if *noP2P {
		cfg.P2PEnabled = false
	}
	if *p2p {
		cfg.P2PEnabled = true
	}
	if *stunURL != "" {
		cfg.StunURL = *stunURL
	}
	if *noAutoReconnect {
		cfg.AutoReconnect = false
	}
	if *autoReconnect {
		cfg.AutoReconnect = true
	}
	if *reconnectDelay > 0 {
		cfg.ReconnectDelay = *reconnectDelay
	}
	if *fileMemoryThresholdMiB > 0 {
		cfg.FileMemoryThresholdMiB = *fileMemoryThresholdMiB
	}
	cfg.FileMemoryThresholdMiB = cfg.FileMemoryThresholdBytes() >> 20
	if *webPort > 0 {
		cfg.WebPort = *webPort
	}

	// 如果有请求，则保存 config
	if *saveConfig {
		if err := cfg.Save(); err != nil {
			slog.Error("failed to save config", "error", err)
			os.Exit(1)
		}
		fmt.Println("✅ Config saved to:", cfg.FilePath)
	}

	// 首次运行自动生成默认配置文件
	cfgPath := cfg.FilePath
	if cfgPath == "" {
		cfgPath = config.ConfigDir() + "/config.json"
	}
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		_ = cfg.Save()
		fmt.Println("📝 已生成默认配置文件:", cfgPath)
	}

	// 如果没有凭据，打印帮助
	if cfg.Username == "" || cfg.Password == "" {
		fmt.Println("ClipCascade Desktop Client")
		fmt.Println()
		fmt.Println("⚠️  未配置凭据。将自动打开控制中心网页引导配置。")
		fmt.Println()
		fmt.Println("   也可以通过命令行配置:")
		fmt.Println("  clipcascade-desktop \\")
		fmt.Println("    --server http://localhost:8080 \\")
		fmt.Println("    --username admin \\")
		fmt.Println("    --password admin123 \\")
		fmt.Println("    --save")
		fmt.Println()
		fmt.Println("完整参数:")
		fmt.Println("  --server <url>                  服务器地址")
		fmt.Println("  --username <user>               登录用户名")
		fmt.Println("  --password <pass>               登录密码")
		fmt.Println("  --e2ee / --no-e2ee              启用/禁用端到端加密 (默认启用)")
		fmt.Println("  --p2p / --no-p2p                启用/禁用 P2P 传输 (默认启用)")
		fmt.Println("  --stun <url>                    STUN 服务器地址")
		fmt.Println("  --auto-reconnect / --no-auto-reconnect  启用/禁用自动重连 (默认启用)")
		fmt.Println("  --reconnect-delay <sec>         重连延迟秒数 (默认 5)")
		fmt.Println("  --file-memory-threshold-mib <n> 文件内存阈值 MiB (默认 1024, 最大 5120)")
		fmt.Println("  --web-port <port>               控制面板端口 (默认 6666)")
		fmt.Println("  --save                          保存参数到配置文件")
		fmt.Println("  --debug                         启用调试日志")
		fmt.Println()
		fmt.Println("也可以直接编辑配置文件:", cfgPath)
		fmt.Println()
		fmt.Println("💡 密码也可以通过环境变量注入: CLIPCASCADE_PASSWORD=xxx")
		fmt.Println()
		fmt.Printf("🌐 正在启动控制中心 (端口 %d)...\n", cfg.WebPort)
	}

	slog.Info("starting ClipCascade desktop client",
		"server", cfg.ServerURL,
		"user", cfg.Username,
		"e2ee", cfg.E2EEEnabled,
		"p2p", cfg.P2PEnabled,
		"file_memory_threshold_mib", cfg.FileMemoryThresholdMiB,
		"web_port", cfg.WebPort,
	)

	// 创建并运行 application (在通过 tray 退出前保持阻塞)
	application := app.New(cfg)
	application.Run()
}
