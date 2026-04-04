//go:build linux

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSimulateAutoPasteWaylandFallsBackToXdotoolWithDetailedLogs(t *testing.T) {
	restore := installAutoPasteTestHooks(t)
	defer restore()

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")

	var calls []string
	autoPasteLookPath = func(file string) (string, error) {
		switch file {
		case "ydotool":
			return "/usr/bin/ydotool", nil
		case "xdotool":
			return "/usr/bin/xdotool", nil
		default:
			return "", exec.ErrNotFound
		}
	}
	autoPasteRunOutput = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		calls = append(calls, path+" "+strings.Join(args, " "))
		if strings.Contains(path, "ydotool") {
			return []byte("uinput missing"), errors.New("exit status 1")
		}
		return []byte(""), nil
	}

	logs := captureAutoPasteLogs(t, func() {
		if err := simulateAutoPaste(); err != nil {
			t.Fatalf("simulateAutoPaste() error = %v", err)
		}
	})

	if len(calls) != 2 {
		t.Fatalf("calls = %v, want 2 commands", calls)
	}
	if !strings.Contains(calls[0], "/usr/bin/ydotool key 29:1 47:1 47:0 29:0") {
		t.Fatalf("first call = %q, want ydotool hotkey", calls[0])
	}
	if !strings.Contains(calls[1], "/usr/bin/xdotool key ctrl+v") {
		t.Fatalf("second call = %q, want xdotool fallback", calls[1])
	}
	for _, want := range []string{
		"session=wayland",
		"autopaste: trying ydotool",
		"autopaste: command failed",
		"tool=ydotool",
		"output=\"uinput missing\"",
		"autopaste: ydotool failed, falling back to xdotool",
		"autopaste: trying xdotool fallback",
		"autopaste: command succeeded",
		"tool=xdotool",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestSimulateAutoPasteX11LogsMissingXdotool(t *testing.T) {
	restore := installAutoPasteTestHooks(t)
	defer restore()

	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")

	autoPasteLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	logs := captureAutoPasteLogs(t, func() {
		err := simulateAutoPaste()
		if !errors.Is(err, ErrAutoPasteUnavailable) {
			t.Fatalf("simulateAutoPaste() error = %v, want %v", err, ErrAutoPasteUnavailable)
		}
	})

	for _, want := range []string{
		"session=x11",
		"autopaste: xdotool not available",
		"autopaste: unavailable on x11",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func TestSimulateAutoPasteWaylandLogsWhenBothToolsUnavailable(t *testing.T) {
	restore := installAutoPasteTestHooks(t)
	defer restore()

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")

	autoPasteLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	logs := captureAutoPasteLogs(t, func() {
		err := simulateAutoPaste()
		if !errors.Is(err, ErrAutoPasteUnavailable) {
			t.Fatalf("simulateAutoPaste() error = %v, want %v", err, ErrAutoPasteUnavailable)
		}
	})

	for _, want := range []string{
		"session=wayland",
		"autopaste: ydotool not available",
		"autopaste: xdotool not available",
		"autopaste: unavailable on wayland",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q\nlogs:\n%s", want, logs)
		}
	}
}

func installAutoPasteTestHooks(t *testing.T) func() {
	t.Helper()

	oldSleep := autoPasteSleep
	oldLookPath := autoPasteLookPath
	oldRunOutput := autoPasteRunOutput

	autoPasteSleep = func(_ time.Duration) {
	}

	return func() {
		autoPasteSleep = oldSleep
		autoPasteLookPath = oldLookPath
		autoPasteRunOutput = oldRunOutput
	}
}

func captureAutoPasteLogs(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)
	defer slog.SetDefault(previous)

	fn()
	return buf.String()
}
