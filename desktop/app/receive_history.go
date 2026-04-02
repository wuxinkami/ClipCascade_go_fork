package app

import (
	"strings"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

type receiveAction int

const (
	receiveActionIgnore receiveAction = iota
	receiveActionAdmitHistory
	receiveActionLegacyPaste
)

type receiveDecision struct {
	action receiveAction
	item   *history.HistoryItem
}

func clipboardSourceSessionID(clipData *protocol.ClipboardData) string {
	if clipData == nil {
		return ""
	}
	if source := strings.TrimSpace(clipData.SourceSessionID); source != "" {
		return source
	}
	if clipData.Metadata == nil {
		return ""
	}
	// 兼容旧客户端：历史上会话来源复用了 metadata.id。
	return strings.TrimSpace(clipData.Metadata.ID)
}

func classifyReceivedClipboardData(clipData *protocol.ClipboardData, now time.Time) receiveDecision {
	if clipData == nil {
		return receiveDecision{action: receiveActionIgnore}
	}

	sourceSessionID := clipboardSourceSessionID(clipData)
	item := &history.HistoryItem{
		Type:              clipData.Type,
		Payload:           clipData.Payload,
		FileName:          clipData.FileName,
		SourceSessionID:   sourceSessionID,
		SourceDevice:      "remote",
		CreatedAt:         now,
		UpdatedAt:         now,
		PendingReplayMode: string(ReplayModeNone),
	}

	switch clipData.Type {
	case constants.TypeText:
		item.State = history.StateReady
		item.PayloadType = constants.TypeText
		return receiveDecision{action: receiveActionAdmitHistory, item: item}
	case constants.TypeImage:
		item.State = history.StateReady
		item.PayloadType = constants.TypeImage
		return receiveDecision{action: receiveActionAdmitHistory, item: item}
	case constants.TypeFileStub:
		manifest, err := protocol.DecodePayload[protocol.FileStubManifest](clipData.Payload)
		if err == nil && manifest.ProtocolVersion > 0 && manifest.TransferID != "" {
			item.State = history.StateOffered
			item.Kind = manifest.Kind
			item.DisplayName = manifest.DisplayName
			item.TransferID = manifest.TransferID
			item.SourceSessionID = manifest.SourceSessionID
			item.SourceDevice = manifest.SourceDevice
			if item.SourceDevice == "" {
				item.SourceDevice = manifest.SourceSessionID
			}
			item.LastChunkIdx = -1
			return receiveDecision{action: receiveActionAdmitHistory, item: item}
		}
		item.State = history.StateOffered
		item.LastChunkIdx = -1
		return receiveDecision{action: receiveActionAdmitHistory, item: item}
	case constants.TypeFileEager:
		return receiveDecision{action: receiveActionLegacyPaste}
	default:
		return receiveDecision{action: receiveActionIgnore}
	}
}

func (a *Application) admitReceivedClipboardData(clipData *protocol.ClipboardData, now time.Time) receiveAction {
	decision := classifyReceivedClipboardData(clipData, now)
	if decision.action != receiveActionAdmitHistory || decision.item == nil {
		return decision.action
	}
	if a.history == nil {
		return receiveActionIgnore
	}

	// 对于已存在的 TransferID（含自发自收场景），复用现有 history item，
	// 但不提前 return，让后续的 incoming transfer 注册逻辑也能执行。
	existingFound := false
	if decision.item.TransferID != "" {
		if existing := a.history.GetByTransferID(decision.item.TransferID); existing != nil {
			a.setSharedClipboardHistoryItem(existing.ID)
			if existing.Type == constants.TypeFileStub {
				a.setLastFileStubHistoryItem(existing.ID)
			}
			existingFound = true
		}
	}

	if !existingFound {
		a.history.AddItem(decision.item)
		if stored := a.history.GetByTransferID(decision.item.TransferID); stored != nil {
			a.setSharedClipboardHistoryItem(stored.ID)
			if stored.Type == constants.TypeFileStub || stored.Type == constants.TypeImage {
				a.setLastFileStubHistoryItem(stored.ID)
			}
		} else if latest := a.history.GetActive(); latest != nil {
			a.setSharedClipboardHistoryItem(latest.ID)
			if latest.Type == constants.TypeFileStub || latest.Type == constants.TypeImage {
				a.setLastFileStubHistoryItem(latest.ID)
			}
		}
	}

	// 自发自收也注册 incoming transfer：让本机也走完整的文件下载流程，
	// 文件会落盘到 tmp 目录，Ctrl+Alt+V / Ctrl+Alt+Shift+V 可正常粘贴。
	if clipData.Type == constants.TypeFileStub && a.transfers != nil {
		if manifest, err := protocol.DecodePayload[protocol.FileStubManifest](clipData.Payload); err == nil && manifest.TransferID != "" {
			stored := a.history.GetByTransferID(manifest.TransferID)
			if stored != nil {
				a.transfers.RegisterIncoming(*manifest, stored.ID, stored.LastChunkIdx)
			}
		}
	}
	return receiveActionAdmitHistory
}
