package app

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/clipcascade/desktop/clipboard"
	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	pkgcrypto "github.com/clipcascade/pkg/crypto"
	"github.com/clipcascade/pkg/protocol"
	"github.com/clipcascade/pkg/sizefmt"
)

var (
	ErrNoCurrentClipboardData        = errors.New("no current clipboard data")
	ErrClipboardTransportUnavailable = errors.New("clipboard transport unavailable")
	appSendCurrentClipboard          = func(a *Application) error { return a.SendCurrentClipboard() }
	appDispatchClipboardBodyDetailed = dispatchClipboardBodyDetailed
	appDispatchClipboardBodySingle   = dispatchClipboardBodySingleRoute
)

type clipboardDispatchResult struct {
	P2PSent   bool
	StompSent bool
}

func buildClipboardDataFromCapture(capture *clipboard.CaptureData) (*protocol.ClipboardData, error) {
	if capture == nil {
		return nil, ErrNoCurrentClipboardData
	}

	return &protocol.ClipboardData{
		Payload:  capture.Payload,
		Type:     capture.Type,
		FileName: capture.FileName,
	}, nil
}

func sendCurrentClipboard(capture func() *clipboard.CaptureData, builder func(*clipboard.CaptureData) (*protocol.ClipboardData, error), sender func(*protocol.ClipboardData) error) error {
	clipData, err := builder(capture())
	if err != nil {
		return err
	}
	return sender(clipData)
}

func (a *Application) SendCurrentClipboard() error {
	if err := a.ensureClipboardReady(); err != nil {
		return err
	}

	return a.sendCapture(a.clip.CaptureCurrent())
}

func (a *Application) sendCapture(capture *clipboard.CaptureData) error {
	clipData, err := a.buildClipboardDataFromCapture(capture)
	if err != nil {
		return err
	}
	a.annotateClipboardSource(clipData)
	if _, err := a.sendClipboardDataWithResult(clipData); err != nil {
		return err
	}
	a.recordOutgoingClipboardHistory(clipData)
	return nil
}

func (a *Application) buildClipboardDataFromCapture(capture *clipboard.CaptureData) (*protocol.ClipboardData, error) {
	if capture == nil {
		return nil, ErrNoCurrentClipboardData
	}

	switch capture.Type {
	case constants.TypeFileStub:
		if len(capture.Paths) == 0 {
			return nil, ErrNoCurrentClipboardData
		}
		// 单个图片文件按图片即时链路发送，不走 file_stub 主流程。
		if len(capture.Paths) == 1 {
			if payload, filename, ok := clipboard.BuildClipboardImagePayload(capture.Paths[0]); ok {
				return &protocol.ClipboardData{
					Payload:  payload,
					Type:     constants.TypeImage,
					FileName: filename,
				}, nil
			}
		}
		sourceDevice := ""
		if a != nil && a.cfg != nil {
			sourceDevice = a.cfg.Username
		}
		transfer, err := a.transfers.RegisterOutgoing(capture.Paths, sourceDevice)
		if err != nil {
			return nil, err
		}
		return protocol.NewClipboardDataWithPayload(constants.TypeFileStub, transfer.Manifest)
	default:
		return buildClipboardDataFromCapture(capture)
	}
}

func (a *Application) annotateClipboardSource(clipData *protocol.ClipboardData) {
	if a == nil || clipData == nil {
		return
	}
	sessionID := a.appSessionID()
	if sessionID == "" {
		return
	}
	clipData.SourceSessionID = sessionID
}

func (a *Application) ensureClipboardReady() error {
	a.clipboardInitOnce.Do(func() {
		a.clipboardInitErr = a.clip.Init()
	})
	return a.clipboardInitErr
}

func (a *Application) sendClipboardData(clipData *protocol.ClipboardData) error {
	_, err := a.sendClipboardDataWithResult(clipData)
	return err
}

func (a *Application) sendClipboardDataWithResult(clipData *protocol.ClipboardData) (clipboardDispatchResult, error) {
	attrs := []any{
		"类型", clipData.Type,
		"大小", sizefmt.HumanSizeFromPayload(clipData.Type, clipData.Payload),
	}
	if names := clipboardLogNames(clipData); names != "" {
		attrs = append(attrs, "文件", names)
	} else if clipData.Type == constants.TypeImage {
		attrs = append(attrs, "文件", "[截图]")
	}
	slog.Info("应用：准备发送剪贴板更新", attrs...)

	jsonBytes, err := clipData.Encode()
	if err != nil {
		return clipboardDispatchResult{}, fmt.Errorf("encode clipboard data: %w", err)
	}

	body := string(jsonBytes)
	if a != nil && a.cfg != nil && a.cfg.E2EEEnabled && a.encKey != nil {
		encrypted, err := pkgcrypto.Encrypt(a.encKey, jsonBytes)
		if err != nil {
			return clipboardDispatchResult{}, fmt.Errorf("encrypt clipboard data: %w", err)
		}
		body, err = pkgcrypto.EncodeToJSONString(encrypted)
		if err != nil {
			return clipboardDispatchResult{}, fmt.Errorf("encode encrypted clipboard data: %w", err)
		}
	}

	if isSingleRouteClipboardType(clipData.Type) {
		targetSessionID := targetSessionIDForSingleRouteClipboardData(clipData)
		return appDispatchClipboardBodySingle(
			body,
			func(payload string) int {
				if a.p2p == nil {
					return 0
				}
				if targetSessionID != "" {
					return a.p2p.SendTo(targetSessionID, payload)
				}
				return a.p2p.Send(payload)
			},
			func() bool { return a.stomp != nil && a.stomp.IsConnected() },
			func(payload string) error {
				return a.stomp.Send(payload)
			},
		)
	}

	return appDispatchClipboardBodyDetailed(
		body,
		func() bool { return a.stomp != nil && a.stomp.IsConnected() },
		func(payload string) error {
			return a.stomp.Send(payload)
		},
		func(payload string) int {
			if a.p2p == nil {
				return 0
			}
			return a.p2p.Send(payload)
		},
	)
}

func isSingleRouteClipboardType(messageType string) bool {
	switch messageType {
	case constants.TypeFileRequest,
		constants.TypeFileChunk,
		constants.TypeFileComplete,
		constants.TypeFileError,
		constants.TypeFileRelease:
		return true
	default:
		return false
	}
}

func targetSessionIDForSingleRouteClipboardData(clipData *protocol.ClipboardData) string {
	if clipData == nil || clipData.Payload == "" {
		return ""
	}
	switch clipData.Type {
	case constants.TypeFileRequest:
		payload, err := protocol.DecodePayload[protocol.FileRequest](clipData.Payload)
		if err == nil && payload != nil {
			return payload.TargetSessionID
		}
	case constants.TypeFileChunk:
		payload, err := protocol.DecodePayload[protocol.FileChunk](clipData.Payload)
		if err == nil && payload != nil {
			return payload.TargetSessionID
		}
	case constants.TypeFileComplete:
		payload, err := protocol.DecodePayload[protocol.FileComplete](clipData.Payload)
		if err == nil && payload != nil {
			return payload.TargetSessionID
		}
	case constants.TypeFileError:
		payload, err := protocol.DecodePayload[protocol.FileError](clipData.Payload)
		if err == nil && payload != nil {
			return payload.TargetSessionID
		}
	case constants.TypeFileRelease:
		payload, err := protocol.DecodePayload[protocol.FileRelease](clipData.Payload)
		if err == nil && payload != nil {
			return payload.TargetSessionID
		}
	}
	return ""
}

func (a *Application) recordOutgoingClipboardHistory(clipData *protocol.ClipboardData) {
	if a == nil || a.history == nil || clipData == nil {
		return
	}

	switch clipData.Type {
	case constants.TypeText:
		a.recordOutgoingTextHistory(clipData)
	case constants.TypeImage:
		a.recordOutgoingImageHistory(clipData)
	case constants.TypeFileStub:
		a.recordOutgoingFileStubHistory(clipData)
	}
}

func (a *Application) recordOutgoingTextHistory(clipData *protocol.ClipboardData) {
	now := time.Now()
	item := &history.HistoryItem{
		Type:              constants.TypeText,
		State:             history.StateReady,
		PayloadType:       constants.TypeText,
		Payload:           clipData.Payload,
		SourceSessionID:   clipboardSourceSessionID(clipData),
		SourceDevice:      a.localHistorySourceDevice(),
		CreatedAt:         now,
		UpdatedAt:         now,
		PendingReplayMode: string(ReplayModeNone),
	}
	a.history.AddItem(item)
	if latest := a.history.GetActive(); latest != nil {
		a.setSharedClipboardHistoryItem(latest.ID)
	}
}

// recordOutgoingImageHistory 为本地截图/复制的图片创建 history item。
// 图片数据（base64 Payload）保留在内存中，用户按热键时才落盘到 /tmp。
func (a *Application) recordOutgoingImageHistory(clipData *protocol.ClipboardData) {
	now := time.Now()
	item := &history.HistoryItem{
		Type:              constants.TypeImage,
		State:             history.StateReady,
		PayloadType:       constants.TypeImage,
		Payload:           clipData.Payload,
		FileName:          clipData.FileName,
		SourceSessionID:   clipboardSourceSessionID(clipData),
		SourceDevice:      a.localHistorySourceDevice(),
		CreatedAt:         now,
		UpdatedAt:         now,
		PendingReplayMode: string(ReplayModeNone),
	}
	a.history.AddItem(item)
	if latest := a.history.GetActive(); latest != nil {
		a.setSharedClipboardHistoryItem(latest.ID)
		a.setLastFileStubHistoryItem(latest.ID)
	}
	slog.Info("应用：本地截图已加入历史（内存）", "大小", sizefmt.HumanSizeFromPayload(clipData.Type, clipData.Payload))
}

func (a *Application) localHistorySourceDevice() string {
	if a != nil && a.cfg != nil {
		if username := strings.TrimSpace(a.cfg.Username); username != "" {
			return username
		}
	}
	return "local"
}

func (a *Application) recordOutgoingFileStubHistory(clipData *protocol.ClipboardData) {
	manifest, err := protocol.DecodePayload[protocol.FileStubManifest](clipData.Payload)
	if err != nil || manifest == nil || manifest.TransferID == "" {
		return
	}
	if existing := a.history.GetByTransferID(manifest.TransferID); existing != nil {
		return
	}

	now := time.Now()
	sourceDevice := manifest.SourceDevice
	if sourceDevice == "" && a.cfg != nil {
		sourceDevice = a.cfg.Username
	}
	item := &history.HistoryItem{
		Type:              constants.TypeFileStub,
		State:             history.StateOffered,
		Kind:              manifest.Kind,
		DisplayName:       manifest.DisplayName,
		Payload:           clipData.Payload,
		SourceSessionID:   manifest.SourceSessionID,
		SourceDevice:      sourceDevice,
		CreatedAt:         now,
		UpdatedAt:         now,
		TransferID:        manifest.TransferID,
		LastChunkIdx:      -1,
		PendingReplayMode: string(ReplayModeNone),
	}
	a.history.AddItem(item)
	if stored := a.history.GetByTransferID(manifest.TransferID); stored != nil {
		a.setSharedClipboardHistoryItem(stored.ID)
		a.setLastFileStubHistoryItem(stored.ID)
	} else if item.ID != "" {
		a.setSharedClipboardHistoryItem(item.ID)
		a.setLastFileStubHistoryItem(item.ID)
	}
}

func dispatchClipboardBody(
	body string,
	stompConnected func() bool,
	stompSend func(string) error,
	p2pSend func(string) int,
) error {
	_, err := dispatchClipboardBodyDetailed(body, stompConnected, stompSend, p2pSend)
	return err
}

func dispatchClipboardBodyDetailed(
	body string,
	stompConnected func() bool,
	stompSend func(string) error,
	p2pSend func(string) int,
) (clipboardDispatchResult, error) {
	result := clipboardDispatchResult{}
	// P2P 尝试发送（快速通道）
	p2pSent := false
	if p2pSend != nil {
		if sentPeers := p2pSend(body); sentPeers > 0 {
			p2pSent = true
			result.P2PSent = true
		}
	}

	// 始终通过 STOMP 发送作为可靠保底
	// P2P 连接是不对称的（接收端可能有 P2P 但发送端没有），
	// 不能仅靠 P2P sentPeers > 0 就认为消息已送达。
	if stompConnected != nil && stompSend != nil && stompConnected() {
		if err := stompSend(body); err != nil {
			if !p2pSent {
				return result, err
			}
			// P2P 已发送成功，STOMP 失败可以容忍
			slog.Debug("剪贴板：STOMP 发送失败但 P2P 已发送", "error", err)
		} else {
			result.StompSent = true
		}
		return result, nil
	}

	// 没有 STOMP 连接
	if p2pSent {
		return result, nil
	}
	return result, ErrClipboardTransportUnavailable
}

func dispatchClipboardBodySingleRoute(
	body string,
	p2pSend func(string) int,
	stompConnected func() bool,
	stompSend func(string) error,
) (clipboardDispatchResult, error) {
	result := clipboardDispatchResult{}
	if p2pSend != nil {
		if sentPeers := p2pSend(body); sentPeers > 0 {
			result.P2PSent = true
			return result, nil
		}
	}
	if stompConnected != nil && stompSend != nil && stompConnected() {
		if err := stompSend(body); err != nil {
			return result, err
		}
		result.StompSent = true
		return result, nil
	}
	return result, ErrClipboardTransportUnavailable
}
