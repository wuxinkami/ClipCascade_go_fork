//go:build linux

package app

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestLinuxHotkeyManagerUsesWaylandPortalWhenAvailable(t *testing.T) {
	origIsWayland := linuxHotkeyIsWaylandEnvironment
	origNewPortal := linuxHotkeyNewWaylandPortal
	t.Cleanup(func() {
		linuxHotkeyIsWaylandEnvironment = origIsWayland
		linuxHotkeyNewWaylandPortal = origNewPortal
	})

	linuxHotkeyIsWaylandEnvironment = func() bool { return true }

	created := 0
	linuxHotkeyNewWaylandPortal = func(bindings hotkeyBindings) (*waylandPortalHotkeys, error) {
		created++
		return &waylandPortalHotkeys{
			signals:  make(chan *dbus.Signal),
			done:     make(chan struct{}),
			bindings: map[string]func(){},
		}, nil
	}

	manager, ok := newHotkeyManager().(*linuxHotkeyManager)
	if !ok {
		t.Fatalf("newHotkeyManager() type = %T, want *linuxHotkeyManager", newHotkeyManager())
	}

	if err := manager.Start(hotkeyBindings{}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}

	manager.mu.Lock()
	portal := manager.portal
	manager.mu.Unlock()
	if portal == nil {
		t.Fatal("manager.portal = nil, want portal backend")
	}

	if err := manager.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
