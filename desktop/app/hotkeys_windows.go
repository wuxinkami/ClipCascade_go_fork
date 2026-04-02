//go:build windows

package app

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32RegisterHotKey   = windows.NewLazySystemDLL("user32.dll").NewProc("RegisterHotKey")
	user32UnregisterHotKey = windows.NewLazySystemDLL("user32.dll").NewProc("UnregisterHotKey")
	user32GetMessageW      = windows.NewLazySystemDLL("user32.dll").NewProc("GetMessageW")
	user32PostThreadMsgW   = windows.NewLazySystemDLL("user32.dll").NewProc("PostThreadMessageW")
	kernel32GetCurrentTID  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	wmHotkey = 0x0312
	wmQuit   = 0x0012

	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004

	vkC = 0x43
	vkV = 0x56

	hotkeySendCurrent = 1
	hotkeyPlaceholder = 2
	hotkeyRealPaste   = 3
)

type point struct {
	x int32
	y int32
}

type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

type windowsHotkeyManager struct {
	mu       sync.Mutex
	running  bool
	threadID uint32
	done     chan struct{}
}

func newHotkeyManager() hotkeyManager {
	return &windowsHotkeyManager{}
}

func (m *windowsHotkeyManager) Start(bindings hotkeyBindings) error {
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
		m.done = nil
		m.threadID = 0
		m.mu.Unlock()
		return err
	}

	return nil
}

func (m *windowsHotkeyManager) run(bindings hotkeyBindings, started chan<- error, done chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	threadID, _, _ := kernel32GetCurrentTID.Call()
	registered := []int{hotkeySendCurrent, hotkeyPlaceholder, hotkeyRealPaste}

	if err := registerHotkey(hotkeySendCurrent, modControl|modAlt|modShift, vkC); err != nil {
		started <- fmt.Errorf("register Ctrl+Alt+Shift+C: %w", err)
		close(done)
		return
	}
	if err := registerHotkey(hotkeyPlaceholder, modControl|modAlt, vkV); err != nil {
		unregisterHotkeys(registered[:1])
		started <- fmt.Errorf("register Ctrl+Alt+V: %w", err)
		close(done)
		return
	}
	if err := registerHotkey(hotkeyRealPaste, modControl|modAlt|modShift, vkV); err != nil {
		unregisterHotkeys(registered[:2])
		started <- fmt.Errorf("register Ctrl+Alt+Shift+V: %w", err)
		close(done)
		return
	}
	defer unregisterHotkeys(registered)

	m.mu.Lock()
	m.threadID = uint32(threadID)
	m.mu.Unlock()
	started <- nil

	var message msg
	for {
		ret, _, callErr := user32GetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			slog.Warn("hotkeys: GetMessageW failed", "error", callErr)
			close(done)
			return
		case 0:
			close(done)
			return
		}

		if message.message != wmHotkey {
			continue
		}

		switch int(message.wParam) {
		case hotkeySendCurrent:
			if bindings.sendCurrentClipboard != nil {
				go bindings.sendCurrentClipboard()
			}
		case hotkeyPlaceholder:
			if bindings.pastePlaceholder != nil {
				go bindings.pastePlaceholder()
			}
		case hotkeyRealPaste:
			if bindings.pasteRealContent != nil {
				go bindings.pasteRealContent()
			}
		}
	}
}

func (m *windowsHotkeyManager) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	threadID := m.threadID
	done := m.done
	m.running = false
	m.threadID = 0
	m.done = nil
	m.mu.Unlock()

	if threadID != 0 {
		ret, _, err := user32PostThreadMsgW.Call(uintptr(threadID), wmQuit, 0, 0)
		if ret == 0 {
			return fmt.Errorf("post WM_QUIT: %w", err)
		}
	}

	if done != nil {
		<-done
	}

	return nil
}

func registerHotkey(id int, modifiers uint32, key uint32) error {
	ret, _, err := user32RegisterHotKey.Call(0, uintptr(id), uintptr(modifiers), uintptr(key))
	if ret != 0 {
		return nil
	}
	if err == windows.ERROR_HOTKEY_ALREADY_REGISTERED {
		return err
	}
	if errno, ok := err.(syscall.Errno); ok && errno != 0 {
		return err
	}
	return syscall.EINVAL
}

func unregisterHotkeys(ids []int) {
	for _, id := range ids {
		user32UnregisterHotKey.Call(0, uintptr(id))
	}
}
