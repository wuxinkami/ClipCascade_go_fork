package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
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
	appRequestFileTransfer   = func(a *Application, item *history.HistoryItem) error { return a.requestFileTransfer(item) }
	appStageClipboardText    = func(a *Application, text string) error { return a.clip.StageText(text) }
	appStageClipboardImage   = func(a *Application, path string) error { return a.clip.StageImageFile(path) }
	appStageClipboardFiles   = func(a *Application, paths []string) error { return a.clip.StageFilePaths(paths) }
	appStartImageMaterialize = func(a *Application, item *history.HistoryItem) {
		if a != nil {
			a.ensureImageMaterializedAsync(item)
		}
	}
	appSimulateAutoPaste = func() error { return simulateAutoPaste() }
	appIsWaylandSession  = func() bool { return runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") != "" }
	appSleep             = func(delay time.Duration) { time.Sleep(delay) }
	appRealClipboardMu   sync.Mutex
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
	autoPasteRequested := opts.autoPaste

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

	if !a.writeClipboardPayloadIfAllowed(clipboardWriteReasonReplayText, &protocol.ClipboardData{
		Type:     item.Type,
		Payload:  item.Payload,
		FileName: item.FileName,
	}) {
		return ReplayResult{}, ErrClipboardUnavailable
	}
	if item.State == history.StateReady {
		if !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
	}

	result := ReplayResult{
		Action:  replayActionClipboardStaged,
		Type:    item.Type,
		Mode:    mode,
		Message: "Copied active item to clipboard",
	}
	return result, nil
}

func (a *Application) replayLazyClipboardItem(item *history.HistoryItem, mode ReplayMode) (ReplayResult, error) {
	if mode == ReplayModePathPlaceholderPaste && item.Type != constants.TypeImage {
		return ReplayResult{}, ErrReplayModeNotApplicable
	}
	// 图片延迟物化：Ctrl+Alt+V 先给路径并自动粘贴，再后台把内存图片写到 /tmp。
	// 只有需要真实文件剪贴板的路径，才同步等待图片物化完成。
	if item.Type == constants.TypeImage && item.Payload != "" && mode != ReplayModePathPlaceholderPaste {
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
	if item == nil || item.Type != constants.TypeImage {
		return ReplayResult{}, ErrReplayModeNotApplicable
	}
	if !imageMaterializedOnDisk(item) && strings.TrimSpace(item.Payload) == "" {
		return ReplayResult{}, fmt.Errorf("%w: missing image payload", ErrUnsupportedReplayState)
	}
	item, err := a.ensureReplayTargetPaths(item)
	if err != nil {
		return ReplayResult{}, err
	}
	if len(item.ReservedPaths) == 0 {
		return ReplayResult{}, fmt.Errorf("%w: missing reserved paths", ErrUnsupportedReplayState)
	}
	if !a.isClipboardWriteAllowed(clipboardWriteReasonReplayPath, &protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: item.ReservedPaths[0],
	}) {
		return ReplayResult{}, ErrClipboardUnavailable
	}
	if err := appStageClipboardText(a, item.ReservedPaths[0]); err != nil {
		return ReplayResult{}, err
	}
	if !imageMaterializedOnDisk(item) {
		appStartImageMaterialize(a, item)
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
		Message:            "Copied placeholder path to clipboard",
	}

	switch item.State {
	case history.StateReady, history.StateReadyToPaste, history.StateConsumed:
		if (item.State == history.StateReady || item.State == history.StateReadyToPaste) && !a.history.UpdateState(item.ID, history.StateConsumed) {
			return ReplayResult{}, ErrReplayStateUpdate
		}
	case history.StateOffered, history.StateFailed:
		if err := appRequestFileTransfer(a, item); err != nil {
			return ReplayResult{}, err
		}
		result.Action = replayActionDownloadRequested
		result.Message = "Copied placeholder path to clipboard and started transfer"
	case history.StateDownloading:
		result.Action = replayActionDownloadInProgress
		result.Message = "Copied placeholder path to clipboard. Transfer still in progress"
	default:
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}

	a.applyAutoPasteResult(&result)
	if result.ManualPasteRequired {
		switch result.Action {
		case replayActionDownloadRequested:
			result.Message = "Placeholder path copied. Transfer started. Press Ctrl+V manually."
		case replayActionDownloadInProgress:
			result.Message = "Placeholder path copied. Transfer still in progress. Press Ctrl+V manually."
		default:
			result.Message = "Placeholder path staged. Press Ctrl+V manually."
		}
	} else {
		switch result.Action {
		case replayActionDownloadRequested:
			result.Message = "Pasted placeholder path and started transfer"
		case replayActionDownloadInProgress:
			result.Message = "Pasted placeholder path. Transfer still in progress"
		default:
			result.Message = "Pasted placeholder path"
		}
	}
	return result, nil
}

func (a *Application) replayLazyRealClipboard(item *history.HistoryItem) (ReplayResult, error) {
	if a == nil || a.clip == nil {
		return ReplayResult{}, ErrClipboardUnavailable
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
		result, err := a.stageRealClipboardContent(item)
		if err != nil {
			return ReplayResult{}, err
		}
		if shouldMarkFileReplayConsumed(item) && !a.history.UpdateState(item.ID, history.StateConsumed) {
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
			Message: "Started transfer for real content clipboard copy",
		}, nil
	case history.StateDownloading:
		slog.Info("应用：replayLazyRealClipboard 传输进行中，等待用户重试",
			"state", item.State, "transfer_id", item.TransferID)
		return ReplayResult{
			Action:  replayActionDownloadInProgress,
			Type:    item.Type,
			Mode:    ReplayModeSystemClipboardPaste,
			Message: "Real content transfer already in progress",
		}, nil
	default:
		slog.Warn("应用：replayLazyRealClipboard 不支持的状态", "state", item.State)
		return ReplayResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	}
}

func (a *Application) stageRealClipboardContent(item *history.HistoryItem) (ReplayResult, error) {
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
		Action: replayActionClipboardStaged,
		Type:   item.Type,
		Mode:   ReplayModeSystemClipboardPaste,
	}
	if item != nil && item.Type == constants.TypeImage {
		result.AutoPasteRequested = true
		a.applyAutoPasteResult(&result)
		if result.ManualPasteRequired {
			result.Message = "Image file copied to clipboard. Press Ctrl+V manually."
		} else {
			result.Message = "Pasted real image file"
		}
		return result, nil
	}
	result.Message = "Copied real content to clipboard"
	return result, nil
}

func shouldMarkFileReplayConsumed(item *history.HistoryItem) bool {
	if item == nil {
		return false
	}
	return item.State == history.StateReady || item.State == history.StateReadyToPaste
}

func (a *Application) stageHistoryItemRealClipboard(item *history.HistoryItem) error {
	if item == nil || a == nil || a.clip == nil {
		return ErrClipboardUnavailable
	}
	if !a.isClipboardWriteAllowed(clipboardWriteReasonReplayReal, &protocol.ClipboardData{
		Type: item.Type,
	}) {
		return ErrClipboardUnavailable
	}
	switch item.Type {
	case constants.TypeImage:
		if len(item.LocalPaths) == 0 {
			return fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
		}
		return appStageClipboardFiles(a, item.LocalPaths[:1])
	case constants.TypeFileStub:
		if len(item.LocalPaths) == 0 {
			return fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
		}
		return appStageClipboardFiles(a, item.LocalPaths)
	default:
		return fmt.Errorf("%w: missing local paths", ErrUnsupportedReplayState)
	}
}

func (a *Application) applyAutoPasteResult(result *ReplayResult) {
	if result == nil || !result.AutoPasteRequested {
		return
	}
	// 预留的键盘注入辅助逻辑：若未来启用，需要额外抑制一次回写事件。
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
