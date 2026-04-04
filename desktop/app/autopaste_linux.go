//go:build linux

package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

const autoPasteWait = 150 * time.Millisecond
const autoPasteExecTimeout = 500 * time.Millisecond

var (
	autoPasteSleep     = time.Sleep
	autoPasteLookPath  = exec.LookPath
	autoPasteGetenv    = os.Getenv
	autoPasteRunOutput = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, path, args...).CombinedOutput()
	}
)

func simulateAutoPaste() error {
	autoPasteSleep(autoPasteWait)

	session := autoPasteSession()
	slog.Debug("autopaste: start",
		"session", session,
		"wayland_display", autoPasteGetenv("WAYLAND_DISPLAY"),
		"display", autoPasteGetenv("DISPLAY"),
	)

	if session == "wayland" {
		return simulateWaylandAutoPaste()
	}
	return simulateX11AutoPaste()
}

func simulateWaylandAutoPaste() error {
	if path, err := autoPasteLookPath("ydotool"); err == nil {
		slog.Debug("autopaste: trying ydotool", "session", "wayland", "path", path)
		if err := runAutoPasteCommand("ydotool", path, "key", "29:1", "47:1", "47:0", "29:0"); err == nil {
			return nil
		}
		slog.Debug("autopaste: ydotool failed, falling back to xdotool")
	} else {
		slog.Debug("autopaste: ydotool not available", "session", "wayland", "error", err)
	}

	if path, err := autoPasteLookPath("xdotool"); err == nil {
		slog.Debug("autopaste: trying xdotool fallback", "session", "wayland", "path", path)
		if err := runAutoPasteCommand("xdotool", path, "key", "ctrl+v"); err == nil {
			return nil
		}
	} else {
		slog.Debug("autopaste: xdotool not available", "session", "wayland", "error", err)
	}

	slog.Warn("autopaste: unavailable on wayland", "session", "wayland")
	return ErrAutoPasteUnavailable
}

func simulateX11AutoPaste() error {
	path, err := autoPasteLookPath("xdotool")
	if err != nil {
		slog.Debug("autopaste: xdotool not available", "session", "x11", "error", err)
		slog.Warn("autopaste: unavailable on x11", "session", "x11")
		return ErrAutoPasteUnavailable
	}

	slog.Debug("autopaste: trying xdotool", "session", "x11", "path", path)
	if err := runAutoPasteCommand("xdotool", path, "key", "ctrl+v"); err != nil {
		slog.Warn("autopaste: unavailable on x11", "session", "x11")
		return ErrAutoPasteUnavailable
	}
	return nil
}

func autoPasteSession() string {
	if autoPasteGetenv("WAYLAND_DISPLAY") != "" {
		return "wayland"
	}
	return "x11"
}

func runAutoPasteCommand(tool string, path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), autoPasteExecTimeout)
	defer cancel()

	output, err := autoPasteRunOutput(ctx, path, args...)
	outputText := summarizeAutoPasteOutput(output)
	if err != nil {
		attrs := []any{
			"tool", tool,
			"path", path,
			"args", strings.Join(args, " "),
			"timed_out", errors.Is(ctx.Err(), context.DeadlineExceeded),
			"error", err,
		}
		if outputText != "" {
			attrs = append(attrs, "output", outputText)
		}
		slog.Warn("autopaste: command failed", attrs...)
		return err
	}

	attrs := []any{
		"tool", tool,
		"path", path,
		"args", strings.Join(args, " "),
	}
	if outputText != "" {
		attrs = append(attrs, "output", outputText)
	}
	slog.Debug("autopaste: command succeeded", attrs...)
	return nil
}

func summarizeAutoPasteOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " | ")
	text = strings.ReplaceAll(text, "\r", " ")
	const maxLen = 240
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
