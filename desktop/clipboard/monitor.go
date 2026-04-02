// Package clipboard 提供跨平台的剪贴板监控和管理功能。
package clipboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.design/x/clipboard"

	"github.com/clipcascade/pkg/constants"
	pkgcrypto "github.com/clipcascade/pkg/crypto"
	"github.com/clipcascade/pkg/sizefmt"
)

// Manager 处理剪贴板监听、更改检测和内容编码。
type Manager struct {
	mu              sync.Mutex
	lastHash        uint64
	suppressedEdits int
	suppressedAt    time.Time // suppressedEdits 上次设置时间，用于过期清零
	onCopy          func(*CaptureData)
	notifier        func(title, message string)
}

// CaptureData 表示一次主动读取当前系统剪贴板得到的结果。
type CaptureData struct {
	Payload  string
	Type     string
	FileName string
	Paths    []string
}

// fileStubPayload 是文件懒加载模式下的轻量元信息，不包含文件内容。
type fileStubPayload struct {
	Count      int      `json:"count"`
	TotalBytes int64    `json:"total_bytes"`
	Names      []string `json:"names,omitempty"`
	Lazy       bool     `json:"lazy"`
}

const tempFileRetention = 24 * time.Hour

var monitorCaptureCurrent = func(m *Manager) *CaptureData {
	if m == nil {
		return nil
	}
	return m.CaptureCurrent()
}

// NewManager 创建一个新的剪贴板 Manager。
func NewManager() *Manager {
	return &Manager{}
}

// Init 初始化剪贴板子系统。在某些平台上必须从 main goroutine 调用。
func (m *Manager) Init() error {
	return clipboard.Init()
}

// CleanupExpiredTempFiles 在启动阶段清理超过保留时长的临时文件。
func (m *Manager) CleanupExpiredTempFiles() {
	tempDir := filepath.Join(os.TempDir(), "ClipCascade")
	cleanupOldTempFiles(tempDir, tempFileRetention)
}

// CaptureCurrent 按需读取当前系统剪贴板内容。
func (m *Manager) CaptureCurrent() *CaptureData {
	paths, _ := getPlatformFilePaths()

	// 优先使用平台原生方式读取（Wayland 下 wl-paste 比 golang.design/x/clipboard 更可靠）
	imageData := getPlatformImageData()
	if len(imageData) == 0 {
		imageData = clipboard.Read(clipboard.FmtImage)
	}
	textData := getPlatformTextData()
	if len(textData) == 0 {
		textData = clipboard.Read(clipboard.FmtText)
	}

	return deriveCaptureData(paths, imageData, textData)
}

func deriveCaptureData(paths []string, image []byte, text []byte) *CaptureData {
	if len(paths) > 0 {
		normalized := normalizeClipboardPaths(paths)
		return &CaptureData{
			Payload:  buildFileStubPayload(normalized),
			Type:     constants.TypeFileStub,
			FileName: buildFileStubMeta(normalized),
			Paths:    normalized,
		}
	}
	if len(image) > 0 {
		return &CaptureData{
			Payload: base64.StdEncoding.EncodeToString(image),
			Type:    constants.TypeImage,
		}
	}
	if len(text) > 0 {
		return &CaptureData{
			Payload: string(text),
			Type:    constants.TypeText,
		}
	}
	return nil
}

// OnCopy 设置剪贴板内容更改时的回调。
func (m *Manager) OnCopy(fn func(*CaptureData)) {
	m.onCopy = fn
}

// SetNotifier 设置可选通知回调，避免 clipboard 包直接依赖 UI 层。
func (m *Manager) SetNotifier(fn func(title, message string)) {
	m.notifier = fn
}

// Watch 开始监控剪贴板变更。
// 它通过轮询系统底层的变更计数器（SequenceNumber/ChangeCount）来实现零 CGO 的事件驱动模拟。
func (m *Manager) Watch(ctx context.Context) {
	// macOS / Linux: 文本和图片采用事件监听；文件保持 1s 轮询兜底。
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		go m.watchEventDriven(ctx)
		return
	}

	// 其他平台（Windows）保持原有变更计数轮询策略。
	go m.watchLegacyByChangeCount(ctx)
}

func (m *Manager) watchLegacyByChangeCount(ctx context.Context) {
	var lastCount int64 = -1

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count := getPlatformChangeCount()
			if count == lastCount {
				continue
			}

			// 只有在初始启动后的第一次变化才处理，或者计数器确实发生了位移
			if lastCount != -1 {
				m.handleSystemChange()
			}
			lastCount = count
		}
	}
}

func (m *Manager) watchEventDriven(ctx context.Context) {
	textCh := clipboard.Watch(ctx, clipboard.FmtText)
	imageCh := clipboard.Watch(ctx, clipboard.FmtImage)
	fileTicker := time.NewTicker(1 * time.Second) // 文件一秒循环检测一次
	defer fileTicker.Stop()

	var nativeTicker *time.Ticker
	var nativeTick <-chan time.Time
	nativeSeeded := true
	if runtime.GOOS == "linux" && isWayland() {
		nativeTicker = time.NewTicker(250 * time.Millisecond)
		defer nativeTicker.Stop()
		nativeTick = nativeTicker.C
		nativeSeeded = false
	}

	// 与旧轮询逻辑保持一致：忽略启动后的首次事件，避免刚连接时回放当前剪贴板。
	skipFirstTextEvent := true
	skipFirstImageEvent := true
	skipFirstFileTick := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-nativeTick:
			if !nativeSeeded {
				m.handleNativeClipboardSnapshot(true)
				nativeSeeded = true
				continue
			}
			m.handleNativeClipboardSnapshot(false)
		case <-fileTicker.C:
			if skipFirstFileTick {
				skipFirstFileTick = false
				continue
			}
			if runtime.GOOS == "linux" && isWayland() {
				m.handleNativeClipboardSnapshot(false)
				continue
			}
			m.handleFileChange()
		case data, ok := <-textCh:
			if !ok {
				textCh = nil
				continue
			}
			if skipFirstTextEvent {
				skipFirstTextEvent = false
				continue
			}
			if runtime.GOOS == "linux" && isWayland() {
				m.handleNativeClipboardSnapshot(false)
				continue
			}
			// 保持文件优先级：如果当前是文件剪贴板，优先走文件链路。
			if m.handleFileChange() {
				continue
			}
			if len(data) > 0 {
				m.handleChange(CaptureData{
					Payload: string(data),
					Type:    constants.TypeText,
				})
			}
		case data, ok := <-imageCh:
			if !ok {
				imageCh = nil
				continue
			}
			if skipFirstImageEvent {
				skipFirstImageEvent = false
				continue
			}
			if runtime.GOOS == "linux" && isWayland() {
				m.handleNativeClipboardSnapshot(false)
				continue
			}
			if m.handleFileChange() {
				continue
			}
			if len(data) > 0 {
				m.handleChange(CaptureData{
					Payload: base64.StdEncoding.EncodeToString(data),
					Type:    constants.TypeImage,
				})
			}
		}
	}
}

func (m *Manager) handleNativeClipboardSnapshot(seedOnly bool) bool {
	capture := monitorCaptureCurrent(m)
	if capture == nil {
		return false
	}
	if seedOnly {
		m.seedCaptureHash(*capture)
		return false
	}
	m.handleChange(*capture)
	return true
}

func (m *Manager) seedCaptureHash(capture CaptureData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastHash = pkgcrypto.XXHash64(capture.Payload)
}

// handleSystemChange 当检测到系统剪贴板变动时，按严格的物理优先级尝试读取。
func (m *Manager) handleSystemChange() {
	paths, _ := getPlatformFilePaths()
	result, ok := selectClipboardContent(paths, clipboard.Read(clipboard.FmtImage), clipboard.Read(clipboard.FmtText), false)
	if !ok {
		return
	}
	m.handleChange(result)
}

func selectClipboardContent(paths []string, imageData []byte, textData []byte, forceFileStub bool) (CaptureData, bool) {
	if result, ok := selectFileContent(paths, forceFileStub); ok {
		return result, true
	}
	if len(imageData) > 0 {
		return CaptureData{
			Payload: base64.StdEncoding.EncodeToString(imageData),
			Type:    constants.TypeImage,
		}, true
	}
	if len(textData) > 0 {
		return CaptureData{
			Payload: string(textData),
			Type:    constants.TypeText,
		}, true
	}
	return CaptureData{}, false
}

func selectFileContent(paths []string, forceFileStub bool) (CaptureData, bool) {
	normalized := normalizeClipboardPaths(paths)
	if len(normalized) == 0 {
		return CaptureData{}, false
	}
	// 所有文件（包括单个图片文件）统一走 TypeFileStub 懒传
	return CaptureData{
		Payload:  buildFileStubPayload(normalized),
		Type:     constants.TypeFileStub,
		FileName: buildFileStubMeta(normalized),
		Paths:    append([]string(nil), normalized...),
	}, true
}

func (m *Manager) handleFileChange() bool {
	paths, _ := getPlatformFilePaths()
	result, ok := selectFileContent(paths, false)
	if !ok {
		return false
	}
	m.handleChange(result)
	return true
}

// handleChange 处理剪贴板更改事件。
func (m *Manager) handleChange(capture CaptureData) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 统一使用 xxHash 检查内容是否确实发生实质性更改（防止自身 Paste 死循环）
	hash := pkgcrypto.XXHash64(capture.Payload)

	// 过期清零：距上次设置超过 5 秒的抑制计数视为过期，防止 Wayland 多次 MIME type 变更消耗计数后永久抑制真实 Ctrl+C
	if m.suppressedEdits > 0 && !m.suppressedAt.IsZero() && time.Since(m.suppressedAt) > 5*time.Second {
		slog.Debug("剪贴板：suppressedEdits 已过期，强制清零", "剩余", m.suppressedEdits)
		m.suppressedEdits = 0
	}
	if m.suppressedEdits > 0 {
		m.lastHash = hash
		m.suppressedEdits--
		return
	}
	if hash == m.lastHash {
		return
	}
	m.lastHash = hash

	if capture.Type == constants.TypeFileStub {
		meta := parseFileStubPayloadWithMeta(capture.Payload, capture.FileName)
		first := ""
		if len(meta.Names) > 0 {
			first = meta.Names[0]
		}
		slog.Debug("剪贴板：检测到文件懒加载占位符", "文件数", meta.Count, "总大小", sizefmt.FormatBytes(meta.TotalBytes), "首文件", first)
	} else if capture.Type == constants.TypeFileEager {
		// 兼容旧版本: 仍可收到 file_eager，但本端不再主动发送此类型。
		slog.Debug("剪贴板：收到旧版 file_eager 数据", "文件名", capture.FileName, "大小", sizefmt.FormatBytes(int64(sizefmt.EstimatedBase64DecodedSize(capture.Payload))))
	} else {
		slog.Debug("剪贴板：检测到更改", "类型", capture.Type, "大小", len(capture.Payload))
	}

	if m.onCopy != nil {
		m.onCopy(cloneCaptureData(&capture))
	}
}

// Paste sets the clipboard content. Updates lastHash to securely prevent self-trigger loop echoing.
func (m *Manager) Paste(payload string, payloadType string, filename string) {
	m.prepareForLocalClipboardWrite(pkgcrypto.XXHash64(payload))

	switch payloadType {
	case constants.TypeText:
		// Wayland 下使用 wl-copy 原生写入，避免 X11 原子导致 KDE 卡顿
		if err := setPlatformText(payload); err == nil {
			m.AddExtraSuppression()
			m.AddExtraSuppression()
		} else {
			clipboard.Write(clipboard.FmtText, []byte(payload))
		}
		slog.Debug("剪贴板：已粘贴文本", "大小", len(payload))
		if m.notifier != nil {
			m.notifier("ClipCascade", fmt.Sprintf("收到文本剪贴板更新 (%s)", sizefmt.FormatBytes(int64(len(payload)))))
		}
	case constants.TypeImage:
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			slog.Warn("剪贴板：无法解码图像", "错误", err)
			return
		}
		clipboard.Write(clipboard.FmtImage, data)
		slog.Debug("剪贴板：已粘贴图像", "大小", len(data))
		if m.notifier != nil {
			m.notifier("ClipCascade", fmt.Sprintf("收到图片剪贴板更新 (%s)", sizefmt.FormatBytes(int64(len(data)))))
		}
	case constants.TypeFileStub:
		meta := parseFileStubPayloadWithMeta(payload, filename)
		slog.Info("剪贴板：收到文件懒加载占位符", "文件数", meta.Count, "总大小", sizefmt.FormatBytes(meta.TotalBytes))
		if m.notifier != nil {
			m.notifier("ClipCascade", fmt.Sprintf("收到文件剪贴板更新 (%d 个, %s)", meta.Count, sizefmt.FormatBytes(meta.TotalBytes)))
		}
	case constants.TypeFileEager:
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			slog.Warn("剪贴板：无法解码文件直传数据", "错误", err)
			return
		}
		tempDir := filepath.Join(os.TempDir(), "ClipCascade")
		if err := os.MkdirAll(tempDir, 0o755); err != nil {
			slog.Warn("剪贴板：无法创建临时目录", "错误", err)
			return
		}
		cleanupOldTempFiles(tempDir, tempFileRetention)
		safeName := sanitizeFilename(filename)
		if safeName == "" {
			safeName = "clipcascade-file"
		}
		destPath := filepath.Join(tempDir, safeName)
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			slog.Warn("剪贴板：无法保存接收文件", "错误", err)
			return
		}
		if err := setPlatformFilePaths([]string{destPath}); err != nil {
			slog.Warn("剪贴板：无法设置文件路径到系统剪贴板", "错误", err)
		}
		size := sizefmt.FormatBytes(int64(len(data)))
		slog.Info("剪贴板：已接收并写入文件到临时目录", "文件名", safeName, "大小", size, "路径", destPath)
		if m.notifier != nil {
			m.notifier("ClipCascade", fmt.Sprintf("收到文件剪贴板更新 (%s)", size))
		}
	default:
		slog.Warn("剪贴板：不支持的数据类型", "类型", payloadType)
	}
}

func (m *Manager) StageText(text string) error {
	m.prepareForLocalClipboardWrite(pkgcrypto.XXHash64(text))
	// Wayland 下 golang.design/x/clipboard 走 X11 原子可能导致 KDE 任务栏卡死，
	// 优先使用 wl-copy 原生写入。wl-copy 写入同样触发多个 MIME-type 变更事件，
	// 需要额外抑制 2 次。
	if err := setPlatformText(text); err == nil {
		m.AddExtraSuppression()
		m.AddExtraSuppression()
		return nil
	}
	clipboard.Write(clipboard.FmtText, []byte(text))
	return nil
}

func (m *Manager) StageImageFile(path string) error {
	data, err := clipboardImageBytesFromFile(path)
	if err != nil {
		return err
	}
	hash := pkgcrypto.XXHash64(base64.StdEncoding.EncodeToString(data))
	m.prepareForLocalClipboardWrite(hash)
	// Wayland 下写入图片也会触发多个 MIME-type 变更事件
	m.AddExtraSuppression()
	m.AddExtraSuppression()
	clipboard.Write(clipboard.FmtImage, data)
	return nil
}

// StageFilePaths writes local file paths back into the system clipboard.
func (m *Manager) StageFilePaths(paths []string) error {
	normalized := normalizeClipboardPaths(paths)
	if len(normalized) == 0 {
		return nil
	}

	// 使用与 handleChange 中相同的 Payload 格式计算 hash，确保自循环抑制生效。
	// handleChange 中文件类型的 hash 基于 CaptureData.Payload，
	// 而 Payload 由 buildFileStubPayload 生成。
	hash := pkgcrypto.XXHash64(buildFileStubPayload(normalized))
	// Wayland 的 wl-copy 写入会触发多个 MIME-type 变更事件
	// (text/uri-list, text/plain, x-special/gnome-copied-files 等)，
	// 单次抑制不够，需要额外抑制 2 次以覆盖所有并发事件。
	m.prepareForLocalClipboardWrite(hash)
	m.AddExtraSuppression()
	m.AddExtraSuppression()

	return setPlatformFilePaths(normalized)
}

func (m *Manager) prepareForLocalClipboardWrite(hash uint64) {
	m.mu.Lock()
	m.lastHash = hash
	m.suppressedEdits++
	m.suppressedAt = time.Now()
	m.mu.Unlock()
}

// AddExtraSuppression 增加一次额外的剪贴板变更抑制计数。
// 用于 simulateAutoPaste 等场景：模拟 Ctrl+V 可能导致目标应用回写剪贴板，
// 产生额外的变更事件需要被忽略。
func (m *Manager) AddExtraSuppression() {
	m.mu.Lock()
	m.suppressedEdits++
	m.suppressedAt = time.Now()
	m.mu.Unlock()
}

func cloneCaptureData(capture *CaptureData) *CaptureData {
	if capture == nil {
		return nil
	}
	clone := *capture
	if capture.Paths != nil {
		clone.Paths = append([]string(nil), capture.Paths...)
	}
	return &clone
}

func buildFileStubPayload(paths []string) string {
	normalized := normalizeClipboardPaths(paths)
	// 发送端优先使用旧版兼容格式（换行分隔路径），保证与历史客户端互通。
	// 新版接收端仍支持解析 JSON file_stub（用于未来协议升级）。
	return strings.Join(normalized, "\n")
}

func normalizeClipboardPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		normalized = append(normalized, p)
	}
	return normalized
}

func buildFileStubMeta(paths []string) string {
	meta := fileStubPayload{
		Names: make([]string, 0, len(paths)),
		Lazy:  true,
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		meta.Count++
		name := filepath.Base(p)
		if name == "" || name == "." || name == string(os.PathSeparator) {
			name = "unknown"
		}
		meta.Names = append(meta.Names, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			meta.TotalBytes += info.Size()
		}
	}
	if meta.Count == 0 {
		return ""
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(data)
}

func buildFileEagerPayload(path string) (payload string, filename string, ok bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", "", false
	}
	const maxEagerBytes = constants.DefaultMaxMessageSizeMiB * 1024 * 1024
	if info.Size() <= 0 || info.Size() > maxEagerBytes {
		return "", "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(data), filepath.Base(path), true
}

func parseFileStubPayload(payload string) fileStubPayload {
	return parseFileStubPayloadWithMeta(payload, "")
}

func parseFileStubPayloadWithMeta(payload string, metaRaw string) fileStubPayload {
	var meta fileStubPayload
	if err := json.Unmarshal([]byte(metaRaw), &meta); err == nil && meta.Count > 0 {
		return meta
	}
	if err := json.Unmarshal([]byte(payload), &meta); err == nil && meta.Count > 0 {
		return meta
	}
	// 兼容旧格式: 换行分隔路径
	lines := strings.Split(payload, "\n")
	meta.Lazy = true
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		meta.Count++
		meta.Names = append(meta.Names, filepath.Base(line))
		if info, err := os.Stat(line); err == nil && !info.IsDir() {
			meta.TotalBytes += info.Size()
		}
	}
	return meta
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "." || name == string(os.PathSeparator) {
		return ""
	}
	return name
}

func cleanupOldTempFiles(dir string, olderThan time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if entry.IsDir() {
			_ = os.RemoveAll(path)
		} else {
			_ = os.Remove(path)
		}
	}
}
