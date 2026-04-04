//go:build windows

package app

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32SendInput        = windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")
	user32GetAsyncKeyState = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")
)

const (
	inputKeyboard              = 1
	keyeventfKeyUp             = 0x0002
	autoPasteSettleDelay       = 40 * time.Millisecond
	autoPastePoll              = 10 * time.Millisecond
	autoPasteKeyReleaseTimeout = 1500 * time.Millisecond
	vkControlPaste             = 0x11
	vkVPaste                   = 0x56
	vkShiftPaste               = 0x10
	vkMenuPaste                = 0x12
	vkLShiftPaste              = 0xA0
	vkRShiftPaste              = 0xA1
	vkLControlPaste            = 0xA2
	vkRControlPaste            = 0xA3
	vkLMenuPaste               = 0xA4
	vkRMenuPaste               = 0xA5
)

// keybdInput 对应 Windows KEYBDINPUT 结构体。
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// inputUnion 对应 Windows INPUT 结构体。
type inputUnion struct {
	inputType uint32
	ki        keybdInput
	padding   [8]byte // 确保对齐到 INPUT 结构体大小
}

// simulateAutoPaste 使用 SendInput 模拟 Ctrl+V 按键。
// SendInput 比 keybd_event 更可靠，能通过 UIPI 检查并正确注入到前景窗口。
func simulateAutoPaste() error {
	if err := waitForAutoPasteHotkeyRelease(); err != nil {
		return err
	}
	time.Sleep(autoPasteSettleDelay)

	// 模拟 Ctrl+V
	pasteInputs := []inputUnion{
		makeKeyInput(vkControlPaste, 0),              // Ctrl 按下
		makeKeyInput(vkVPaste, 0),                    // V 按下
		makeKeyInput(vkVPaste, keyeventfKeyUp),       // V 释放
		makeKeyInput(vkControlPaste, keyeventfKeyUp), // Ctrl 释放
	}
	sent := sendInputs(pasteInputs)
	if sent != uint32(len(pasteInputs)) {
		return fmt.Errorf("SendInput: expected %d, sent %d", len(pasteInputs), sent)
	}
	return nil
}

func waitForAutoPasteHotkeyRelease() error {
	deadline := time.Now().Add(autoPasteKeyReleaseTimeout)
	for time.Now().Before(deadline) {
		if !isVirtualKeyPressed(vkControlPaste) &&
			!isVirtualKeyPressed(vkLControlPaste) &&
			!isVirtualKeyPressed(vkRControlPaste) &&
			!isVirtualKeyPressed(vkShiftPaste) &&
			!isVirtualKeyPressed(vkLShiftPaste) &&
			!isVirtualKeyPressed(vkRShiftPaste) &&
			!isVirtualKeyPressed(vkMenuPaste) &&
			!isVirtualKeyPressed(vkLMenuPaste) &&
			!isVirtualKeyPressed(vkRMenuPaste) &&
			!isVirtualKeyPressed(vkVPaste) {
			return nil
		}
		time.Sleep(autoPastePoll)
	}
	return fmt.Errorf("hotkey keys still pressed after %s", autoPasteKeyReleaseTimeout)
}

func isVirtualKeyPressed(vk uint16) bool {
	ret, _, _ := user32GetAsyncKeyState.Call(uintptr(vk))
	return uint16(ret)&0x8000 != 0
}

func makeKeyInput(vk uint16, flags uint32) inputUnion {
	return inputUnion{
		inputType: inputKeyboard,
		ki: keybdInput{
			wVk:     vk,
			dwFlags: flags,
		},
	}
}

func sendInputs(inputs []inputUnion) uint32 {
	if len(inputs) == 0 {
		return 0
	}
	inputSize := unsafe.Sizeof(inputs[0])
	ret, _, _ := user32SendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		uintptr(inputSize),
	)
	return uint32(ret)
}
