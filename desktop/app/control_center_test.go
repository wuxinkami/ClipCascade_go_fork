package app

import (
	"testing"

	"github.com/clipcascade/desktop/config"
)

func TestHistoryPanelDevicesIncludesLocalDeviceWithoutPeers(t *testing.T) {
	app := &Application{
		cfg:       &config.Config{Username: "admin"},
		sessionID: "session-local",
	}

	devices := app.historyPanelDevices()

	if devices.Local == nil {
		t.Fatal("Local device is nil, want local device entry")
	}
	if devices.Local.DeviceName != "admin" {
		t.Fatalf("local device name = %q, want %q", devices.Local.DeviceName, "admin")
	}
	if devices.Local.SessionID != "session-local" {
		t.Fatalf("local session id = %q, want %q", devices.Local.SessionID, "session-local")
	}
	if !devices.Local.Local || !devices.Local.Ready {
		t.Fatalf("local flags = local:%v ready:%v, want true/true", devices.Local.Local, devices.Local.Ready)
	}
	if len(devices.Peers) != 0 {
		t.Fatalf("peers = %d, want 0", len(devices.Peers))
	}
}
