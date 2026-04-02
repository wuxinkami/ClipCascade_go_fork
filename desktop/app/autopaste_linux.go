//go:build linux

package app

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

const autoPasteWait = 150 * time.Millisecond
const execTimeout = 500 * time.Millisecond

func simulateAutoPaste() error {
	time.Sleep(autoPasteWait)

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		// Wayland 环境优先尝试 ydotool
		if path, err := exec.LookPath("ydotool"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
			defer cancel()
			if err := exec.CommandContext(ctx, path, "key", "29:1", "47:1", "47:0", "29:0").Run(); err == nil {
				return nil
			}
			slog.Debug("autopaste: ydotool 执行失败或超时，尝试 xdotool fallback")
		}
		// ydotool 不可用时通过 XWayland 兼容层使用 xdotool
		if path, err := exec.LookPath("xdotool"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
			defer cancel()
			if err := exec.CommandContext(ctx, path, "key", "ctrl+v").Run(); err == nil {
				return nil
			}
		}
		return ErrAutoPasteUnavailable
	}

	// X11 环境使用 xdotool 发送 Ctrl+V。
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "xdotool", "key", "ctrl+v").Run(); err != nil {
		return ErrAutoPasteUnavailable
	}
	return nil
}

