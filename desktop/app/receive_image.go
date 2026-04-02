package app

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
)

// ensureImageMaterialized 确保图片历史项已从内存（base64 Payload）物化到临时文件。
// 如果文件已经真实存在于磁盘上，则直接返回。
// 在热键触发时按需调用，不在接收时立即执行。
func (a *Application) ensureImageMaterialized(item *history.HistoryItem) (*history.HistoryItem, error) {
	if item == nil || item.Type != constants.TypeImage {
		return item, fmt.Errorf("非图片历史项")
	}

	// 检查文件是否真实存在（不仅仅检查 LocalPaths 是否非空）
	if len(item.LocalPaths) > 0 {
		if _, err := os.Stat(item.LocalPaths[0]); err == nil {
			// 文件确实存在，无需重新物化
			return item, nil
		}
		// 路径存在但文件不存在，需要重新物化
		slog.Debug("应用：图片路径存在但文件不在磁盘，重新物化", "path", item.LocalPaths[0])
	}

	if item.Payload == "" {
		return nil, fmt.Errorf("图片 Payload 为空，无法物化")
	}

	// 解码 base64 图片数据
	data, err := base64.StdEncoding.DecodeString(item.Payload)
	if err != nil {
		return nil, fmt.Errorf("解码图片 base64 失败: %w", err)
	}

	// 确定目标路径：优先使用已有的 ReservedPaths / LocalPaths
	var destPath string
	if len(item.ReservedPaths) > 0 {
		destPath = item.ReservedPaths[0]
	} else if len(item.LocalPaths) > 0 {
		destPath = item.LocalPaths[0]
	} else {
		// 规划新路径
		item, err = a.ensureReplayTargetPaths(item)
		if err != nil || len(item.ReservedPaths) == 0 {
			return nil, fmt.Errorf("规划图片路径失败: %w", err)
		}
		destPath = item.ReservedPaths[0]
	}

	// 写入临时文件
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("写入图片文件失败: %w", err)
	}

	slog.Info("应用：按需物化图片到临时文件", "path", destPath, "size", len(data))

	// 更新历史项
	updated, err := a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
		next.LocalPaths = []string{destPath}
		next.ReservedPaths = []string{destPath}
		next.UpdatedAt = time.Now()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("更新图片历史项失败: %w", err)
	}
	return updated, nil
}

// imageItemFileName 从图片历史项中提取文件名，用于路径规划。
func imageItemFileName(item *history.HistoryItem) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.FileName)
}
