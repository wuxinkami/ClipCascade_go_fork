package app

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/protocol"
)

func (a *Application) ensureReservedReplayPaths(item *history.HistoryItem) (*history.HistoryItem, error) {
	if item == nil {
		return nil, ErrNoActiveHistoryItem
	}
	if len(item.ReservedPaths) > 0 {
		return item, nil
	}
	if a == nil || a.history == nil {
		return nil, ErrNoActiveHistoryItem
	}

	paths, err := reservedPathsForHistoryItem(item)
	if err != nil {
		return nil, err
	}
	updated, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
		if len(next.ReservedPaths) == 0 {
			next.ReservedPaths = append([]string(nil), paths...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (a *Application) storePendingReplayMode(item *history.HistoryItem, mode ReplayMode) (*history.HistoryItem, error) {
	if item == nil || a == nil || a.history == nil {
		return nil, ErrNoActiveHistoryItem
	}
	updated, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
		next.PendingReplayMode = string(mode)
		if len(next.ReservedPaths) == 0 && len(item.ReservedPaths) > 0 {
			next.ReservedPaths = append([]string(nil), item.ReservedPaths...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func reservedPathsForHistoryItem(item *history.HistoryItem) ([]string, error) {
	if item == nil {
		return nil, ErrNoActiveHistoryItem
	}
	manifest, err := protocol.DecodePayload[protocol.FileStubManifest](item.Payload)
	if err != nil || manifest == nil {
		return nil, fmt.Errorf("decode file manifest: %w", err)
	}

	return reservedPathsForManifest(manifest, item.CreatedAt)
}

func reservedPathsForManifest(manifest *protocol.FileStubManifest, createdAt time.Time) ([]string, error) {
	if manifest == nil {
		return nil, fmt.Errorf("nil file manifest")
	}
	tempDir := filepath.Join(os.TempDir(), "ClipCascade")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}

	candidateName := reservedManifestFileName(manifest, createdAt)
	// 幂等：直接使用原文件名
	destPath, err := safeJoinUnderBase(tempDir, candidateName)
	if err != nil {
		return nil, err
	}
	return []string{destPath}, nil
}

func reservedManifestFileName(manifest *protocol.FileStubManifest, createdAt time.Time) string {
	timestamp := createdAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	switch manifest.Kind {
	case protocol.FileKindImage:
		name := strings.TrimSpace(manifestPrimaryName(manifest))
		if name == "" {
			return fmt.Sprintf("%s.png", timestamp.Format("20060102150405"))
		}
		name = sanitizeFileName(name)
		if filepath.Ext(name) == "" {
			name += ".png"
		}
		return name
	case protocol.FileKindSingleFile:
		name := strings.TrimSpace(manifestPrimaryName(manifest))
		if name == "" {
			return fmt.Sprintf("file_%s.bin", timestamp.Format("20060102150405"))
		}
		return sanitizeFileName(name)
	case protocol.FileKindFolder:
		name := strings.TrimSpace(manifestPrimaryName(manifest))
		if name == "" {
			return fmt.Sprintf("folder_%s", timestamp.Format("20060102150405"))
		}
		return sanitizeFileName(name)
	case protocol.FileKindMultiFile:
		return replayTimestampDirName(timestamp)
	default:
		if manifest.EntryCount > 1 {
			return replayTimestampDirName(timestamp)
		}
		name := sanitizeFileName(manifestPrimaryName(manifest))
		if name == "" {
			return fmt.Sprintf("file_%s.bin", timestamp.Format("20060102150405"))
		}
		return name
	}
}

func manifestPrimaryName(manifest *protocol.FileStubManifest) string {
	if manifest == nil {
		return ""
	}
	if len(manifest.TopLevelNames) > 0 {
		return manifest.TopLevelNames[0]
	}
	if manifest.DisplayName != "" {
		if idx := strings.Index(manifest.DisplayName, " and "); idx > 0 {
			return manifest.DisplayName[:idx]
		}
		return manifest.DisplayName
	}
	return ""
}

func reserveUniquePath(path string) (string, error) {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	candidate := path
	for idx := 1; ; idx++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s_%d%s", base, idx, ext)
	}
}

func (a *Application) completePendingReplayMode(transferID string) error {
	if a == nil || a.history == nil || transferID == "" {
		return nil
	}
	item := a.history.GetByTransferID(transferID)
	if item == nil {
		slog.Warn("应用：completePendingReplayMode 找不到 history item", "transfer_id", transferID)
		return nil
	}
	slog.Info("应用：completePendingReplayMode 传输完成后处理",
		"transfer_id", transferID,
		"pending_mode", item.PendingReplayMode,
		"state", item.State,
		"local_paths", item.LocalPaths,
	)

	pendingMode := ReplayMode(item.PendingReplayMode)

	switch pendingMode {
	case ReplayModeSystemClipboardPaste:
		slog.Info("应用：completePendingReplayMode 执行真实内容写入系统剪贴板")
		_, err := a.stageRealClipboardContent(item)
		if err != nil {
			slog.Warn("应用：completePendingReplayMode stageRealClipboardContent 失败", "error", err)
			return err
		}
		if _, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
			next.State = history.StateConsumed
			next.PendingReplayMode = string(ReplayModeNone)
			return nil
		}); err != nil {
			return err
		}

	case ReplayModePathPlaceholderPaste:
		if names := historyItemLogNames(item); names != "" {
			slog.Info("应用：占位路径关联的文件传输已完成", "文件", names)
		}
		if _, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
			next.State = history.StateConsumed
			next.PendingReplayMode = string(ReplayModeNone)
			return nil
		}); err != nil {
			return err
		}

	case ReplayModeNone, ReplayModeClipboardImmediate:
		// 无 pending 动作，清理 PendingReplayMode 即可
		slog.Debug("应用：completePendingReplayMode 无 pending 动作", "pending_mode", item.PendingReplayMode)
		if item.PendingReplayMode != "" {
			_, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
				next.PendingReplayMode = string(ReplayModeNone)
				return nil
			})
			return err
		}
		return nil

	default:
		slog.Warn("应用：completePendingReplayMode 未知的 pending mode", "pending_mode", pendingMode)
		return nil
	}

	// 只有实际完成了 pending 的文件传输动作才发送通知
	displayName := item.DisplayName
	if displayName == "" {
		displayName = "File"
	}
	notifyFn("ClipCascade", "File received / 接收完成: "+displayName)

	return nil
}
