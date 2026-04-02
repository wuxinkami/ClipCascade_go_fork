//go:build windows

package app

import (
	"time"

	"golang.org/x/sys/windows"
)

var (
	user32KeybdEvent = windows.NewLazySystemDLL("user32.dll").NewProc("keybd_event")
)

const (
	vkControl     = 0x11
	vkShift       = 0x10
	vkMenu        = 0x12
	keyeventfUp   = 0x0002
	autoPasteWait = 300 * time.Millisecond
)

func simulateAutoPaste() error {
	time.Sleep(autoPasteWait)
	user32KeybdEvent.Call(vkShift, 0, keyeventfUp, 0)
	user32KeybdEvent.Call(vkMenu, 0, keyeventfUp, 0)
	user32KeybdEvent.Call(vkControl, 0, 0, 0)
	user32KeybdEvent.Call(vkV, 0, 0, 0)
	user32KeybdEvent.Call(vkV, 0, keyeventfUp, 0)
	user32KeybdEvent.Call(vkControl, 0, keyeventfUp, 0)
	return nil
}
