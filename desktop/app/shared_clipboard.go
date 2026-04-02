package app

import (
	"sync"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
)

type sharedClipboardState struct {
	mu             sync.RWMutex
	id             string
	lastFileStubID string // 单独追踪最近的 file_stub，防止 text/image 覆盖后丢失
}

func (a *Application) setSharedClipboardHistoryItem(id string) {
	if a == nil || id == "" {
		return
	}
	a.sharedClipboard.mu.Lock()
	a.sharedClipboard.id = id
	a.sharedClipboard.mu.Unlock()
}

func (a *Application) setLastFileStubHistoryItem(id string) {
	if a == nil || id == "" {
		return
	}
	a.sharedClipboard.mu.Lock()
	a.sharedClipboard.lastFileStubID = id
	a.sharedClipboard.mu.Unlock()
}

func (a *Application) sharedClipboardHistoryItem() *history.HistoryItem {
	if a == nil || a.history == nil {
		return nil
	}
	a.sharedClipboard.mu.RLock()
	id := a.sharedClipboard.id
	a.sharedClipboard.mu.RUnlock()
	if id == "" {
		return nil
	}
	return a.history.GetByID(id)
}

func (a *Application) lastFileStubHistoryItem() *history.HistoryItem {
	if a == nil || a.history == nil {
		return nil
	}
	a.sharedClipboard.mu.RLock()
	id := a.sharedClipboard.lastFileStubID
	a.sharedClipboard.mu.RUnlock()
	if id == "" {
		return nil
	}
	return a.history.GetByID(id)
}

func (a *Application) resolveSharedReplayItem() *history.HistoryItem {
	if a == nil || a.history == nil {
		return nil
	}
	// 第一优先级：共享剪贴板指向的项（可能是 text/image/file_stub）
	if item := a.sharedClipboardHistoryItem(); canReplayHistoryItem(item) {
		// 如果共享剪贴板就是 file_stub，直接返回
		if item.Type == constants.TypeFileStub {
			return item
		}
		// 如果共享剪贴板是 text/image，但有进行中/待处理的 file_stub，优先返回 file_stub
		// 这防止了中间到达的 text 把 file_stub 从热键可达范围挤掉
		if fileItem := a.lastFileStubHistoryItem(); canReplayHistoryItem(fileItem) {
			return fileItem
		}
		return item
	}
	// 第二优先级：最近的 file_stub
	if item := a.lastFileStubHistoryItem(); canReplayHistoryItem(item) {
		return item
	}
	return a.history.GetActive()
}

// isLocalOriginItem 判断 history item 是否来自本机发送端（自发自收检测）。
// 文件 stub：检查 SourceSessionID 是否等于本机 session ID。
// 图片：检查 SourceDevice 是否为 "local" 或等于本机 username。
func (a *Application) isLocalOriginItem(item *history.HistoryItem) bool {
	if a == nil || item == nil {
		return false
	}
	sessionID := a.appSessionID()
	// 文件 stub：通过 SourceSessionID 精确匹配
	if item.SourceSessionID != "" && sessionID != "" && item.SourceSessionID == sessionID {
		return true
	}
	// 图片/其他：通过 SourceDevice 匹配
	if item.SourceDevice == "local" {
		return true
	}
	if a.cfg != nil && a.cfg.Username != "" && item.SourceDevice == a.cfg.Username {
		return true
	}
	return false
}

