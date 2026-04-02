package ui

import "testing"

func TestNewTrayDefaultsToDisconnectedStatus(t *testing.T) {
	tray := NewTray()
	if tray.currentStatus != "Disconnected / 未连接" {
		t.Fatalf("currentStatus = %q, want %q", tray.currentStatus, "Disconnected / 未连接")
	}
}

func TestSetStatusStoresLatestStatusBeforeTrayReady(t *testing.T) {
	tray := NewTray()
	tray.SetStatus("Connected ✓")
	if tray.currentStatus != "Connected ✓" {
		t.Fatalf("currentStatus = %q, want %q", tray.currentStatus, "Connected ✓")
	}
}

func TestSetReplayActionsEnabledStoresLatestValueBeforeTrayReady(t *testing.T) {
	tray := NewTray()
	tray.SetReplayActionsEnabled(true)
	if !tray.replayEnabled {
		t.Fatal("replayEnabled = false, want true")
	}
}
