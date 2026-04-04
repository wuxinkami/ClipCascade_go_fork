// Package app 管理 desktop client 生命周期：login、connect、监控、reconnect。
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/clipcascade/pkg/constants"
	pkgcrypto "github.com/clipcascade/pkg/crypto"
	"github.com/clipcascade/pkg/protocol"
	"github.com/clipcascade/pkg/sizefmt"

	"github.com/clipcascade/desktop/clipboard"
	"github.com/clipcascade/desktop/config"
	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/desktop/transport"
	"github.com/clipcascade/desktop/ui"
	"github.com/google/uuid"
	"github.com/grandcat/zeroconf"
)

// Application 是主要的 desktop client 控制器。
type Application struct {
	cfg          *config.Config
	httpClient   *http.Client
	stomp        *transport.StompClient
	p2p          *transport.P2PClient
	clip         *clipboard.Manager
	history      *history.Manager
	historyPanel *historyPanelServer
	hotkeys      hotkeyManager
	tray         *ui.Tray
	logFilePath  string
	sessionID    string
	transfers    *transferManager
	ctx          context.Context
	cancel       context.CancelFunc
	encKey       []byte // 从 password 派生的 AES-256-GCM 密钥
	reconnecting bool
	connecting   bool // 防止用户重复点击连接产生并发泄漏
	connMu       sync.Mutex

	clipboardInitOnce sync.Once
	clipboardInitErr  error

	lastRecvMu   sync.Mutex
	lastRecvHash string
	lastRecvTime time.Time

	sharedClipboard sharedClipboardState

	controlEventMu sync.Mutex
	controlEvents  []historyPanelEvent

	imageMaterializeMu   sync.Mutex
	imageMaterializeJobs map[string]*imageMaterializeJob
}

var appPasteClipboardPayload = func(a *Application, payload string, payloadType string, fileName string) {
	if a == nil || a.clip == nil {
		return
	}
	a.clip.Paste(payload, payloadType, fileName)
}

type clipboardWriteReason string

const (
	clipboardWriteReasonIncomingAuto   clipboardWriteReason = "incoming_auto"
	clipboardWriteReasonIncomingLegacy clipboardWriteReason = "incoming_legacy"
	clipboardWriteReasonReplayText     clipboardWriteReason = "replay_text"
	clipboardWriteReasonReplayPath     clipboardWriteReason = "replay_path_placeholder"
	clipboardWriteReasonReplayReal     clipboardWriteReason = "replay_real_content"
)

var appLocalUsableIPv4Networks = localUsableIPv4Networks

// New 创建一个新的 Application 实例。
func New(cfg *config.Config) *Application {
	ctx, cancel := context.WithCancel(context.Background())
	jar, _ := cookiejar.New(nil)

	app := &Application{
		cfg:         cfg,
		clip:        clipboard.NewManager(),
		history:     history.NewManager(0),
		hotkeys:     newHotkeyManager(),
		tray:        ui.NewTray(),
		logFilePath: config.LogFilePath(),
		sessionID:   uuid.NewString(),
		ctx:         ctx,
		cancel:      cancel,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 15 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 不要跟随重定向
			},
		},
	}
	app.transfers = newTransferManager(app.sessionID)
	app.historyPanel = newHistoryPanelServer(app.history, func(id string, mode ReplayMode) error {
		if id != "" && !app.history.SetActive(id) {
			return ErrHistoryItemNotFound
		}
		_, err := app.ReplayActiveHistoryItem(mode)
		return err
	})
	app.historyPanel.SetSendCurrent(func() error { return appSendCurrentClipboard(app) })
	app.historyPanel.SetConnect(func() { go app.connect() })
	app.historyPanel.SetDisconnect(func() { app.disconnect() })
	app.historyPanel.SetOverviewProvider(func() historyPanelOverview { return app.historyPanelOverview() })
	app.historyPanel.SetDevicesProvider(func() historyPanelDeviceSnapshot { return app.historyPanelDevices() })
	app.historyPanel.SetSettingsProvider(func() historyPanelSettings { return app.historyPanelSettings() })
	app.historyPanel.SetSettingsSaver(func(input historyPanelSettingsInput) (historyPanelSettings, error) {
		return app.saveHistoryPanelSettings(input)
	})
	app.historyPanel.SetFileTransfersProvider(func() []historyPanelFileTransfer { return app.historyPanelFileTransfers() })
	app.historyPanel.SetEventsProvider(func() []historyPanelEvent { return app.historyPanelEvents() })
	app.historyPanel.SetNeedsSetup(func() bool {
		return app.cfg.Username == "" || app.cfg.Password == ""
	})
	app.historyPanel.SetWebPasswordProvider(func() string {
		return app.cfg.WebPassword
	})
	app.historyPanel.SetWebPasswordSetter(func(password string) error {
		app.cfg.WebPassword = password
		return app.cfg.Save()
	})
	app.clip.SetNotifier(ui.Notify)
	return app
}

// Run 启动 application。在 macOS 上必须从 main goroutine 调用。
func (a *Application) Run() {
	cleanupExpiredTransferTempDirs(fileTransferTempRetention)
	// 启动即清理 24 小时前的接收临时文件，避免长期累积。
	a.clip.CleanupExpiredTempFiles()

	if err := a.ensureClipboardReady(); err != nil {
		slog.Error("clipboard init failed", "error", err)
		ui.Notify("ClipCascade", "Clipboard initialization failed / 剪贴板初始化失败: "+err.Error())
	} else {
		a.clip.OnCopy(func(capture *clipboard.CaptureData) {
			a.handleLocalClipboardCapture(capture)
		})
		go a.clip.Watch(a.ctx)
	}

	// 设置 tray 回调
	a.tray.OnConnect(func() {
		go a.connect()
	})
	a.tray.OnDisconnect(func() {
		a.disconnect()
	})
	a.tray.OnOpenHistory(func() { go a.triggerOpenHistoryPanel() })
	a.tray.OnOpenLogWindow(func() { go a.triggerOpenLogWindow() })
	a.tray.OnReady(func() {
		a.refreshReplayActiveAvailability()
	})
	a.tray.OnPastePlaceholder(func() { go a.triggerPastePlaceholderHistoryItem() })
	a.tray.OnPasteRealContent(func() { go a.triggerPasteRealContentHistoryItem() })
	a.tray.OnSendCurrentClipboard(func() { go a.triggerSendCurrentClipboard() })
	a.tray.OnQuit(func() {
		a.shutdown()
	})
	a.history.OnChanged(func() {
		a.refreshReplayActiveAvailability()
	})
	a.refreshReplayActiveAvailability()

	if err := a.hotkeys.Start(hotkeyBindings{
		sendCurrentClipboard: a.triggerSendCurrentClipboard,
		pastePlaceholder:     a.triggerPastePlaceholderHistoryItem,
		pasteRealContent:     a.triggerPasteRealContentHistoryItem,
	}); err != nil {
		slog.Error("hotkey init failed", "error", err)
		ui.Notify("ClipCascade", "Global hotkeys unavailable / 全局快捷键不可用: "+err.Error())
	}

	// 如果配置了凭据，则自动连接
	if a.cfg.Username != "" && a.cfg.Password != "" {
		go a.connect()
	} else {
		// 首次未配置凭据：自动打开控制中心引导用户完成设置
		slog.Info("应用：未检测到凭据配置，将打开控制中心引导设置")
		ui.Notify("ClipCascade", "首次使用，请在浏览器中配置连接信息 / First time? Please configure in browser")
		go a.triggerOpenHistoryPanel()
	}

	// Run tray (blocks until quit)
	a.tray.Run()
}

// discoverServer 尝试在局域网中发现所有可用的 ClipCascade 服务器。
func (a *Application) discoverServer() ([]string, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()

	err = resolver.Browse(ctx, "_clipcascade._tcp", "local.", entries)
	if err != nil {
		return nil, err
	}

	var foundURLs []string
	seen := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			if len(foundURLs) > 0 {
				return foundURLs, nil
			}
			return nil, fmt.Errorf("server discovery timed out")
		case entry := <-entries:
			if entry != nil {
				// 收集该服务条目下的所有 IPv4 地址
				for _, ip := range entry.AddrIPv4 {
					if !isUsableDiscoveredIPv4(ip) {
						continue
					}
					url := fmt.Sprintf("http://%s:%d", ip, entry.Port)
					if !seen[url] {
						foundURLs = append(foundURLs, url)
						seen[url] = true
						slog.Debug("应用：发现潜在服务器地址", "地址", url)
					}
				}
			}
		}
	}
}

func isUsableDiscoveredIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip = ip.To4()
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, network := range appLocalUsableIPv4Networks() {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func localUsableIPv4Networks() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	networks := make([]*net.IPNet, 0)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		if strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") ||
			strings.Contains(name, "podman") ||
			strings.Contains(name, "veth") ||
			strings.Contains(name, "virbr") ||
			strings.Contains(name, "zt") ||
			strings.Contains(name, "tun") ||
			strings.Contains(name, "tap") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet == nil {
				continue
			}
			if ipNet.IP == nil || ipNet.IP.To4() == nil {
				continue
			}
			networks = append(networks, &net.IPNet{
				IP:   ipNet.IP.Mask(ipNet.Mask),
				Mask: ipNet.Mask,
			})
		}
	}
	return networks
}

// connect 执行 login → 获取加密密钥 → 启动 WebSocket → 开始剪贴板监控。
func (a *Application) connect() {
	a.connMu.Lock()
	if a.connecting || (a.stomp != nil && a.stomp.IsConnected()) {
		a.connMu.Unlock()
		slog.Info("应用：已在连接中或已连接，忽略重复请求")
		return
	}
	a.connecting = true
	a.connMu.Unlock()

	defer func() {
		a.connMu.Lock()
		a.connecting = false
		a.connMu.Unlock()
	}()

	a.recordControlEvent("connect", "Connect requested")
	a.tray.SetStatus("Connecting... / 连接中...")

	// 确定待尝试的服务器列表
	var urlsToTry []string
	if a.cfg.ServerURL != "" {
		a.cfg.ServerURL = config.NormalizeServerURL(a.cfg.ServerURL)
		urlsToTry = append(urlsToTry, a.cfg.ServerURL)
	}

	// 如果未配置、使用的是 localhost、或显式要求发现，则搜索局域网
	if len(urlsToTry) == 0 || strings.Contains(urlsToTry[0], "localhost") {
		slog.Info("应用：启动局域网自动发现...")
		discovered, err := a.discoverServer()
		if err == nil {
			urlsToTry = append(urlsToTry, discovered...)
		}
	}

	var lastErr error
	var successfulURL string
	var cookies []*http.Cookie

	// 依次尝试所有地址
	for _, targetURL := range urlsToTry {
		targetURL = config.NormalizeServerURL(targetURL)
		a.cfg.ServerURL = targetURL
		slog.Info("应用：正在尝试登录服务器", "URL", targetURL)

		cookies, lastErr = a.login()
		if lastErr == nil {
			successfulURL = targetURL
			break
		}
		slog.Warn("应用：尝试服务器失败", "URL", targetURL, "错误", lastErr)
	}

	// 如果所有预设地址都失败了，最后再做一次全网大搜索尝试补救
	if successfulURL == "" {
		slog.Info("应用：初次尝试均失败，执行深度补救发现...")
		discovered, dErr := a.discoverServer()
		if dErr == nil {
			slog.Info("应用：局域网发现结果", "候选地址数", len(discovered), "列表", discovered)
			for _, targetURL := range discovered {
				targetURL = config.NormalizeServerURL(targetURL)
				// 跳过已经试过失败的
				retry := true
				for _, tried := range urlsToTry {
					if tried == targetURL {
						retry = false
						break
					}
				}
				if !retry {
					continue
				}

				a.cfg.ServerURL = targetURL
				slog.Info("应用：正在尝试发现的备选地址", "URL", targetURL)
				cookies, lastErr = a.login()
				if lastErr == nil {
					successfulURL = targetURL
					break
				}
			}
		}
	}

	if successfulURL == "" {
		slog.Error("登录完全失败，请检查服务器状态或配置", "错误", lastErr)
		if lastErr != nil {
			a.recordControlEvent("connect", "Connect failed: "+lastErr.Error())
		} else {
			a.recordControlEvent("connect", "Connect failed")
		}
		a.tray.SetStatus("Login Failed / 登录失败")
		ui.Notify("ClipCascade", "Failed to connect to any server / 无法连接到任何服务器")
		return
	}

	successfulURL = config.NormalizeServerURL(successfulURL)
	slog.Info("应用：服务器连接成功", "最终URL", successfulURL)
	a.cfg.ServerURL = successfulURL
	if err := a.cfg.SaveServerURLOnly(successfulURL); err != nil {
		slog.Warn("应用：保存最近可用服务器地址失败", "错误", err)
	}

	// 步骤 2: 获取用于加密密钥派生的 user 信息技巧。
	if a.cfg.E2EEEnabled {
		if err := a.deriveEncryptionKey(); err != nil {
			slog.Error("密钥派生失败", "错误", err)
			a.tray.SetStatus("Key Error / 密钥错误")
			return
		}
	}

	// Step 3: Connect WebSocket STOMP
	a.stomp = transport.NewStompClient(a.cfg.ServerURL, cookies)
	a.stomp.OnMessage(a.onReceive)

	if err := a.stomp.Connect(); err != nil {
		slog.Error("WebSocket 连接失败", "错误", err)
		a.recordControlEvent("connect", "WebSocket connect failed: "+err.Error())
		a.tray.SetStatus("WS Failed / WS 失败")
		ui.Notify("ClipCascade", "WebSocket connection failed / WebSocket 连接失败")
		go a.reconnectLoop()
		return
	}

	// Step 4: Connect P2P if enabled
	if a.cfg.P2PEnabled {
		stunURL := constants.DefaultStunURL
		if a.cfg.StunURL != "" {
			stunURL = a.cfg.StunURL
		}
		a.p2p = transport.NewP2PClient(a.cfg.ServerURL, cookies, stunURL)
		a.p2p.OnReceive(a.onReceive)
		if err := a.p2p.Connect(); err != nil {
			slog.Warn("应用：P2P 连接失败", "错误", err)
			a.recordControlEvent("p2p", "P2P signaling failed: "+err.Error())
		}
	}

	a.tray.SetStatus("Connected ✓ / 已连接 ✓")
	ui.Notify("ClipCascade", "Connected to server as / 已连接到服务器, 用户: "+a.cfg.Username)
	slog.Info("应用：已连接")
	a.recordControlEvent("connect", "Connected to server")

	// Start reconnect monitor
	go a.monitorConnection()
}

// login 执行基于 HTTP 表单的 login 并返回 session cookies。
func (a *Application) login() ([]*http.Cookie, error) {
	a.cfg.ServerURL = config.NormalizeServerURL(a.cfg.ServerURL)
	if a.cfg.ServerURL == "" {
		return nil, fmt.Errorf("empty server URL")
	}
	loginURL := a.cfg.ServerURL + "/login"

	form := url.Values{
		"username": {a.cfg.Username},
		"password": {a.cfg.Password},
	}

	resp, err := a.httpClient.PostForm(loginURL, form)
	if err != nil {
		return nil, fmt.Errorf("POST /login: %w", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	// Check for redirect to "/" (success) vs "/login?error" (failure)
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther {
		location := resp.Header.Get("Location")
		if strings.Contains(location, "error") {
			return nil, fmt.Errorf("invalid credentials")
		}
	}

	// Extract cookies from jar
	u, _ := url.Parse(a.cfg.ServerURL)
	cookies := a.httpClient.Jar.Cookies(u)

	if len(cookies) == 0 {
		return nil, fmt.Errorf("no session cookie received")
	}

	slog.Info("应用：已登录", "用户名", a.cfg.Username, "Cookie 数量", len(cookies))
	return cookies, nil
}

// deriveEncryptionKey 从 server 获取 user 信息并在本地派生 AES 密钥。
func (a *Application) deriveEncryptionKey() error {
	resp, err := a.httpClient.Get(a.cfg.ServerURL + "/api/user-info")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var info struct {
		Salt       string `json:"salt"`
		HashRounds int    `json:"hash_rounds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return err
	}

	a.encKey = pkgcrypto.DeriveKey(a.cfg.Password, a.cfg.Username, info.Salt, info.HashRounds)
	slog.Info("应用：加密密钥已派生", "循环次数", info.HashRounds)
	return nil
}

// onReceive 当从 server 接收到剪贴板消息时被调用。
func (a *Application) onReceive(body string) {
	var clipData *protocol.ClipboardData

	if a.cfg.E2EEEnabled && a.encKey != nil {
		// Decrypt
		encrypted, err := pkgcrypto.DecodeFromJSONString(body)
		if err != nil {
			slog.Warn("解密解析失败", "错误", err)
			return
		}
		plaintext, err := pkgcrypto.Decrypt(a.encKey, encrypted)
		if err != nil {
			slog.Warn("解密失败", "错误", err)
			return
		}
		clipData, err = protocol.DecodeClipboardData(plaintext)
		if err != nil {
			slog.Warn("剪贴板数据解码失败", "错误", err)
			return
		}
	} else {
		var err error
		clipData, err = protocol.DecodeClipboardData([]byte(body))
		if err != nil {
			slog.Warn("剪贴板数据解码失败", "错误", err)
			return
		}
	}

	// 在解密之后做去重：E2EE 每次加密使用不同 nonce，
	// 基于密文的 hash 无法去重同一明文的多次发送。
	// 这里用 Type+Payload 的 hash 做短窗口去重，
	// 防止 P2P/STOMP 双通道并发、以及用户快速重复发送。
	a.lastRecvMu.Lock()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(clipData.Type+clipData.Payload)))
	now := time.Now()
	if a.lastRecvHash == hash && now.Sub(a.lastRecvTime) < 500*time.Millisecond {
		a.lastRecvMu.Unlock()
		slog.Debug("应用：静默丢弃重复荷载（解密后去重，窗口 500ms）")
		return
	}
	a.lastRecvHash = hash
	a.lastRecvTime = now
	a.lastRecvMu.Unlock()

	if handled, err := a.handleFileTransferMessage(clipData); handled {
		if err != nil {
			slog.Warn("应用：处理文件传输消息失败", "type", clipData.Type, "error", err)
		}
		return
	}

	action := a.admitReceivedClipboardData(clipData, time.Now())
	switch action {
	case receiveActionAdmitHistory:
		attrs := []any{
			"类型", clipData.Type,
			"大小", sizefmt.HumanSizeFromPayload(clipData.Type, clipData.Payload),
		}
		if names := clipboardLogNames(clipData); names != "" {
			attrs = append(attrs, "文件", names)
		} else if clipData.Type == constants.TypeImage {
			if clipData.FileName != "" {
				attrs = append(attrs, "文件", clipData.FileName)
			} else {
				attrs = append(attrs, "文件", "[截图]")
			}
		}
		slog.Info("应用：收到剪贴板更新并已加入历史", attrs...)
		// 文本和图片都需要在“异机接收”场景立即写入系统剪贴板。
		// 自发自收场景（来源会话与本机会话一致）只入历史，不二次写回。
		if a.shouldWriteReceivedClipboardToSystem(clipData) {
			appPasteClipboardPayload(a, clipData.Payload, clipData.Type, clipData.FileName)
		}
		// 图片临时文件仍按延迟物化：用户按 Ctrl+Alt+V / Ctrl+Alt+Shift+V 时才落盘。
		// 用户按 Ctrl+Alt+V / Ctrl+Alt+Shift+V 时才物化到 /tmp。
		return
	case receiveActionLegacyPaste:
		attrs := []any{
			"类型", clipData.Type,
			"大小", sizefmt.HumanSizeFromPayload(clipData.Type, clipData.Payload),
		}
		if names := clipboardLogNames(clipData); names != "" {
			attrs = append(attrs, "文件", names)
		} else if clipData.Type == constants.TypeImage {
			attrs = append(attrs, "文件", "[截图]")
		}
		slog.Info("应用：收到旧版剪贴板更新，继续走兼容直贴路径", attrs...)
		if a.writeClipboardPayloadIfAllowed(clipboardWriteReasonIncomingLegacy, clipData) {
			slog.Debug("应用：已通过兼容路径接收并粘贴", "类型", clipData.Type, "大小", len(clipData.Payload))
		}
		return
	default:
		slog.Warn("应用：收到不支持的剪贴板类型，已忽略", "类型", clipData.Type)
		return
	}
}

func (a *Application) shouldWriteReceivedClipboardToSystem(clipData *protocol.ClipboardData) bool {
	return a.isClipboardWriteAllowed(clipboardWriteReasonIncomingAuto, clipData)
}

func (a *Application) isClipboardWriteAllowed(reason clipboardWriteReason, clipData *protocol.ClipboardData) bool {
	if a == nil || a.clip == nil || clipData == nil {
		return false
	}

	switch reason {
	case clipboardWriteReasonIncomingAuto, clipboardWriteReasonIncomingLegacy:
		if clipData.Type != constants.TypeText && clipData.Type != constants.TypeImage {
			return false
		}
		sourceSessionID := clipboardSourceSessionID(clipData)
		if sourceSessionID == "" {
			// 兼容旧客户端：未携带来源会话时，按异机消息处理。
			return true
		}
		return sourceSessionID != a.appSessionID()
	case clipboardWriteReasonReplayText, clipboardWriteReasonReplayPath, clipboardWriteReasonReplayReal:
		return true
	default:
		return false
	}
}

func (a *Application) writeClipboardPayloadIfAllowed(reason clipboardWriteReason, clipData *protocol.ClipboardData) bool {
	if !a.isClipboardWriteAllowed(reason, clipData) {
		if clipData != nil {
			slog.Debug("应用：统一守卫阻止系统剪贴板写入",
				"reason", reason,
				"type", clipData.Type,
				"source_session_id", clipboardSourceSessionID(clipData),
			)
		}
		return false
	}
	appPasteClipboardPayload(a, clipData.Payload, clipData.Type, clipData.FileName)
	return true
}

func (a *Application) handleLocalClipboardCapture(capture *clipboard.CaptureData) {
	if a == nil || capture == nil {
		return
	}
	if err := a.sendCapture(capture); err != nil {
		if errors.Is(err, ErrClipboardTransportUnavailable) {
			slog.Debug("application: clipboard capture skipped because no transport is ready", "type", capture.Type)
			return
		}
		slog.Warn("application: auto-send clipboard capture failed", "type", capture.Type, "error", err)
	}
}

// monitorConnection 检查连接健康状况并触发重连。
func (a *Application) monitorConnection() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if a.stomp != nil && !a.stomp.IsConnected() && !a.isReconnecting() {
				slog.Warn("应用：连接丢失，正在触发重连")
				go a.reconnectLoop()
			}
		}
	}
}

// reconnectLoop tries to reconnect with exponential backoff and jitter.
func (a *Application) reconnectLoop() {
	if !a.cfg.AutoReconnect {
		return
	}
	if !a.beginReconnect() {
		return
	}
	defer a.endReconnect()

	a.recordControlEvent("connect", "Reconnect loop started")
	a.tray.SetStatus("Reconnecting... / 重连中...")

	delay := time.Duration(a.cfg.ReconnectDelay) * time.Second
	if delay == 0 {
		delay = time.Duration(constants.DefaultReconnectDelay) * time.Second
	}
	maxDelay := time.Duration(constants.MaxReconnectDelay) * time.Second

	failCount := 0
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(delay):
			failCount++
			slog.Info("应用：正在尝试重连", "延迟", delay, "失败次数", failCount)

			// 如果连续失败多次，或者间隔已经拉得很大，尝试重新发现服务器
			if failCount%3 == 0 {
				slog.Info("应用：重连多次失败，尝试局域网重新探测服务器列表...")
				discovered, err := a.discoverServer()
				if err == nil && len(discovered) > 0 {
					// 这里只需标记可能需要尝试新地址，后续 connect() 会处理 urlsToTry
					slog.Info("应用：探测到局域网活跃服务器候选", "数量", len(discovered))
				}
			}

			a.connect()
			if a.stomp != nil && a.stomp.IsConnected() {
				return // reconnected
			}
			delay = min(delay*2, maxDelay)
		}
	}
}

func (a *Application) beginReconnect() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.reconnecting {
		return false
	}
	a.reconnecting = true
	return true
}

func (a *Application) endReconnect() {
	a.connMu.Lock()
	a.reconnecting = false
	a.connMu.Unlock()
}

func (a *Application) isReconnecting() bool {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	return a.reconnecting
}

// disconnect disconnects from the server.
func (a *Application) disconnect() {
	a.connMu.Lock()
	stomp := a.stomp
	p2p := a.p2p
	a.stomp = nil
	a.p2p = nil
	a.connMu.Unlock()

	if stomp != nil {
		stomp.Close()
	}
	if p2p != nil {
		p2p.Close()
	}
	a.tray.SetStatus("Disconnected / 未连接")
	ui.Notify("ClipCascade", "Disconnected from server / 已从服务器断开连接")
	slog.Info("应用：已断开连接")
	a.recordControlEvent("connect", "Disconnected")
}

// shutdown cleanly shuts down the application.
func (a *Application) shutdown() {
	if a.hotkeys != nil {
		if err := a.hotkeys.Stop(); err != nil {
			slog.Warn("application: failed to stop global hotkeys", "error", err)
		}
	}
	a.disconnect()
	if a.historyPanel != nil {
		if err := a.historyPanel.Close(); err != nil {
			slog.Warn("应用：关闭历史面板服务失败", "error", err)
		}
	}
	a.cancel()
	slog.Info("应用：正在关闭")
}
