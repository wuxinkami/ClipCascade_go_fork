//go:build !windows && !linux && !darwin

package app

type noopHotkeyManager struct{}

func newHotkeyManager() hotkeyManager {
	return &noopHotkeyManager{}
}

func (m *noopHotkeyManager) Start(bindings hotkeyBindings) error {
	return nil
}

func (m *noopHotkeyManager) Stop() error {
	return nil
}
