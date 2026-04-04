package app

import (
	"sync"

	"github.com/clipcascade/desktop/history"
)

type sharedClipboardState struct {
	mu             sync.RWMutex
	id             string
	lastFileStubID string // 单独追踪最近的 file_stub/图片历史项，避免图片或文件回放目标被其它内容挤掉
}

func (a *Application) setSharedClipboardHistoryItem(id string) {
	if a == nil || id == "" {
		return
	}
	a.sharedClipboard.mu.Lock()
	a.sharedClipboard.id = id
	a.sharedClipboard.mu.Unlock()
	if a.history != nil {
		// 控制面板的 active 视图基于 history.GetActive() 渲染。
		// 当共享剪贴板最新项切换时，同步刷新 active，避免网页仍停留在旧的手动 active 项。
		a.history.SetActive(id)
	}
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
		return item
	}
	// 第二优先级：最近的 file_stub
	if item := a.lastFileStubHistoryItem(); canReplayHistoryItem(item) {
		return item
	}
	return a.history.GetActive()
}

// isLocalOriginItem 判断 history item 是否来自本机发送端（自发自收检测）。
// 仅通过 SourceSessionID 精确匹配本机 sessionID 来判断。
// 注意：不能用 SourceDevice（= username）判断，因为多台设备可能共用同一个账号。
func (a *Application) isLocalOriginItem(item *history.HistoryItem) bool {
	if a == nil || item == nil {
		return false
	}
	sessionID := a.appSessionID()
	// 文件 stub：通过 SourceSessionID 精确匹配
	if item.SourceSessionID != "" && sessionID != "" && item.SourceSessionID == sessionID {
		return true
	}
	// 图片/文本等：仅在明确标记为 "local" 时才视为本机来源
	// （local 标记仅在本机 clipboard 监控直接捕获时设置，不会出现在远端广播中）
	if item.SourceDevice == "local" {
		return true
	}
	return false
}
