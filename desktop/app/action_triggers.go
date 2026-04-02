package app

import (
	"log/slog"

	"github.com/clipcascade/desktop/ui"
)

var notifyFn = ui.Notify

func (a *Application) triggerSendCurrentClipboard() {
	if err := appSendCurrentClipboard(a); err != nil {
		a.recordControlEvent("clipboard", "Send current clipboard failed: "+err.Error())
		slog.Warn("application: send current clipboard failed", "error", err)
		return
	}

	a.recordControlEvent("clipboard", "Sent current clipboard")
	// 移除通用提示
}

func (a *Application) triggerReplayActiveHistoryItem() {
	a.triggerReplayWithMode(ReplayModeClipboardImmediate)
}

func (a *Application) triggerPastePlaceholderHistoryItem() {
	a.triggerReplayWithMode(ReplayModePathPlaceholderPaste)
}

func (a *Application) triggerPasteRealContentHistoryItem() {
	a.triggerReplayWithMode(ReplayModeSystemClipboardPaste)
}

func (a *Application) triggerReplayWithMode(mode ReplayMode) {
	// 记录当前共享剪贴板状态供调试
	if item := a.resolveSharedReplayItem(); item != nil {
		slog.Info("应用：热键触发 replay",
			"mode", mode,
			"item_id", item.ID,
			"item_type", item.Type,
			"item_state", item.State,
			"transfer_id", item.TransferID,
			"pending_replay_mode", item.PendingReplayMode,
			"local_paths_count", len(item.LocalPaths),
			"reserved_paths_count", len(item.ReservedPaths),
		)
	} else {
		slog.Warn("应用：热键触发但无可用的共享剪贴板项", "mode", mode)
	}
	result, err := appReplaySharedClipboardItem(a, mode)
	if err != nil {
		a.recordControlEvent("clipboard", "Replay active failed: "+err.Error())
		slog.Warn("应用：replay 失败", "error", err, "mode", mode)
		return
	}

	message := result.Message
	if message == "" {
		message = "Clipboard action sent"
	}
	slog.Info("应用：replay 完成", "mode", mode, "action", result.Action, "message", message)
	a.recordControlEvent("clipboard", message)
	// 移除通用提示
}

func (a *Application) triggerOpenHistoryPanel() {
	a.recordControlEvent("console", "Opened control center")
	appOpenHistoryPanel(a)
}
