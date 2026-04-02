package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
)

var (
	ErrNoActiveHistoryItem     = errors.New("no active history item")
	ErrClipboardUnavailable    = errors.New("clipboard manager unavailable")
	ErrUnsupportedReplayState  = errors.New("unsupported history item state for replay")
	ErrUnsupportedReplayType   = errors.New("unsupported history item type for replay")
	ErrReplayModeNotApplicable = errors.New("replay mode not applicable for history item type")
	ErrReplayStateUpdate       = errors.New("failed to mark history item consumed")
	ErrAutoPasteUnavailable    = errors.New("auto paste unavailable on this platform")
	appReplayActiveHistoryItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		return a.ReplayActiveHistoryItem(mode)
	}
	appReplaySharedClipboardItem = func(a *Application, mode ReplayMode) (ReplayResult, error) {
		return a.ReplaySharedClipboardItem(mode)
	}
	appRequestFileTransfer = func(a *Application, item *history.HistoryItem) error { return a.requestFileTransfer(item) }
	appStageClipboardText  = func(a *Application, text string) error { return a.clip.StageText(text) }
	appStageClipboardFiles = func(a *Application, paths []string) error { return a.clip.StageFilePaths(paths) }
	appSimulateAutoPaste   = func() error { return simulateAutoPaste() }
	appIsWaylandSession    = func() bool { return runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" }
	appSleep               = func(delay time.Duration) { time.Sleep(delay) }
	appRealClipboardMu     sync.Mutex
)

const waylandFileClipboardSettleWait = 120 * time.Millisecond

type ReplayMode string

const (
	ReplayModeNone                 ReplayMode = ""
	ReplayModeClipboardImmediate   ReplayMode = "clipboard_immediate"
	ReplayModePathPlaceholderPaste ReplayMode = "path_placeholder_paste"
	ReplayModeSystemClipboardPaste ReplayMode = "system_clipboard_paste"
)

type replayAction string

const (
	replayActionClipboardStaged    replayAction = "clipboard_staged"
	replayActionDownloadRequested  replayAction = "download_requested"
	replayActionDownloadInProgress replayAction = "download_in_progress"
)

type ReplayResult struct {
	Action              replayAction
	Type                string
	Mode                ReplayMode
	AutoPasteRequested  bool
	AutoPasteAttempted  bool
	ManualPasteRequired bool
	Message             string
}

type replayOptions struct {
	autoPaste bool
	mode      ReplayMode
}

type replayExecutor struct {
	paste          func(payload string, payloadType string, filename string)
	stageFilePaths func(paths []string) error
	markConsumed   func(id string) bool
	autoPaste      func() error
}

func canReplayHistoryItem(item *history.HistoryItem) bool {
	if item == nil {
		return false
	}

	switch item.Type {
	case constants.TypeText:
		return item.State == history.StateReady || item.State == history.StateConsumed
	case constants.TypeImage:
		switch item.State {
		case history.StateReady, history.StateReadyToPaste, history.StateConsumed:
			// 图片可能尚未落盘（Payload 在内存中），也允许重放
			return len(item.LocalPaths) > 0 || item.Payload != ""
		default:
			return false
		}
	case constants.TypeFileStub:
		switch item.State {
		case history.StateOffered, history.StateDownloading, history.StateFailed:
			return true
		case history.StateReadyToPaste, history.StateConsumed:
			return len(item.LocalPaths) > 0 || len(item.ReservedPaths) > 0
		default:
			return false
		}
	default:
		return false
	}
}

func replayHistoryItem(item *history.HistoryItem, exec replayExecutor, opts replayOptions) (ReplayResult, error) {
	if item == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}
	mode := opts.mode
	if mode == ReplayModeNone {
		mode = ReplayModeClipboardImmediate
	}
	autoPasteRequested := opts.autoPaste || mode == ReplayModePathPlaceholderPaste || mode == ReplayModeSystemClipboardPaste

	result := ReplayResult{
		Action:             replayActionClipboardStaged,
		Type:               item.Type,
		Mode:               mode,
		AutoPasteRequested: autoPasteRequested,
	}

	switch item.Type {
	case constants.TypeText:
		if item.State != history.StateReady && item.State != history.StateConsumed {
			return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
		}
		if exec.paste == nil {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		exec.paste(item.Payload, item.Type, item.FileName)
		if item.State == history.StateReady {
			if exec.markConsumed == nil || item.ID == "" || !exec.markConsumed(item.ID) {
				return ReplayResult{}, ErrReplayStateUpdate
			}
		}
	case constants.TypeImage:
		if (item.State == history.StateReady || item.State == history.StateReadyToPaste || item.State == history.StateConsumed) && len(item.LocalPaths) > 0 {
			if exec.stageFilePaths == nil {
				return ReplayResult{}, ErrClipboardUnavailable
			}
			if err := exec.stageFilePaths(item.LocalPaths); err != nil {
				return ReplayResult{}, err
			}
			if item.State == history.StateReady || item.State == history.StateReadyToPaste {
				if exec.markConsumed == nil || item.ID == "" || !exec.markConsumed(item.ID) {
					return ReplayResult{}, ErrReplayStateUpdate
				}
			}
		} else {
			return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
		}
	case constants.TypeFileStub:
		if item.State != history.StateReadyToPaste && item.State != history.StateConsumed {
			return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
		}
		if len(item.LocalPaths) == 0 {
			return ReplayResult{}, fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
		}
		if exec.stageFilePaths == nil {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		if err := exec.stageFilePaths(item.LocalPaths); err != nil {
			return ReplayResult{}, err
		}
		if item.State == history.StateReadyToPaste {
			if exec.markConsumed == nil || item.ID == "" || !exec.markConsumed(item.ID) {
				return ReplayResult{}, ErrReplayStateUpdate
			}
		}
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayType, item.Type)
	}

	if autoPasteRequested {
		result.AutoPasteAttempted = true
		if exec.autoPaste == nil || exec.autoPaste() != nil {
			result.ManualPasteRequired = true
		}
	}
	return result, nil
}

// ReplayActiveHistoryItem replays the current active history item onto the local clipboard.
func (a *Application) ReplayActiveHistoryItem(mode ReplayMode) (ReplayResult, error) {
	if mode == ReplayModeNone {
		mode = ReplayModeClipboardImmediate
	}
	if a == nil || a.history == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}

	item := a.history.GetActive()
	if item == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}
	switch item.Type {
	case constants.TypeText:
		if a.clip == nil {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayReadyClipboardItem(item, mode)
	case constants.TypeImage:
		if a.clip == nil && (item.State == history.StateReady || item.State == history.StateReadyToPaste || item.State == history.StateConsumed) {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayLazyClipboardItem(item, mode)
	case constants.TypeFileStub:
		if a.clip == nil && (item.State == history.StateReadyToPaste || item.State == history.StateConsumed) {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayLazyClipboardItem(item, mode)
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayType, item.Type)
	}
}

// ReplaySharedClipboardItem replays the latest shared clipboard item tracked by the app.
func (a *Application) ReplaySharedClipboardItem(mode ReplayMode) (ReplayResult, error) {
	if mode == ReplayModeNone {
		mode = ReplayModeClipboardImmediate
	}
	if a == nil || a.history == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}

	item := a.resolveSharedReplayItem()
	if item == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}

	switch item.Type {
	case constants.TypeText:
		if a.clip == nil {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayReadyClipboardItem(item, mode)
	case constants.TypeImage:
		if a.clip == nil && (item.State == history.StateReady || item.State == history.StateReadyToPaste || item.State == history.StateConsumed) {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayLazyClipboardItem(item, mode)
	case constants.TypeFileStub:
		if a.clip == nil && (item.State == history.StateReadyToPaste || item.State == history.StateConsumed) {
			return ReplayResult{}, ErrClipboardUnavailable
		}
		return a.replayLazyClipboardItem(item, mode)
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayType, item.Type)
	}
}

func (a *Application) replayReadyClipboardItem(item *history.HistoryItem, mode ReplayMode) (ReplayResult, error) {
	if item == nil {
		return ReplayResult{}, ErrNoActiveHistoryItem
	}
	if item.Type == constants.TypeText && mode != ReplayModeClipboardImmediate {
		return ReplayResult{}, ErrReplayModeNotApplicable
	}
	if item.State != history.StateReady && item.State != history.StateConsumed {
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}

	a.clip.Paste(item.Payload, item.Type, item.FileName)
	if item.State == history.StateReady {
		if !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
	}

	result := ReplayResult{
		Action:             replayActionClipboardStaged,
		Type:               item.Type,
		Mode:               mode,
		AutoPasteRequested: mode != ReplayModeClipboardImmediate,
		Message:            "Staged active item to clipboard",
	}
	if mode != ReplayModeClipboardImmediate {
		a.applyAutoPasteResult(&result)
		if result.ManualPasteRequired {
			result.Message = "Clipboard staged. Press Ctrl+V manually."
		} else {
			result.Message = "Pasted active item"
		}
	}
	return result, nil
}

func (a *Application) replayLazyClipboardItem(item *history.HistoryItem, mode ReplayMode) (ReplayResult, error) {
	// 图片延迟物化：首次热键触发时才从内存（base64 Payload）落盘到 /tmp
	// ensureImageMaterialized 内部会检查文件是否真实存在
	if item.Type == constants.TypeImage && item.Payload != "" {
		materialized, err := a.ensureImageMaterialized(item)
		if err != nil {
			slog.Warn("应用：图片物化失败", "error", err, "item_id", item.ID)
			return ReplayResult{}, err
		}
		item = materialized
	}

	switch mode {
	case ReplayModeClipboardImmediate:
		return a.replayLazyImmediate(item)
	case ReplayModePathPlaceholderPaste:
		return a.replayLazyPlaceholder(item)
	case ReplayModeSystemClipboardPaste:
		return a.replayLazyRealClipboard(item)
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayType, mode)
	}
}

func (a *Application) replayLazyImmediate(item *history.HistoryItem) (ReplayResult, error) {
	switch item.State {
	case history.StateOffered, history.StateFailed:
		if err := appRequestFileTransfer(a, item); err != nil {
			return ReplayResult{}, err
		}
		return ReplayResult{
			Action:  replayActionDownloadRequested,
			Type:    item.Type,
			Mode:    ReplayModeClipboardImmediate,
			Message: "Started file transfer for active item",
		}, nil
	case history.StateDownloading:
		return ReplayResult{
			Action:  replayActionDownloadInProgress,
			Type:    item.Type,
			Mode:    ReplayModeClipboardImmediate,
			Message: "File transfer already in progress",
		}, nil
	case history.StateReady, history.StateReadyToPaste, history.StateConsumed:
		if len(item.LocalPaths) == 0 {
			return ReplayResult{}, fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
		}
		if err := a.stageHistoryItemRealClipboard(item); err != nil {
			return ReplayResult{}, err
		}
		if (item.State == history.StateReady || item.State == history.StateReadyToPaste) && !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
		return ReplayResult{
			Action:  replayActionClipboardStaged,
			Type:    item.Type,
			Mode:    ReplayModeClipboardImmediate,
			Message: "Staged real content to clipboard",
		}, nil
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}
}

func (a *Application) replayLazyPlaceholder(item *history.HistoryItem) (ReplayResult, error) {
	if a == nil || a.clip == nil {
		return ReplayResult{}, ErrClipboardUnavailable
	}
	item, err := a.ensureReplayTargetPaths(item)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(item.ReservedPaths) == 0 {
		return ReplayResult{}, fmt.Errorf("%w: missing reserved paths", ErrUnsupportedReplayState)
	}
	if err := appStageClipboardText(a, item.ReservedPaths[0]); err != nil {
		return ReplayResult{}, err
	}
	attrs := []any{
		"mode", ReplayModePathPlaceholderPaste,
		"目标路径", item.ReservedPaths[0],
	}
	if names := historyItemLogNames(item); names != "" {
		attrs = append(attrs, "文件", names)
	}
	slog.Info("应用：已写入占位路径到系统剪贴板", attrs...)

	result := ReplayResult{
		Action:             replayActionClipboardStaged,
		Type:               item.Type,
		Mode:               ReplayModePathPlaceholderPaste,
		AutoPasteRequested: true,
		Message:            "Pasted placeholder path",
	}
	a.applyAutoPasteResult(&result)
	if result.ManualPasteRequired {
		result.Message = "Placeholder path staged. Press Ctrl+V manually."
	}

	switch item.State {
	case history.StateReady, history.StateReadyToPaste, history.StateConsumed:
		if (item.State == history.StateReady || item.State == history.StateReadyToPaste) && !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
		return result, nil
	case history.StateOffered, history.StateFailed:
		if err := appRequestFileTransfer(a, item); err != nil {
			return ReplayResult{}, err
		}
		result.Action = replayActionDownloadRequested
		result.Message = "Pasted placeholder path and started transfer"
		return result, nil
	case history.StateDownloading:
		result.Action = replayActionDownloadInProgress
		result.Message = "Pasted placeholder path. Transfer still in progress"
		return result, nil
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}
}

func (a *Application) replayLazyRealClipboard(item *history.HistoryItem) (ReplayResult, error) {
	if a == nil || a.clip == nil {
		return ReplayResult{}, ErrClipboardUnavailable
	}

	// 自发自收场景：本机既是发送端又是接收端。
	// 如果 item 没有 LocalPaths（首次触发、未被路径粘贴占位过），则系统剪贴板
	// 可能仍保持原始内容，直接模拟 Ctrl+V 即可。
	// 但如果 item 已有 LocalPaths（之前按过 Ctrl+Alt+V 或图片已物化），
	// 系统剪贴板可能已被路径文本覆盖，需要重新将真实文件写入系统剪贴板。
	if a.isLocalOriginItem(item) {
		// 已有落盘文件：重新将真实文件写回系统文件型剪贴板
		if len(item.LocalPaths) > 0 {
			slog.Info("应用：replayLazyRealClipboard 自发自收，item 已有本地文件，回写系统剪贴板",
				"item_id", item.ID, "item_type", item.Type, "local_paths", item.LocalPaths)
			result, err := a.stageRealClipboardWithAutoPaste(item)
			if err != nil {
				return ReplayResult{}, err
			}
			if (item.State == history.StateReady || item.State == history.StateReadyToPaste) && !a.history.UpdateState(item.ID, history.StateConsumed) {
				return ReplayResult{}, ErrReplayStateUpdate
			}
			return result, nil
		}

		// 没有 LocalPaths 但有 ReservedPaths：之前按过 Ctrl+Alt+V，
		// 系统剪贴板已被路径文本覆盖，需要用原始源路径恢复系统文件型剪贴板。
		if len(item.ReservedPaths) > 0 && item.Type == constants.TypeFileStub && item.TransferID != "" {
			if transfer := a.transfers.GetOutgoing(item.TransferID); transfer != nil && len(transfer.SourcePaths) > 0 {
				slog.Info("应用：replayLazyRealClipboard 自发自收，使用发送端原始路径恢复系统剪贴板",
					"item_id", item.ID, "source_paths", transfer.SourcePaths)
				if err := appStageClipboardFiles(a, transfer.SourcePaths); err != nil {
					return ReplayResult{}, err
				}
				if appIsWaylandSession() {
					appSleep(waylandFileClipboardSettleWait)
				}
				result := ReplayResult{
					Action:             replayActionClipboardStaged,
					Type:               item.Type,
					Mode:               ReplayModeSystemClipboardPaste,
					AutoPasteRequested: true,
					Message:            "Self-loopback: restored original files to clipboard",
				}
				a.applyAutoPasteResult(&result)
				if result.ManualPasteRequired {
					result.Message = "Self-loopback: press Ctrl+V manually"
				}
				return result, nil
			}
		}

		// 没有 LocalPaths 也没有 ReservedPaths：系统剪贴板应仍持有原始内容，直接模拟粘贴
		slog.Info("应用：replayLazyRealClipboard 自发自收，直接模拟粘贴，不覆盖系统剪贴板",
			"item_id", item.ID, "item_type", item.Type, "state", item.State)

		result := ReplayResult{
			Action:             replayActionClipboardStaged,
			Type:               item.Type,
			Mode:               ReplayModeSystemClipboardPaste,
			AutoPasteRequested: true,
			Message:            "Self-loopback: paste from system clipboard directly",
		}
		a.applyAutoPasteResult(&result)
		if result.ManualPasteRequired {
			result.Message = "Self-loopback: press Ctrl+V manually"
		} else {
			result.Message = "Self-loopback: pasted from system clipboard"
		}
		return result, nil
	}

	item, err := a.ensureReplayTargetPaths(item)
	if err != nil {
		slog.Warn("应用：replayLazyRealClipboard ensureReservedReplayPaths 失败", "error", err)
		return ReplayResult{}, err
	}

	switch item.State {
	case history.StateReady, history.StateReadyToPaste, history.StateConsumed:
		slog.Info("应用：replayLazyRealClipboard 文件已就绪，写入系统剪贴板",
			"state", item.State, "local_paths", item.LocalPaths)
		if len(item.LocalPaths) == 0 {
			return ReplayResult{}, fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
		}
		result, err := a.stageRealClipboardWithAutoPaste(item)
		if err != nil {
			return ReplayResult{}, err
		}
		if (item.State == history.StateReady || item.State == history.StateReadyToPaste) && !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
		return result, nil
	case history.StateOffered, history.StateFailed:
		slog.Info("应用：replayLazyRealClipboard 开始请求文件传输",
			"state", item.State, "transfer_id", item.TransferID)
		if err := appRequestFileTransfer(a, item); err != nil {
			return ReplayResult{}, err
		}
		return ReplayResult{
			Action:  replayActionDownloadRequested,
			Type:    item.Type,
			Mode:    ReplayModeSystemClipboardPaste,
			Message: "Started transfer for real clipboard paste",
		}, nil
	case history.StateDownloading:
		slog.Info("应用：replayLazyRealClipboard 传输进行中，等待用户重试",
			"state", item.State, "transfer_id", item.TransferID)
		return ReplayResult{
			Action:  replayActionDownloadInProgress,
			Type:    item.Type,
			Mode:    ReplayModeSystemClipboardPaste,
			Message: "Real clipboard transfer already in progress",
		}, nil
	default:
		slog.Warn("应用：replayLazyRealClipboard 不支持的状态", "state", item.State)
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}
}

func (a *Application) stageRealClipboardWithAutoPaste(item *history.HistoryItem) (ReplayResult, error) {
	appRealClipboardMu.Lock()
	defer appRealClipboardMu.Unlock()

	if err := a.stageHistoryItemRealClipboard(item); err != nil {
		return ReplayResult{}, err
	}
	if appIsWaylandSession() {
		appSleep(waylandFileClipboardSettleWait)
	}
	attrs := []any{
		"mode", ReplayModeSystemClipboardPaste,
	}
	if names := historyItemLogNames(item); names != "" {
		attrs = append(attrs, "文件", names)
	}
	if targets := pathLogNames(item.LocalPaths); targets != "" {
		attrs = append(attrs, "本地路径", targets)
	}
	slog.Info("应用：已写入真实内容到系统剪贴板", attrs...)
	result := ReplayResult{
		Action:             replayActionClipboardStaged,
		Type:               item.Type,
		Mode:               ReplayModeSystemClipboardPaste,
		AutoPasteRequested: true,
		Message:            "Staged real content to clipboard",
	}
	a.applyAutoPasteResult(&result)
	if result.ManualPasteRequired {
		result.Message = "Real content staged. Press Ctrl+V manually."
	} else {
		result.Message = "Pasted real content"
	}
	return result, nil
}

func (a *Application) stageHistoryItemRealClipboard(item *history.HistoryItem) error {
	if item == nil || a == nil || a.clip == nil {
		return ErrClipboardUnavailable
	}
	// 图片按文件同型处理：本地消费时统一回写文件路径，而不是图像二进制。
	switch {
	case len(item.LocalPaths) > 0:
		return appStageClipboardFiles(a, item.LocalPaths)
	default:
		return fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
	}
}

func (a *Application) applyAutoPasteResult(result *ReplayResult) {
	if result == nil || !result.AutoPasteRequested {
		return
	}
	// 自动粘贴模拟 Ctrl+V 可能导致目标应用回写剪贴板，需要额外抑制一次
	if a.clip != nil {
		a.clip.AddExtraSuppression()
	}
	result.AutoPasteAttempted = true
	if err := appSimulateAutoPaste(); err != nil {
		result.ManualPasteRequired = true
		slog.Warn("application: auto-paste failed after clipboard stage", "error", err, "mode", result.Mode, "type", result.Type)
	}
}

func (a *Application) refreshReplayActiveAvailability() {
	if a == nil || a.tray == nil || a.history == nil {
		return
	}
	a.tray.SetReplayActionsEnabled(canReplayHistoryItem(a.resolveSharedReplayItem()))
}
