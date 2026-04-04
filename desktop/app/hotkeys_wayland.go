//go:build linux

package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalBusName                         = "org.freedesktop.portal.Desktop"
	portalObjectPath                      = "/org/freedesktop/portal/desktop"
	portalGlobalShortcutsInterface        = "org.freedesktop.portal.GlobalShortcuts"
	portalRequestInterface                = "org.freedesktop.portal.Request"
	portalSessionInterface                = "org.freedesktop.portal.Session"
	portalRequestResponseSignal           = "org.freedesktop.portal.Request.Response"
	portalActivatedSignal                 = "org.freedesktop.portal.GlobalShortcuts.Activated"
	portalResponseSuccess          uint32 = 0
)

type waylandPortalHotkeys struct {
	mu            sync.RWMutex
	conn          *dbus.Conn
	signals       chan *dbus.Signal
	done          chan struct{}
	closeOnce     sync.Once
	sessionHandle dbus.ObjectPath
	bindings      map[string]func()
}

type portalShortcutSpec struct {
	ID         string
	Properties map[string]dbus.Variant
}

func isWaylandHotkeyEnvironment() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

func newWaylandPortalHotkeys(bindings hotkeyBindings) (*waylandPortalHotkeys, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus for Wayland hotkeys: %w", err)
	}

	portal := &waylandPortalHotkeys{
		conn:    conn,
		signals: make(chan *dbus.Signal, 32),
		done:    make(chan struct{}),
		bindings: map[string]func(){
			"send_current":      bindings.sendCurrentClipboard,
			"paste_placeholder": bindings.pastePlaceholder,
			"paste_real":        bindings.pasteRealContent,
		},
	}
	conn.Signal(portal.signals)

	if err := portal.conn.AddMatchSignal(
		dbus.WithMatchObjectPath(dbus.ObjectPath(portalObjectPath)),
		dbus.WithMatchInterface(portalGlobalShortcutsInterface),
		dbus.WithMatchMember("Activated"),
	); err != nil {
		portal.Close()
		return nil, fmt.Errorf("watch Wayland Activated signal: %w", err)
	}

	sessionHandle, err := portal.createSession()
	if err != nil {
		portal.Close()
		return nil, err
	}
	portal.sessionHandle = sessionHandle

	if err := portal.bindShortcuts(); err != nil {
		portal.Close()
		return nil, err
	}

	return portal, nil
}

func (m *waylandPortalHotkeys) Run() {
	m.mu.RLock()
	signals := m.signals
	done := m.done
	m.mu.RUnlock()
	if signals == nil {
		return
	}

	for {
		select {
		case <-done:
			return
		case signal, ok := <-signals:
			if !ok {
				return
			}
			if signal == nil || signal.Name != portalActivatedSignal || len(signal.Body) < 2 {
				continue
			}
			m.mu.RLock()
			sessionHandle := m.sessionHandle
			handlerMap := m.bindings
			m.mu.RUnlock()
			sessionPath, ok := signal.Body[0].(dbus.ObjectPath)
			if !ok || sessionPath != sessionHandle {
				continue
			}
			shortcutID, ok := signal.Body[1].(string)
			if !ok || shortcutID == "" {
				continue
			}
			if handler := handlerMap[shortcutID]; handler != nil {
				go handler()
			}
		}
	}
}

func (m *waylandPortalHotkeys) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		conn := m.conn
		signals := m.signals
		sessionHandle := m.sessionHandle
		done := m.done
		m.conn = nil
		m.mu.Unlock()

		if done != nil {
			close(done)
		}
		if conn != nil && sessionHandle != "" {
			_ = conn.Object(portalBusName, sessionHandle).Call(portalSessionInterface+".Close", 0).Err
		}
		if conn != nil {
			if signals != nil {
				conn.RemoveSignal(signals)
			}
			_ = conn.Close()
		}
	})
}

func (m *waylandPortalHotkeys) createSession() (dbus.ObjectPath, error) {
	requestHandle, err := m.callPortalRequest(
		portalGlobalShortcutsInterface+".CreateSession",
		map[string]dbus.Variant{
			"handle_token":         dbus.MakeVariant(portalToken("clipcascade_create")),
			"session_handle_token": dbus.MakeVariant(portalToken("clipcascade_session")),
		},
	)
	if err != nil {
		return "", err
	}

	response, err := m.waitForPortalResponse(requestHandle, 10*time.Second)
	if err != nil {
		return "", err
	}
	sessionRaw, ok := response["session_handle"]
	if !ok {
		return "", errors.New("Wayland GlobalShortcuts response missing session_handle")
	}
	sessionPath, ok := sessionRaw.Value().(dbus.ObjectPath)
	if ok {
		return sessionPath, nil
	}
	sessionText, ok := sessionRaw.Value().(string)
	if !ok || sessionText == "" {
		return "", errors.New("Wayland GlobalShortcuts returned invalid session_handle")
	}
	return dbus.ObjectPath(sessionText), nil
}

func (m *waylandPortalHotkeys) bindShortcuts() error {
	specs := []portalShortcutSpec{
		{
			ID: "send_current",
			Properties: map[string]dbus.Variant{
				"description":       dbus.MakeVariant("发送当前剪贴板"),
				"preferred_trigger": dbus.MakeVariant("<Ctrl><Alt><Shift>c"),
			},
		},
		{
			ID: "paste_placeholder",
			Properties: map[string]dbus.Variant{
				"description":       dbus.MakeVariant("粘贴占位路径"),
				"preferred_trigger": dbus.MakeVariant("<Ctrl><Alt>v"),
			},
		},
		{
			ID: "paste_real",
			Properties: map[string]dbus.Variant{
				"description":       dbus.MakeVariant("粘贴真实内容"),
				"preferred_trigger": dbus.MakeVariant("<Ctrl><Alt><Shift>v"),
			},
		},
	}

	requestHandle, err := m.callPortalRequest(
		portalGlobalShortcutsInterface+".BindShortcuts",
		m.sessionHandle,
		specs,
		"",
		map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(portalToken("clipcascade_bind")),
		},
	)
	if err != nil {
		return err
	}
	// 用户需要在 portal 弹窗中确认快捷键设置，给予充足的等待时间
	results, err := m.waitForPortalResponse(requestHandle, 10*time.Minute)
	if err != nil {
		return err
	}

	// 检查 portal 返回的快捷键是否有有效 trigger。
	// 如果用户之前在弹窗中全部设为"无"，portal 缓存了无绑定 session，
	// 此时应返回错误以触发 X11 降级。
	boundCount := 0
	if shortcutsVar, ok := results["shortcuts"]; ok {
		boundCount = countBoundTriggers(shortcutsVar)
	} else {
		slog.Warn("Wayland GlobalShortcuts: BindShortcuts 响应中没有 shortcuts 字段", "results_keys", fmt.Sprintf("%v", mapKeys(results)))
	}
	if boundCount == 0 {
		return errors.New("Wayland GlobalShortcuts: 所有快捷键都未绑定 trigger，降级到 X11")
	}
	slog.Info("Wayland GlobalShortcuts: 快捷键已绑定", "有效绑定数", boundCount)
	return nil
}

// countBoundTriggers 从 portal BindShortcuts 返回的 shortcuts variant 中
// 统计有效绑定的快捷键数量。D-Bus 签名为 a(sa{sv})。
func countBoundTriggers(v dbus.Variant) int {
	count := 0
	// godbus 可能将 a(sa{sv}) 解析为多种 Go 类型
	switch shortcuts := v.Value().(type) {
	case [][]interface{}:
		for _, s := range shortcuts {
			if hasTrigger(s) {
				count++
			}
		}
	case []interface{}:
		// 可能是扁平的 struct 数组
		for _, item := range shortcuts {
			if s, ok := item.([]interface{}); ok && hasTrigger(s) {
				count++
			}
		}
	default:
		slog.Warn("Wayland GlobalShortcuts: shortcuts 类型无法识别", "type", fmt.Sprintf("%T", v.Value()))
	}
	return count
}

func hasTrigger(s []interface{}) bool {
	if len(s) < 2 {
		return false
	}
	props, ok := s[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}
	triggerVar, ok := props["trigger_description"]
	if !ok {
		return false
	}
	trigger, ok := triggerVar.Value().(string)
	return ok && trigger != ""
}

func mapKeys(m map[string]dbus.Variant) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (m *waylandPortalHotkeys) callPortalRequest(method string, args ...any) (dbus.ObjectPath, error) {
	call := m.conn.Object(portalBusName, dbus.ObjectPath(portalObjectPath)).Call(method, 0, args...)
	if call.Err != nil {
		return "", fmt.Errorf("Wayland GlobalShortcuts call %s failed: %w", method, call.Err)
	}
	if len(call.Body) == 0 {
		return "", fmt.Errorf("Wayland GlobalShortcuts call %s returned no request handle", method)
	}
	requestHandle, ok := call.Body[0].(dbus.ObjectPath)
	if !ok || requestHandle == "" {
		return "", fmt.Errorf("Wayland GlobalShortcuts call %s returned invalid request handle", method)
	}
	return requestHandle, nil
}

func (m *waylandPortalHotkeys) waitForPortalResponse(requestHandle dbus.ObjectPath, timeout time.Duration) (map[string]dbus.Variant, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case signal := <-m.signals:
			if signal == nil || signal.Path != requestHandle || signal.Name != portalRequestResponseSignal || len(signal.Body) < 2 {
				continue
			}
			responseCode, ok := signal.Body[0].(uint32)
			if !ok {
				return nil, errors.New("invalid portal response code")
			}
			results, ok := signal.Body[1].(map[string]dbus.Variant)
			if !ok {
				return nil, errors.New("invalid portal response payload")
			}
			if responseCode != portalResponseSuccess {
				return nil, fmt.Errorf("Wayland GlobalShortcuts request failed with response code %d", responseCode)
			}
			return results, nil
		case <-timer.C:
			return nil, fmt.Errorf("timeout waiting for Wayland GlobalShortcuts response")
		}
	}
}

func portalToken(prefix string) string {
	prefix = strings.ReplaceAll(prefix, "-", "_")
	return prefix
}
