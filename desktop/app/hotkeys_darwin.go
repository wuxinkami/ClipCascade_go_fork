//go:build darwin

package app

/*
#cgo LDFLAGS: -framework Carbon
#include <Carbon/Carbon.h>

extern void clipcascadeDarwinHotkeyPressed(int32_t hotkeyID);

static OSStatus clipcascadeHotkeyHandler(EventHandlerCallRef nextHandler, EventRef event, void *userData) {
	EventHotKeyID hotKey;
	GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hotKey), NULL, &hotKey);
	clipcascadeDarwinHotkeyPressed((int32_t)hotKey.id);
	return noErr;
}

static OSStatus clipcascadeInstallHotkeyHandler(EventHandlerRef *outHandler) {
	EventTypeSpec eventType;
	eventType.eventClass = kEventClassKeyboard;
	eventType.eventKind = kEventHotKeyPressed;
	return InstallApplicationEventHandler(&clipcascadeHotkeyHandler, 1, &eventType, NULL, outHandler);
}

static OSStatus clipcascadeRegisterHotkey(uint32_t keyCode, uint32_t modifiers, uint32_t hotkeyID, EventHotKeyRef *outRef) {
	EventHotKeyID hkID;
	hkID.signature = 'CCAS';
	hkID.id = hotkeyID;
	return RegisterEventHotKey(keyCode, modifiers, hkID, GetApplicationEventTarget(), 0, outRef);
}

static void clipcascadeUnregisterHotkey(EventHotKeyRef ref) {
	if (ref != NULL) {
		UnregisterEventHotKey(ref);
	}
}

static void clipcascadeRemoveHotkeyHandler(EventHandlerRef handler) {
	if (handler != NULL) {
		RemoveEventHandler(handler);
	}
}
*/
import "C"

import (
	"fmt"
	"sync"
)

const (
	darwinHotkeySendCurrent = 1
	darwinHotkeyPlaceholder = 2
	darwinHotkeyRealPaste   = 3
)

var (
	darwinHotkeyRegistryMu sync.Mutex
	darwinHotkeyRegistry   *darwinHotkeyManager
)

type darwinHotkeyManager struct {
	mu       sync.Mutex
	running  bool
	handler  C.EventHandlerRef
	refs     map[int]C.EventHotKeyRef
	bindings hotkeyBindings
}

func newHotkeyManager() hotkeyManager {
	return &darwinHotkeyManager{
		refs: make(map[int]C.EventHotKeyRef),
	}
}

func (m *darwinHotkeyManager) Start(bindings hotkeyBindings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		m.bindings = bindings
		return nil
	}

	if status := C.clipcascadeInstallHotkeyHandler(&m.handler); status != C.noErr {
		return fmt.Errorf("install macOS hotkey handler: %d", int(status))
	}

	registered := []int{}
	register := func(id int, modifiers C.uint32_t, keyCode C.uint32_t, label string) error {
		var ref C.EventHotKeyRef
		if status := C.clipcascadeRegisterHotkey(keyCode, modifiers, C.uint32_t(id), &ref); status != C.noErr {
			return fmt.Errorf("register %s: %d", label, int(status))
		}
		m.refs[id] = ref
		registered = append(registered, id)
		return nil
	}

	if err := register(darwinHotkeySendCurrent, C.cmdKey|C.optionKey|C.shiftKey, C.kVK_ANSI_C, "Cmd+Shift+Option+C"); err != nil {
		m.cleanupLocked(registered)
		return err
	}
	if err := register(darwinHotkeyPlaceholder, C.cmdKey|C.optionKey, C.kVK_ANSI_V, "Cmd+Option+V"); err != nil {
		m.cleanupLocked(registered)
		return err
	}
	if err := register(darwinHotkeyRealPaste, C.cmdKey|C.optionKey|C.shiftKey, C.kVK_ANSI_V, "Cmd+Shift+Option+V"); err != nil {
		m.cleanupLocked(registered)
		return err
	}

	m.bindings = bindings
	m.running = true

	darwinHotkeyRegistryMu.Lock()
	darwinHotkeyRegistry = m
	darwinHotkeyRegistryMu.Unlock()
	return nil
}

func (m *darwinHotkeyManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return nil
	}
	registered := []int{darwinHotkeySendCurrent, darwinHotkeyPlaceholder, darwinHotkeyRealPaste}
	m.cleanupLocked(registered)
	m.bindings = hotkeyBindings{}
	m.running = false

	darwinHotkeyRegistryMu.Lock()
	if darwinHotkeyRegistry == m {
		darwinHotkeyRegistry = nil
	}
	darwinHotkeyRegistryMu.Unlock()
	return nil
}

func (m *darwinHotkeyManager) cleanupLocked(ids []int) {
	for _, id := range ids {
		if ref, ok := m.refs[id]; ok {
			C.clipcascadeUnregisterHotkey(ref)
			delete(m.refs, id)
		}
	}
	if m.handler != nil {
		C.clipcascadeRemoveHotkeyHandler(m.handler)
		m.handler = nil
	}
}

//export clipcascadeDarwinHotkeyPressed
func clipcascadeDarwinHotkeyPressed(hotkeyID C.int32_t) {
	darwinHotkeyRegistryMu.Lock()
	manager := darwinHotkeyRegistry
	darwinHotkeyRegistryMu.Unlock()
	if manager == nil {
		return
	}

	manager.mu.Lock()
	bindings := manager.bindings
	manager.mu.Unlock()

	switch int(hotkeyID) {
	case darwinHotkeySendCurrent:
		if bindings.sendCurrentClipboard != nil {
			go bindings.sendCurrentClipboard()
		}
	case darwinHotkeyPlaceholder:
		if bindings.pastePlaceholder != nil {
			go bindings.pastePlaceholder()
		}
	case darwinHotkeyRealPaste:
		if bindings.pasteRealContent != nil {
			go bindings.pasteRealContent()
		}
	}
}
