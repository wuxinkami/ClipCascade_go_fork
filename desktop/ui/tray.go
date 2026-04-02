// Package ui 提供系统 tray 和通知功能。
package ui

import (
	"log/slog"

	"github.com/getlantern/systray"
)

// Tray 管理系统 tray 图标和菜单。
type Tray struct {
	onConnect              func()
	onDisconnect           func()
	onOpenHistory          func()
	onPastePlaceholder     func()
	onPasteRealContent     func()
	onSendCurrentClipboard func()
	onReadyHook            func()
	onQuit                 func()
	currentStatus          string
	replayEnabled          bool
	statusItem             *systray.MenuItem
	connectItem            *systray.MenuItem
	disconnectItem         *systray.MenuItem
	historyItem            *systray.MenuItem
	placeholderItem        *systray.MenuItem
	realPasteItem          *systray.MenuItem
	sendCurrentItem        *systray.MenuItem
}

// NewTray 创建一个新的系统 tray 管理器。
func NewTray() *Tray {
	return &Tray{
		currentStatus: "Disconnected / 未连接",
	}
}

// OnConnect 设置“Connect”菜单点击的回调。
func (t *Tray) OnConnect(fn func()) { t.onConnect = fn }

// OnDisconnect 设置“Disconnect”菜单点击的回调。
func (t *Tray) OnDisconnect(fn func()) { t.onDisconnect = fn }

// OnQuit 设置“Quit”菜单点击的回调。
func (t *Tray) OnQuit(fn func()) { t.onQuit = fn }

// OnOpenHistory 设置“Open Console”菜单点击的回调。
func (t *Tray) OnOpenHistory(fn func()) { t.onOpenHistory = fn }

// OnPastePlaceholder 设置“Paste Placeholder Path”菜单点击的回调。
func (t *Tray) OnPastePlaceholder(fn func()) { t.onPastePlaceholder = fn }

// OnPasteRealContent 设置“Paste Real Content”菜单点击的回调。
func (t *Tray) OnPasteRealContent(fn func()) { t.onPasteRealContent = fn }

// OnSendCurrentClipboard 设置“Send Current Clipboard”菜单点击的回调。
func (t *Tray) OnSendCurrentClipboard(fn func()) { t.onSendCurrentClipboard = fn }

// OnReady 设置 tray 菜单构建完成后的回调。
func (t *Tray) OnReady(fn func()) { t.onReadyHook = fn }

// Run 启动系统 tray。这在 tray 退出前保持阻塞。
// 在 macOS 上必须从 main goroutine 调用。
func (t *Tray) Run() {
	systray.Run(t.onReady, t.onExit)
}

// Quit 退出系统 tray。
func (t *Tray) Quit() {
	systray.Quit()
}

func (t *Tray) onReady() {
	if len(iconData) > 0 {
		systray.SetIcon(iconData) // 显示嵌入的图标图片
	}
	// systray.SetTitle("ClipCascade")
	// 在 macOS 上，如果同时设置了 Title 和 Icon，那么 Title 的纯文本会强制覆盖掉精美的图标！所以留空以显示 Logo
	systray.SetTooltip("ClipCascade - Clipboard Sync")

	t.statusItem = systray.AddMenuItem("Status: "+t.currentStatus, "")
	t.statusItem.Disable()

	systray.AddSeparator()

	t.connectItem = systray.AddMenuItem("Connect / 连接", "Connect to server")
	t.disconnectItem = systray.AddMenuItem("Disconnect / 断开", "Disconnect from server")
	t.historyItem = systray.AddMenuItem("Console / 控制中心", "Open local control center")
	t.placeholderItem = systray.AddMenuItem("Paste Local Placeholder / 粘贴占位符", "Paste the local placeholder path for the latest shared clipboard content")
	t.realPasteItem = systray.AddMenuItem("Paste Real Content / 粘贴真实文件", "Paste the local real files for the latest shared clipboard content")
	t.sendCurrentItem = systray.AddMenuItem("Send Current Clipboard / 发送当前剪贴板剪贴板", "Send current clipboard once")
	t.historyItem.Enable()
	t.sendCurrentItem.Enable()
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit / 退出", "Exit ClipCascade")

	t.SetStatus(t.currentStatus)
	t.SetReplayActionsEnabled(t.replayEnabled)

	if t.onReadyHook != nil {
		t.onReadyHook()
	}

	go func() {
		for {
			select {
			case <-t.connectItem.ClickedCh:
				if t.onConnect != nil {
					t.onConnect()
				}
			case <-t.disconnectItem.ClickedCh:
				if t.onDisconnect != nil {
					t.onDisconnect()
				}
			case <-t.historyItem.ClickedCh:
				if t.onOpenHistory != nil {
					t.onOpenHistory()
				}
			case <-t.placeholderItem.ClickedCh:
				if t.onPastePlaceholder != nil {
					t.onPastePlaceholder()
				}
			case <-t.realPasteItem.ClickedCh:
				if t.onPasteRealContent != nil {
					t.onPasteRealContent()
				}
			case <-t.sendCurrentItem.ClickedCh:
				if t.onSendCurrentClipboard != nil {
					t.onSendCurrentClipboard()
				}
			case <-quitItem.ClickedCh:
				if t.onQuit != nil {
					t.onQuit()
				}
				systray.Quit()
			}
		}
	}()
}

func (t *Tray) onExit() {
	slog.Info("tray: exiting")
}

// SetStatus 更新 tray 菜单中的状态显示。
func (t *Tray) SetStatus(status string) {
	t.currentStatus = status
	if t.statusItem != nil {
		t.statusItem.SetTitle("Status: " + status)
	}

	if t.connectItem == nil || t.disconnectItem == nil {
		return
	}

	switch status {
	case "Connected ✓ / 已连接 ✓", "Connected ✓":
		t.connectItem.Disable()
		t.disconnectItem.Enable()
	case "Connecting... / 连接中...", "Connecting...":
		t.connectItem.Disable()
		t.disconnectItem.Disable()
	case "Reconnecting... / 重连中...", "Reconnecting...":
		t.connectItem.Disable()
		t.disconnectItem.Disable()
	default:
		t.connectItem.Enable()
		t.disconnectItem.Disable()
	}
}

// SetReplayActiveEnabled 控制 Replay Active 菜单项是否可用。
func (t *Tray) SetReplayActionsEnabled(enabled bool) {
	t.replayEnabled = enabled
	if t.placeholderItem == nil || t.realPasteItem == nil {
		return
	}
	if enabled {
		t.placeholderItem.Enable()
		t.realPasteItem.Enable()
		return
	}
	t.placeholderItem.Disable()
	t.realPasteItem.Disable()
}

// Notify 记录通知信息到日志（桌面弹窗已禁用）。
func Notify(title, message string) {
	slog.Debug("notification (suppressed)", "title", title, "message", message)
}
