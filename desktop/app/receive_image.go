package app

import (
	"crypto/sha256"
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

type imageMaterializeJob struct {
	done chan struct{}
	err  error
}

var appMaterializeImageNow = func(a *Application, item *history.HistoryItem) (*history.HistoryItem, error) {
	return a.materializeImageNow(item)
}

// ensureImageMaterialized 确保图片历史项已从内存（base64 Payload）物化到临时文件。
// 幂等：如果同名同大小的文件已存在于磁盘上，直接复用不重新写入。
// 在热键触发时按需调用，不在接收时立即执行。
func (a *Application) ensureImageMaterialized(item *history.HistoryItem) (*history.HistoryItem, error) {
	if item == nil || item.Type != constants.TypeImage {
		return item, fmt.Errorf("非图片历史项")
	}
	if a == nil || item.ID == "" || a.history == nil {
		return appMaterializeImageNow(a, item)
	}

	job, leader := a.beginImageMaterializeJob(item.ID)
	if !leader {
		<-job.done
		if job.err != nil {
			return nil, job.err
		}
		if stored := a.history.GetByID(item.ID); stored != nil {
			return stored, nil
		}
		return item, nil
	}

	updated, err := appMaterializeImageNow(a, item)
	a.finishImageMaterializeJob(item.ID, job, err)
	return updated, err
}

func (a *Application) ensureImageMaterializedAsync(item *history.HistoryItem) {
	if item == nil || item.Type != constants.TypeImage {
		return
	}
	if strings.TrimSpace(item.Payload) == "" {
		return
	}
	if a == nil || item.ID == "" {
		return
	}

	job, leader := a.beginImageMaterializeJob(item.ID)
	if !leader {
		return
	}
	go func(itemID string, fallback *history.HistoryItem, activeJob *imageMaterializeJob) {
		current := fallback
		if a != nil && a.history != nil {
			if stored := a.history.GetByID(itemID); stored != nil {
				current = stored
			}
		}
		updated, err := appMaterializeImageNow(a, current)
		if err != nil {
			slog.Warn("应用：图片后台物化失败", "item_id", itemID, "error", err)
		} else if updated != nil && len(updated.LocalPaths) > 0 {
			slog.Info("应用：图片后台物化完成", "item_id", itemID, "path", updated.LocalPaths[0])
		}
		a.finishImageMaterializeJob(itemID, activeJob, err)
	}(item.ID, item, job)
}

func (a *Application) materializeImageNow(item *history.HistoryItem) (*history.HistoryItem, error) {
	if item == nil || item.Type != constants.TypeImage {
		return item, fmt.Errorf("非图片历史项")
	}
	if len(item.LocalPaths) > 0 {
		slog.Debug("应用：检查图片物化路径", "path", item.LocalPaths[0])
	}

	if item.Payload == "" {
		if imageMaterializedOnDisk(item) {
			return item, nil
		}
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
		destPath, err = normalizeReplayTargetPath(item.ReservedPaths[0])
	} else if len(item.LocalPaths) > 0 {
		destPath, err = normalizeReplayTargetPath(item.LocalPaths[0])
	} else {
		// 规划新路径
		item, err = a.ensureReplayTargetPaths(item)
		if err != nil || len(item.ReservedPaths) == 0 {
			return nil, fmt.Errorf("规划图片路径失败: %w", err)
		}
		destPath, err = normalizeReplayTargetPath(item.ReservedPaths[0])
	}
	if err != nil {
		return nil, fmt.Errorf("图片目标路径非法: %w", err)
	}

	// 幂等检查：如果同名文件已存在且内容一致，直接复用
	match, matchErr := fileMatchesBytes(destPath, data)
	if matchErr != nil {
		return nil, fmt.Errorf("检查图片落盘复用失败: %w", matchErr)
	}
	if match {
		slog.Info("应用：同名同内容图片已存在，复用", "path", destPath, "size", len(data))
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

func (a *Application) beginImageMaterializeJob(itemID string) (*imageMaterializeJob, bool) {
	if a == nil || itemID == "" {
		return &imageMaterializeJob{done: make(chan struct{})}, true
	}
	a.imageMaterializeMu.Lock()
	defer a.imageMaterializeMu.Unlock()

	if a.imageMaterializeJobs == nil {
		a.imageMaterializeJobs = make(map[string]*imageMaterializeJob)
	}
	if job, ok := a.imageMaterializeJobs[itemID]; ok {
		return job, false
	}
	job := &imageMaterializeJob{done: make(chan struct{})}
	a.imageMaterializeJobs[itemID] = job
	return job, true
}

func (a *Application) finishImageMaterializeJob(itemID string, job *imageMaterializeJob, err error) {
	if job == nil {
		return
	}
	job.err = err
	close(job.done)
	if a == nil || itemID == "" {
		return
	}
	a.imageMaterializeMu.Lock()
	defer a.imageMaterializeMu.Unlock()
	if a.imageMaterializeJobs != nil && a.imageMaterializeJobs[itemID] == job {
		delete(a.imageMaterializeJobs, itemID)
	}
}

func imageMaterializedOnDisk(item *history.HistoryItem) bool {
	if item == nil || len(item.LocalPaths) == 0 {
		return false
	}
	targetPath, err := normalizeReplayTargetPath(item.LocalPaths[0])
	if err != nil {
		return false
	}
	if _, err := os.Stat(targetPath); err == nil {
		return true
	}
	return false
}

// imageItemFileName 从图片历史项中提取文件名，用于路径规划。
func imageItemFileName(item *history.HistoryItem) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.FileName)
}

func fileMatchesBytes(path string, data []byte) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() || info.Size() != int64(len(data)) {
		return false, nil
	}
	sum, size, err := fileSHA256(path)
	if err != nil {
		return false, err
	}
	expected := sha256.Sum256(data)
	return size == int64(len(data)) && sum == fmt.Sprintf("%x", expected[:]), nil
}
