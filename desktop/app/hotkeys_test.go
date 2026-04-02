package app

import "testing"

func TestNewHotkeyManagerReturnsImplementation(t *testing.T) {
	manager := newHotkeyManager()
	if manager == nil {
		t.Fatal("newHotkeyManager() returned nil")
	}
}
