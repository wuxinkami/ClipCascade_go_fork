//go:build windows

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32Console           = windows.NewLazySystemDLL("kernel32.dll")
	user32Console             = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleProcessList = kernel32Console.NewProc("GetConsoleProcessList")
	procGetConsoleWindow      = kernel32Console.NewProc("GetConsoleWindow")
	procShowWindow            = user32Console.NewProc("ShowWindow")
)

const (
	swHide = 0
)

func supportsLogWindow() bool {
	return true
}

// HideStartupConsoleWindow 仅在“应用拥有自己的独立控制台”时将其隐藏。
// 如果当前进程是从现有 cmd/powershell 启动的，共享控制台进程数会大于 1，此时不隐藏，避免把用户自己的终端一起藏掉。
func HideStartupConsoleWindow() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	count, err := consoleProcessCount()
	if err != nil || count > 1 {
		return
	}
	procShowWindow.Call(hwnd, swHide)
}

func consoleProcessCount() (uint32, error) {
	processIDs := make([]uint32, 8)
	ret, _, callErr := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&processIDs[0])),
		uintptr(len(processIDs)),
	)
	if ret == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, fmt.Errorf("GetConsoleProcessList returned 0")
	}
	return uint32(ret), nil
}

// OpenLogWindow 使用独立 PowerShell 控制台实时查看日志文件。
// 日志窗口只是查看器，关闭它不会影响主进程。
func OpenLogWindow(logPath string) error {
	if strings.TrimSpace(logPath) == "" {
		return fmt.Errorf("log path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_ = file.Close()

	escapedPath := strings.ReplaceAll(logPath, "'", "''")
	command := fmt.Sprintf("$Host.UI.RawUI.WindowTitle='ClipCascade Logs'; $path='%s'; Get-Content -Path $path -Tail 200 -Wait -Encoding UTF8", escapedPath)
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoExit", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	return cmd.Start()
}
