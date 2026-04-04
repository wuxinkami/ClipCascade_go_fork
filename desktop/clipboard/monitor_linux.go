//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"

	pkgcrypto "github.com/clipcascade/pkg/crypto"
)

var (
	warnWaylandMissingPasteTypesOnce sync.Once
	warnWaylandMissingPasteReadOnce  sync.Once
	warnWaylandMissingCopyOnce       sync.Once
	warnX11MissingXclipOnce          sync.Once
	execClipboardCommand             = exec.Command
)

func isWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// getPlatformChangeCount 对于 Linux 零 CGO 方案，较难直接获取 SequenceNumber，
// 我们通过读取 xclip 的元数据或直接由 Watch 内部状态管理。
func getPlatformChangeCount() int64 {
	// 简单实现：由于 Watch 已经是 500ms 轮询，这里可以配合 clipboard.Watch 的原生信号
	// 或者针对 Linux 做一个内容哈希（虽然低效但仅限 Linux 兜底）
	if isWayland() {
		cmd := execClipboardCommand("wl-paste", "--list-types")
		out, err := cmd.Output()
		if err == nil {
			return int64(pkgcrypto.XXHash64(string(out)))
		}
		if errors.Is(err, exec.ErrNotFound) {
			warnWaylandMissingPasteTypesOnce.Do(func() {
				slog.Warn("剪贴板：未找到 wl-paste，Wayland 变更检测已降级")
			})
			return 0
		}
	}

	cmd := execClipboardCommand("xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		warnX11MissingXclipOnce.Do(func() {
			slog.Warn("剪贴板：未找到 xclip，X11 变更检测已降级")
		})
		return 0
	}
	return int64(pkgcrypto.XXHash64(string(out)))
}

func parseLinuxClipboardFileURI(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, "file") {
		return "", false
	}
	// Only accept local file URIs.
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "", false
	}
	if parsed.Path == "" {
		return "", false
	}
	return parsed.Path, true
}

func buildLinuxClipboardFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

// getPlatformFilePaths 尝试使用 Linux 剪贴板工具获取文件 URI。
func getPlatformFilePaths() ([]string, error) {
	var cmd *exec.Cmd
	if isWayland() {
		cmd = execClipboardCommand("wl-paste", "--type", "text/uri-list")
	} else {
		cmd = execClipboardCommand("xclip", "-selection", "clipboard", "-o", "-t", "text/uri-list")
	}

	out, err := cmd.Output()
	if err != nil {
		if isWayland() && errors.Is(err, exec.ErrNotFound) {
			warnWaylandMissingPasteReadOnce.Do(func() {
				slog.Warn("剪贴板：未找到 wl-paste，Wayland 文件读取已降级")
			})
			return nil, nil
		}
		if !isWayland() && errors.Is(err, exec.ErrNotFound) {
			warnX11MissingXclipOnce.Do(func() {
				slog.Warn("剪贴板：未找到 xclip，X11 文件读取已降级")
			})
		}
		return nil, nil
	}

	raw := string(bytes.TrimSpace(out))
	if raw == "" {
		return nil, nil
	}

	var validPaths []string
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path, ok := parseLinuxClipboardFileURI(line)
		if !ok {
			continue
		}
		// 严格磁盘校验
		if _, err := os.Stat(path); err == nil {
			validPaths = append(validPaths, path)
		}
	}

	return validPaths, nil
}

func listWaylandClipboardTypes() []string {
	cmd := execClipboardCommand("wl-paste", "--list-types")
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			warnWaylandMissingPasteTypesOnce.Do(func() {
				slog.Warn("剪贴板：未找到 wl-paste，Wayland 类型枚举已降级")
			})
		}
		return nil
	}
	lines := strings.Split(string(out), "\n")
	types := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		types = append(types, line)
	}
	return types
}

func selectWaylandPreferredType(available []string, preferred []string, prefixes []string, exclude map[string]bool) string {
	if len(available) == 0 {
		return ""
	}
	availableSet := make(map[string]struct{}, len(available))
	for _, item := range available {
		availableSet[item] = struct{}{}
	}
	for _, want := range preferred {
		if _, ok := availableSet[want]; ok {
			return want
		}
	}
	for _, item := range available {
		if exclude[item] {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(item, prefix) {
				return item
			}
		}
	}
	return ""
}

func readWaylandClipboardMime(mime string) []byte {
	if strings.TrimSpace(mime) == "" {
		return nil
	}
	cmd := execClipboardCommand("wl-paste", "--no-newline", "--type", mime)
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return out
	}
	return nil
}

// getPlatformImageData 读取系统剪贴板中的图片原始数据。
// Wayland 下 golang.design/x/clipboard 库依赖 X11 原子，无法读取 Spectacle 等
// 原生 Wayland 应用放入剪贴板的图片。必须通过 wl-paste 命令行原生读取。
func getPlatformImageData() []byte {
	if isWayland() {
		mime := selectWaylandPreferredType(
			listWaylandClipboardTypes(),
			[]string{"image/png", "image/jpeg", "image/jpg", "image/webp", "image/bmp", "image/gif"},
			[]string{"image/"},
			nil,
		)
		if data := readWaylandClipboardMime(mime); len(data) > 0 {
			return data
		}
	}
	// X11 环境或 Wayland 兜底：使用 golang.design/x/clipboard
	return nil // 由调用方 fallback 到 clipboard.Read
}

// getPlatformTextData 读取系统剪贴板中的文本数据。
// Wayland 下同理需要通过 wl-paste 读取。
func getPlatformTextData() []byte {
	if isWayland() {
		mime := selectWaylandPreferredType(
			listWaylandClipboardTypes(),
			[]string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING", "STRING", "TEXT"},
			[]string{"text/", "application/"},
			map[string]bool{
				"text/uri-list": true,
				"text/html":     true,
			},
		)
		if data := readWaylandClipboardMime(mime); len(data) > 0 {
			return data
		}
		cmd := execClipboardCommand("wl-paste", "--no-newline")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return out
		}
	}
	return nil
}

// setPlatformText 在 Wayland 下使用 wl-copy 写入纯文本，避免 golang.design/x/clipboard
// 通过 X11 原子写入导致 KDE Plasma 任务栏卡死。
func setPlatformText(text string) error {
	if !isWayland() {
		return errNotWayland
	}
	cmd := execClipboardCommand("wl-copy", "--type", "text/plain")
	cmd.Stdin = strings.NewReader(text)
	err := cmd.Start()
	if err == nil {
		holdWlCopyProcess(cmd)
	} else {
		if errors.Is(err, exec.ErrNotFound) {
			warnWaylandMissingCopyOnce.Do(func() {
				slog.Warn("剪贴板：未找到 wl-copy，Wayland 文本写入已降级")
			})
			return errNotWayland
		}
		slog.Warn("剪贴板：wl-copy 文本写入失败", "错误", err)
	}
	return err
}

var errNotWayland = errors.New("not wayland or wl-copy unavailable")

func setPlatformImage(data []byte) error {
	if !isWayland() {
		return errNotWayland
	}
	cmd := execClipboardCommand("wl-copy", "--type", clipboardImageMimeType(data))
	cmd.Stdin = bytes.NewReader(data)
	err := cmd.Start()
	if err == nil {
		holdWlCopyProcess(cmd)
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		warnWaylandMissingCopyOnce.Do(func() {
			slog.Warn("剪贴板：未找到 wl-copy，Wayland 图片写入已降级")
		})
		return errNotWayland
	}
	slog.Warn("剪贴板：wl-copy 图片写入失败", "错误", err)
	return err
}

// setPlatformFilePaths 使用 Linux 剪贴板工具写入文件路径。
func setPlatformFilePaths(paths []string) error {
	normalized := normalizeClipboardPaths(paths)
	if len(normalized) == 0 {
		return nil
	}

	uriList := make([]string, 0, len(normalized))
	for _, p := range normalized {
		uriList = append(uriList, buildLinuxClipboardFileURI(p))
	}
	// text/uri-list 规范强烈要求使用 CRLF (\r\n) 换行，
	// 否则 KDE Dolphin 或 Plasma 任务栏等应用在请求剪贴板时可能会挂起或解析失败。
	payload := strings.Join(uriList, "\r\n") + "\r\n"

	var cmd *exec.Cmd
	if isWayland() {
		slog.Info("剪贴板：通过 wl-copy 将文件路径写入 Wayland 剪贴板...")
		cmd = execClipboardCommand("wl-copy", "--type", "text/uri-list")
	} else {
		slog.Info("剪贴板：通过 xclip 将文件路径写入 Linux 剪贴板...")
		cmd = execClipboardCommand("xclip", "-selection", "clipboard", "-t", "text/uri-list")
	}

	cmd.Stdin = strings.NewReader(payload)
	err := cmd.Start()
	if err == nil {
		// Wayland 下 wl-copy 必须保持进程运行才能维持剪贴板内容
		// 持有进程引用，下次写入时才终止旧进程
		if isWayland() {
			holdWlCopyProcess(cmd)
		} else {
			go cmd.Wait()
		}
	} else {
		if isWayland() && errors.Is(err, exec.ErrNotFound) {
			warnWaylandMissingCopyOnce.Do(func() {
				slog.Warn("剪贴板：未找到 wl-copy，Wayland 文件写入已降级")
			})
			return errNotWayland
		}
		slog.Warn("剪贴板：异步写入失败", "错误", err)
	}
	return err
}

// activeWlCopy 持有当前 wl-copy 进程引用，确保 Wayland 剪贴板内容不被丢失
var (
	activeWlCopyMu sync.Mutex
	activeWlCopy   *exec.Cmd
)

func holdWlCopyProcess(cmd *exec.Cmd) {
	activeWlCopyMu.Lock()
	// 终止旧的 wl-copy 进程
	if old := activeWlCopy; old != nil && old.Process != nil {
		_ = old.Process.Kill()
		go old.Wait() // 清理僵尸进程
	}
	activeWlCopy = cmd
	activeWlCopyMu.Unlock()
	// 异步等待但不清除引用（进程结束后引用自然失效）
	go cmd.Wait()
}
