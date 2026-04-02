//go:build linux

package app

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const (
	x11KeysymUpperC   xproto.Keysym = 0x0043
	x11KeysymLowerC   xproto.Keysym = 0x0063
	x11KeysymUpperV   xproto.Keysym = 0x0056
	x11KeysymLowerV   xproto.Keysym = 0x0076
	x11KeysymNumLock  xproto.Keysym = 0xFF7F
	x11BaseModifiers                = uint16(xproto.ModMaskControl | xproto.ModMask1)
	x11ShiftModifiers               = uint16(x11BaseModifiers | xproto.ModMaskShift)
)

type linuxHotkeyManager struct {
	mu      sync.Mutex
	running bool
	conn    *xgb.Conn
	portal  *waylandPortalHotkeys
	done    chan struct{}
}

type x11HotkeySpec struct {
	name      string
	keycode   xproto.Keycode
	modifiers uint16
	handler   func()
}

type x11Grab struct {
	keycode   xproto.Keycode
	modifiers uint16
}

func newHotkeyManager() hotkeyManager {
	return &linuxHotkeyManager{}
}

func (m *linuxHotkeyManager) Start(bindings hotkeyBindings) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	started := make(chan error, 1)
	m.done = done
	m.running = true
	m.mu.Unlock()

	go m.run(bindings, started, done)

	if err := <-started; err != nil {
		m.mu.Lock()
		m.running = false
		m.conn = nil
		m.done = nil
		m.mu.Unlock()
		return err
	}

	return nil
}

func (m *linuxHotkeyManager) run(bindings hotkeyBindings, started chan<- error, done chan struct{}) {
	defer close(done)

	// 注意：Wayland GlobalShortcuts portal 的 BindShortcuts 每次调用都会弹出
	// 用户确认弹窗（XDG portal 安全模型设计），导致每次启动都需要用户手动确认。
	// 在 portal 支持静默绑定之前，统一使用 X11 grab（通过 XWayland 兼容 Wayland）。
	// if isWaylandHotkeyEnvironment() {
	// 	portal, err := newWaylandPortalHotkeys(bindings)
	// 	if err == nil {
	// 		m.mu.Lock()
	// 		m.portal = portal
	// 		m.mu.Unlock()
	// 		started <- nil
	// 		portal.Run()
	// 		return
	// 	}
	// 	slog.Warn("Wayland GlobalShortcuts portal failed, falling back to X11 grab", "error", err)
	// }

	conn, err := xgb.NewConn()
	if err != nil {
		started <- fmt.Errorf("connect X11 display: %w", err)
		return
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	if setup == nil {
		started <- fmt.Errorf("resolve X11 setup: unavailable")
		return
	}
	screen := setup.DefaultScreen(conn)
	if screen == nil {
		started <- fmt.Errorf("resolve X11 default screen: unavailable")
		return
	}

	keyC, err := lookupKeycode(conn, setup, x11KeysymLowerC, x11KeysymUpperC)
	if err != nil {
		started <- fmt.Errorf("resolve Ctrl+Alt+Shift+C keycode: %w", err)
		return
	}
	keyV, err := lookupKeycode(conn, setup, x11KeysymLowerV, x11KeysymUpperV)
	if err != nil {
		started <- fmt.Errorf("resolve Ctrl+Alt+V keycode: %w", err)
		return
	}
	numLockMask, err := lookupModifierMask(conn, setup, x11KeysymNumLock)
	if err != nil {
		started <- fmt.Errorf("resolve NumLock modifier: %w", err)
		return
	}

	specs := []x11HotkeySpec{
		{
			name:      "Ctrl+Alt+Shift+C",
			keycode:   keyC,
			modifiers: x11ShiftModifiers,
			handler:   bindings.sendCurrentClipboard,
		},
		{
			name:      "Ctrl+Alt+V",
			keycode:   keyV,
			modifiers: x11BaseModifiers,
			handler:   bindings.pastePlaceholder,
		},
		{
			name:      "Ctrl+Alt+Shift+V",
			keycode:   keyV,
			modifiers: x11ShiftModifiers,
			handler:   bindings.pasteRealContent,
		},
	}

	grabs := make([]x11Grab, 0, len(specs)*4)
	for _, spec := range specs {
		registered, grabErr := grabKeyVariants(conn, screen.Root, spec.keycode, spec.modifiers, numLockMask)
		if grabErr != nil {
			ungrabKeys(conn, screen.Root, grabs)
			started <- fmt.Errorf("register %s: %w", spec.name, grabErr)
			return
		}
		grabs = append(grabs, registered...)
	}
	defer ungrabKeys(conn, screen.Root, grabs)

	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	slog.Info("hotkeys: X11 hotkeys registered", "grabs", len(grabs))
	started <- nil

	for {
		event, eventErr := conn.WaitForEvent()
		if event == nil && eventErr == nil {
			slog.Info("hotkeys: X11 connection closed")
			return
		}
		if eventErr != nil {
			slog.Warn("hotkeys: X11 event error", "error", eventErr)
			continue
		}

		switch keyPress := event.(type) {
		case xproto.KeyPressEvent:
			dispatchX11Hotkey(specs, numLockMask, keyPress)
		case *xproto.KeyPressEvent:
			if keyPress != nil {
				dispatchX11Hotkey(specs, numLockMask, *keyPress)
			}
		}
	}
}

func (m *linuxHotkeyManager) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	conn := m.conn
	portal := m.portal
	done := m.done
	m.running = false
	m.conn = nil
	m.portal = nil
	m.done = nil
	m.mu.Unlock()

	if portal != nil {
		portal.Close()
	}
	if conn != nil {
		conn.Close()
	}
	if done != nil {
		<-done
	}

	slog.Info("hotkeys: X11 hotkeys stopped")
	return nil
}

func dispatchX11Hotkey(specs []x11HotkeySpec, numLockMask uint16, event xproto.KeyPressEvent) {
	normalizedState := normalizeModifierState(event.State, numLockMask)

	for _, spec := range specs {
		if event.Detail != spec.keycode || normalizedState != spec.modifiers {
			continue
		}
		if spec.handler != nil {
			go spec.handler()
		}
		return
	}
}

func grabKeyVariants(conn *xgb.Conn, root xproto.Window, keycode xproto.Keycode, baseModifiers uint16, numLockMask uint16) ([]x11Grab, error) {
	var grabs []x11Grab

	for _, modifiers := range expandModifierVariants(baseModifiers, numLockMask) {
		if err := xproto.GrabKeyChecked(
			conn,
			false,
			root,
			modifiers,
			keycode,
			xproto.GrabModeAsync,
			xproto.GrabModeAsync,
		).Check(); err != nil {
			ungrabKeys(conn, root, grabs)
			return nil, err
		}

		grabs = append(grabs, x11Grab{
			keycode:   keycode,
			modifiers: modifiers,
		})
	}

	return grabs, nil
}

func ungrabKeys(conn *xgb.Conn, root xproto.Window, grabs []x11Grab) {
	for _, grab := range grabs {
		_ = xproto.UngrabKeyChecked(conn, grab.keycode, root, grab.modifiers).Check()
	}
}

func expandModifierVariants(baseModifiers uint16, numLockMask uint16) []uint16 {
	candidates := []uint16{
		baseModifiers,
		baseModifiers | uint16(xproto.ModMaskLock),
		baseModifiers | numLockMask,
		baseModifiers | numLockMask | uint16(xproto.ModMaskLock),
	}

	seen := make(map[uint16]struct{}, len(candidates))
	result := make([]uint16, 0, len(candidates))
	for _, modifiers := range candidates {
		if _, ok := seen[modifiers]; ok {
			continue
		}
		seen[modifiers] = struct{}{}
		result = append(result, modifiers)
	}
	return result
}

func normalizeModifierState(state uint16, numLockMask uint16) uint16 {
	return state &^ (uint16(xproto.ModMaskLock) | numLockMask)
}

func lookupKeycode(conn *xgb.Conn, setup *xproto.SetupInfo, primary xproto.Keysym, secondary xproto.Keysym) (xproto.Keycode, error) {
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	reply, err := xproto.GetKeyboardMapping(conn, xproto.Keycode(setup.MinKeycode), count).Reply()
	if err != nil {
		return 0, err
	}

	perKeycode := int(reply.KeysymsPerKeycode)
	for offset := 0; offset < int(count); offset++ {
		base := offset * perKeycode
		for _, keysym := range reply.Keysyms[base : base+perKeycode] {
			if keysym == primary || keysym == secondary {
				return xproto.Keycode(uint8(setup.MinKeycode) + uint8(offset)), nil
			}
		}
	}

	return 0, fmt.Errorf("keysym %#x/%#x not found", uint32(primary), uint32(secondary))
}

func lookupModifierMask(conn *xgb.Conn, setup *xproto.SetupInfo, keysym xproto.Keysym) (uint16, error) {
	keycode, err := lookupKeycode(conn, setup, keysym, keysym)
	if err != nil {
		// 某些键盘布局没有绑定 NumLock，这种情况下退化为只处理 CapsLock。
		return 0, nil
	}

	reply, err := xproto.GetModifierMapping(conn).Reply()
	if err != nil {
		return 0, err
	}

	perModifier := int(reply.KeycodesPerModifier)
	for modifierIndex := 0; modifierIndex < 8; modifierIndex++ {
		base := modifierIndex * perModifier
		for _, modifierKeycode := range reply.Keycodes[base : base+perModifier] {
			if modifierKeycode == 0 || modifierKeycode != keycode {
				continue
			}
			return modifierMaskForIndex(modifierIndex), nil
		}
	}

	// 某些键盘布局没有绑定 NumLock，这种情况下退化为只处理 CapsLock。
	return 0, nil
}

func modifierMaskForIndex(index int) uint16 {
	switch index {
	case xproto.MapIndexShift:
		return uint16(xproto.ModMaskShift)
	case xproto.MapIndexLock:
		return uint16(xproto.ModMaskLock)
	case xproto.MapIndexControl:
		return uint16(xproto.ModMaskControl)
	case xproto.MapIndex1:
		return uint16(xproto.ModMask1)
	case xproto.MapIndex2:
		return uint16(xproto.ModMask2)
	case xproto.MapIndex3:
		return uint16(xproto.ModMask3)
	case xproto.MapIndex4:
		return uint16(xproto.ModMask4)
	case xproto.MapIndex5:
		return uint16(xproto.ModMask5)
	default:
		return 0
	}
}
