//go:build darwin

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	pkgcrypto "github.com/clipcascade/pkg/crypto"
)

const darwinAppleScriptTimeout = 2 * time.Second

// getPlatformChangeCount 使用 AppleScript 获取 macOS 剪贴板的 change count。
func getPlatformChangeCount() int64 {
	ctx, cancel := context.WithTimeout(context.Background(), darwinAppleScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-e", "return (get the clipboard info)")
	out, err := cmd.Output()
	if err != nil {
		return time.Now().UnixNano() // 降级方案
	}
	// 利用输出的哈希值作为计数器，或者从详细 info 中解析（此处简单处理）
	return int64(pkgcrypto.XXHash64(string(out)))
}

// getPlatformFilePaths 尝试获取 macOS 剪贴板中的物理文件路径。
func getPlatformFilePaths() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), darwinAppleScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(
		ctx,
		"osascript",
		"-e", "on run",
		"-e", "try",
		"-e", "set clipValue to the clipboard",
		"-e", "set lines to {}",
		"-e", "if class of clipValue is list then",
		"-e", "repeat with oneItem in clipValue",
		"-e", "try",
		"-e", "set end of lines to (POSIX path of oneItem)",
		"-e", "end try",
		"-e", "end repeat",
		"-e", "else",
		"-e", "try",
		"-e", "set end of lines to (POSIX path of clipValue)",
		"-e", "end try",
		"-e", "end if",
		"-e", "set oldDelims to AppleScript's text item delimiters",
		"-e", "set AppleScript's text item delimiters to linefeed",
		"-e", "set joined to lines as text",
		"-e", "set AppleScript's text item delimiters to oldDelims",
		"-e", "return joined",
		"-e", "on error",
		"-e", "return \"\"",
		"-e", "end try",
		"-e", "end run",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}

	paths := strings.Split(raw, "\n")
	var validPaths []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			// 严格磁盘校验
			if _, err := os.Stat(p); err == nil {
				validPaths = append(validPaths, p)
			}
		}
	}

	return validPaths, nil
}

// getPlatformImageData macOS 直接使用 golang.design/x/clipboard
func getPlatformImageData() []byte { return nil }

// getPlatformTextData macOS 直接使用 golang.design/x/clipboard
func getPlatformTextData() []byte { return nil }

// isWayland macOS 平台不存在 Wayland，始终返回 false。
func isWayland() bool { return false }

// setPlatformText macOS 平台不需要特殊处理，返回降级错误。
func setPlatformText(_ string) error { return errNotWayland }

func setPlatformImage(_ []byte) error { return errNotWayland }

var errNotWayland = errors.New("not wayland")

func setPlatformFilePaths(paths []string) error {
	normalized := normalizeClipboardPaths(paths)
	if len(normalized) == 0 {
		return nil
	}
	slog.Info("剪贴板：通过 osascript 将文件路径写入 macOS 剪贴板...", "文件数", len(normalized))
	args := []string{
		"-e", "on run argv",
		"-e", "set fileList to {}",
		"-e", "repeat with onePath in argv",
		"-e", "set end of fileList to (POSIX file onePath)",
		"-e", "end repeat",
		"-e", "set the clipboard to fileList",
		"-e", "end run",
	}
	args = append(args, normalized...)
	ctx, cancel := context.WithTimeout(context.Background(), darwinAppleScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", args...)

	err := cmd.Run()
	if err != nil {
		slog.Warn("剪贴板：无法设置 macOS 剪贴板文件路径", "错误", err)
	}
	return err
}
