//go:build !windows

package ui

func supportsLogWindow() bool {
	return false
}

func HideStartupConsoleWindow() {
}

func OpenLogWindow(logPath string) error {
	return nil
}
