package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	pkgcrypto "github.com/clipcascade/pkg/crypto"
	"github.com/clipcascade/pkg/protocol"
)

const (
	fileTransferChunkSize        = 1024 * 1024
	fileTransferArchive          = "payload.zip"
	fileTransferRawPayload       = "payload.bin"
	fileTransferExtracted        = "extracted"
	fileTransferTempPrefix       = "clipcascade-transfer-"
	fileTransferTempRetention    = 24 * time.Hour
	fileTransferArchiveRetention = 15 * time.Minute
)

type transferArchiveMode string

const (
	transferArchiveModeDisk   transferArchiveMode = "disk"
	transferArchiveModeMemory transferArchiveMode = "memory"
)

var errMemoryArchiveTooLarge = errors.New("memory archive exceeds threshold")

type outgoingTransfer struct {
	Manifest        protocol.FileStubManifest
	SourcePaths     []string
	ArchiveMode     transferArchiveMode
	ArchiveBytes    []byte
	BaseDir         string
	ArchivePath     string
	ArchiveSHA256   string
	ArchiveSize     int64
	ChunkCount      int
	cleanupTimer    *time.Timer
	cleanupGen      uint64
	SourceSignature string
	sendingTargets  map[string]struct{} // 仅去重同一目标端的并发发送
	prepareMu       sync.Mutex
}

type incomingTransfer struct {
	Manifest            protocol.FileStubManifest
	HistoryItemID       string
	ArchiveMode         transferArchiveMode
	StorageMode         transferArchiveMode
	ArchiveBytes        []byte
	BaseDir             string
	ArchivePath         string
	ExtractDir          string
	LastChunkIdx        int
	TotalChunks         int
	RequestedResumeFrom int
	RequestActive       bool
	Completing          bool
	Completed           bool
}

type transferManager struct {
	sessionID string

	mu       sync.Mutex
	outgoing map[string]*outgoingTransfer
	incoming map[string]*incomingTransfer
}

func newTransferManager(sessionID ...string) *transferManager {
	id := ""
	if len(sessionID) > 0 {
		id = sessionID[0]
	}
	if id == "" {
		id = uuid.NewString()
	}
	return &transferManager{
		sessionID: id,
		outgoing:  make(map[string]*outgoingTransfer),
		incoming:  make(map[string]*incomingTransfer),
	}
}

func (m *transferManager) SessionID() string {
	if m == nil {
		return ""
	}
	return m.sessionID
}

func createFileStubManifest(entryID, transferID, sessionID, sourceDevice string, paths []string) protocol.FileStubManifest {
	return createFileStubManifestWithKind(entryID, transferID, sessionID, sourceDevice, "", paths)
}

func createFileStubManifestWithKind(entryID, transferID, sessionID, sourceDevice string, kindOverride string, paths []string) protocol.FileStubManifest {
	normalized := append([]string(nil), paths...)
	topLevelNames := make([]string, 0, len(normalized))
	totalBytes := int64(0)
	kind := protocol.FileKindMultiFile
	if len(normalized) == 1 {
		kind = protocol.FileKindSingleFile
		if info, err := os.Stat(normalized[0]); err == nil && info.IsDir() {
			kind = protocol.FileKindFolder
		}
	}
	if kindOverride != "" {
		kind = kindOverride
	}
	for _, path := range normalized {
		name := filepath.Base(path)
		if name == "" || name == "." || name == string(os.PathSeparator) {
			name = "unknown"
		}
		topLevelNames = append(topLevelNames, name)
		totalBytes += estimatePathBytes(path)
	}
	displayName := strings.Join(topLevelNames, ", ")
	if len(topLevelNames) > 1 {
		displayName = fmt.Sprintf("%s and %d more", topLevelNames[0], len(topLevelNames)-1)
	}
	return protocol.FileStubManifest{
		ProtocolVersion:     protocol.FileProtocolVersion,
		EntryID:             entryID,
		TransferID:          transferID,
		SourceSessionID:     sessionID,
		SourceDevice:        sourceDevice,
		Kind:                kind,
		ArchiveFormat:       archiveFormatForKind(kind),
		DisplayName:         displayName,
		EntryCount:          len(normalized),
		TopLevelNames:       topLevelNames,
		EstimatedTotalBytes: totalBytes,
	}
}

func archiveFormatForKind(kind string) string {
	switch kind {
	case protocol.FileKindSingleFile, protocol.FileKindImage:
		return "raw"
	default:
		return "zip"
	}
}

func usesRawTransfer(manifest protocol.FileStubManifest) bool {
	return strings.EqualFold(strings.TrimSpace(manifest.ArchiveFormat), "raw")
}

func transferPayloadTempName(manifest protocol.FileStubManifest) string {
	if usesRawTransfer(manifest) {
		return fileTransferRawPayload
	}
	return fileTransferArchive
}

func replayTempRootDir() string {
	return filepath.Join(os.TempDir(), "ClipCascade")
}

func rawTransferSourcePath(sourcePaths []string) (string, error) {
	if len(sourcePaths) != 1 {
		return "", fmt.Errorf("raw transfer requires exactly one source path, got %d", len(sourcePaths))
	}
	source := sourcePaths[0]
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("raw transfer does not support directory source %s", source)
	}
	return source, nil
}

func (a *Application) ensureReplayTargetPaths(item *history.HistoryItem) (*history.HistoryItem, error) {
	if item == nil {
		return nil, ErrNoActiveHistoryItem
	}
	if len(item.ReservedPaths) > 0 {
		return item, nil
	}
	if a == nil || a.history == nil {
		return nil, ErrNoActiveHistoryItem
	}

	paths, err := plannedReplayTargetsForHistoryItem(item)
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

func plannedReplayTargetsForHistoryItem(item *history.HistoryItem) ([]string, error) {
	if item == nil {
		return nil, ErrNoActiveHistoryItem
	}
	if item.Type == constants.TypeImage {
		return plannedReplayTargetsForImageItem(item)
	}

	manifest, err := protocol.DecodePayload[protocol.FileStubManifest](item.Payload)
	if err != nil || manifest == nil {
		return nil, fmt.Errorf("decode file manifest: %w", err)
	}
	return plannedReplayTargetsForManifest(manifest, item.CreatedAt)
}

func plannedReplayTargetsForImageItem(item *history.HistoryItem) ([]string, error) {
	timestamp := item.CreatedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	tempDir := replayTempRootDir()
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}

	name := strings.TrimSpace(item.FileName)
	if name == "" {
		// 截图或无名图片使用时间戳命名
		name = fmt.Sprintf("%s.png", timestamp.Format("20060102150405"))
	} else {
		name = sanitizeFileName(name)
		if filepath.Ext(name) == "" {
			name += ".png"
		}
	}
	// 幂等：直接使用原文件名，同名时覆盖写入
	destPath, err := safeJoinUnderBase(tempDir, name)
	if err != nil {
		return nil, err
	}
	return []string{destPath}, nil
}

func plannedReplayTargetsForManifest(manifest *protocol.FileStubManifest, createdAt time.Time) ([]string, error) {
	if manifest == nil {
		return nil, errors.New("nil file manifest")
	}
	timestamp := createdAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	tempDir := replayTempRootDir()
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}

	var candidate string
	switch manifest.Kind {
	case protocol.FileKindSingleFile:
		candidate = sanitizeFileName(manifestPrimaryName(manifest))
		if candidate == "" {
			candidate = fmt.Sprintf("file_%s.bin", timestamp.Format("20060102150405"))
		}
	case protocol.FileKindFolder:
		candidate = sanitizeFileName(manifestPrimaryName(manifest))
		if candidate == "" {
			candidate = fmt.Sprintf("folder_%s", timestamp.Format("20060102150405"))
		}
	default:
		candidate = replayTimestampDirName(timestamp)
	}

	// 幂等：直接使用原文件名，覆盖写入
	destPath, err := safeJoinUnderBase(tempDir, candidate)
	if err != nil {
		return nil, err
	}
	return []string{destPath}, nil
}

func replayTimestampDirName(timestamp time.Time) string {
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return fmt.Sprintf("%s_%09d", timestamp.Format("20060102150405"), timestamp.Nanosecond())
}

func estimatePathBytes(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func sourcePathsSignature(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("no source paths for signature")
	}
	normalized := append([]string(nil), paths...)
	for i, source := range normalized {
		normalized[i] = filepath.Clean(source)
	}
	sort.Strings(normalized)

	hash := sha256.New()
	for _, source := range normalized {
		if err := appendSourcePathSignature(hash, source, filepath.Base(source)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func appendSourcePathSignature(dst io.Writer, sourcePath, relName string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink not supported in transfer: %s", sourcePath)
	}

	normalizedRel := filepath.ToSlash(relName)
	if info.IsDir() {
		if _, err := fmt.Fprintf(dst, "D\x00%s\x00", normalizedRel); err != nil {
			return err
		}
		entries, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := appendSourcePathSignature(dst, filepath.Join(sourcePath, entry.Name()), filepath.Join(relName, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := fmt.Fprintf(dst, "F\x00%s\x00%d\x00", normalizedRel, info.Size()); err != nil {
		return err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(dst, file); err != nil {
		return err
	}
	_, err = io.WriteString(dst, "\x00")
	return err
}

func (m *transferManager) RegisterOutgoing(paths []string, sourceDevice string) (*outgoingTransfer, error) {
	return m.RegisterOutgoingWithKind(paths, sourceDevice, "")
}

func (m *transferManager) RegisterOutgoingWithKind(paths []string, sourceDevice string, kindOverride string) (*outgoingTransfer, error) {
	if len(paths) == 0 {
		return nil, errors.New("no source paths for outgoing transfer")
	}
	signature, err := sourcePathsSignature(paths)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	for _, existing := range m.outgoing {
		if existing == nil {
			continue
		}
		if existing.SourceSignature == signature {
			m.mu.Unlock()
			return existing, nil
		}
	}
	m.mu.Unlock()

	transferID := uuid.NewString()
	entryID := uuid.NewString()
	manifest := createFileStubManifestWithKind(entryID, transferID, m.sessionID, sourceDevice, kindOverride, paths)
	transfer := &outgoingTransfer{
		Manifest:        manifest,
		SourcePaths:     append([]string(nil), paths...),
		SourceSignature: signature,
		sendingTargets:  make(map[string]struct{}),
	}

	m.mu.Lock()
	m.outgoing[transferID] = transfer
	m.mu.Unlock()
	return transfer, nil
}

func (m *transferManager) GetOutgoing(transferID string) *outgoingTransfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	transfer := m.outgoing[transferID]
	if transfer == nil {
		return nil
	}
	return &outgoingTransfer{
		Manifest:        transfer.Manifest,
		SourcePaths:     append([]string(nil), transfer.SourcePaths...),
		ArchiveMode:     transfer.ArchiveMode,
		ArchiveBytes:    append([]byte(nil), transfer.ArchiveBytes...),
		BaseDir:         transfer.BaseDir,
		ArchivePath:     transfer.ArchivePath,
		ArchiveSHA256:   transfer.ArchiveSHA256,
		ArchiveSize:     transfer.ArchiveSize,
		ChunkCount:      transfer.ChunkCount,
		SourceSignature: transfer.SourceSignature,
	}
}

func (m *transferManager) getOutgoingMutable(transferID string) *outgoingTransfer {
	return m.outgoing[transferID]
}

func (m *transferManager) RegisterIncoming(manifest protocol.FileStubManifest, historyItemID string, lastChunkIdx int) *incomingTransfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	transfer := &incomingTransfer{
		Manifest:      manifest,
		HistoryItemID: historyItemID,
		LastChunkIdx:  lastChunkIdx,
	}
	m.incoming[manifest.TransferID] = transfer
	return cloneIncomingTransfer(transfer)
}

func (m *transferManager) GetIncoming(transferID string) *incomingTransfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneIncomingTransfer(m.incoming[transferID])
}

func (m *transferManager) mutateIncoming(transferID string, fn func(*incomingTransfer) error) (*incomingTransfer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	transfer := m.incoming[transferID]
	if transfer == nil {
		return nil, errors.New("incoming transfer not found")
	}
	if err := fn(transfer); err != nil {
		return nil, err
	}
	return cloneIncomingTransfer(transfer), nil
}

func cloneIncomingTransfer(transfer *incomingTransfer) *incomingTransfer {
	if transfer == nil {
		return nil
	}
	copyTransfer := *transfer
	copyTransfer.Manifest.TopLevelNames = append([]string(nil), transfer.Manifest.TopLevelNames...)
	copyTransfer.ArchiveBytes = append([]byte(nil), transfer.ArchiveBytes...)
	return &copyTransfer
}

func newTransferTempDir(transferID string) (string, error) {
	prefix := fileTransferTempPrefix
	if transferID != "" {
		shortID := transferID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		prefix += shortID + "-"
	}
	return os.MkdirTemp("", prefix)
}

func cleanupExpiredTransferTempDirs(olderThan time.Duration) {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), fileTransferTempPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
	}
}

func normalizeArchiveMode(mode string) transferArchiveMode {
	switch transferArchiveMode(mode) {
	case transferArchiveModeMemory:
		return transferArchiveModeMemory
	default:
		return transferArchiveModeDisk
	}
}

func (m transferArchiveMode) String() string {
	if m == "" {
		return string(transferArchiveModeDisk)
	}
	return string(m)
}

func (m *transferManager) scheduleOutgoingArchiveCleanup(transferID string, delay time.Duration) {
	m.mu.Lock()
	transfer := m.outgoing[transferID]
	if transfer == nil {
		m.mu.Unlock()
		return
	}
	if transfer.cleanupTimer != nil {
		transfer.cleanupTimer.Stop()
	}
	transfer.cleanupGen++
	gen := transfer.cleanupGen
	transfer.cleanupTimer = time.AfterFunc(delay, func() {
		var baseDir string
		m.mu.Lock()
		stored := m.outgoing[transferID]
		if stored == nil || stored.cleanupGen != gen {
			m.mu.Unlock()
			return
		}
		baseDir = stored.BaseDir
		stored.ArchiveMode = transferArchiveModeDisk
		stored.ArchiveBytes = nil
		stored.BaseDir = ""
		stored.ArchivePath = ""
		stored.ArchiveSHA256 = ""
		stored.ArchiveSize = 0
		stored.ChunkCount = 0
		stored.cleanupTimer = nil
		m.mu.Unlock()
		if baseDir != "" {
			_ = os.RemoveAll(baseDir)
		}
	})
	m.mu.Unlock()
}

func (m *transferManager) releaseOutgoingArchive(transferID string) {
	var baseDir string
	m.mu.Lock()
	transfer := m.outgoing[transferID]
	if transfer != nil {
		if len(transfer.sendingTargets) > 0 {
			m.mu.Unlock()
			return
		}
		if transfer.cleanupTimer != nil {
			transfer.cleanupTimer.Stop()
			transfer.cleanupTimer = nil
		}
		baseDir = transfer.BaseDir
		transfer.cleanupGen++
		transfer.ArchiveMode = transferArchiveModeDisk
		transfer.ArchiveBytes = nil
		transfer.BaseDir = ""
		transfer.ArchivePath = ""
		transfer.ArchiveSHA256 = ""
		transfer.ArchiveSize = 0
		transfer.ChunkCount = 0
	}
	m.mu.Unlock()
	if baseDir != "" {
		_ = os.RemoveAll(baseDir)
	}
}

func (m *transferManager) deleteIncomingArchive(transferID string) {
	var archivePath string
	m.mu.Lock()
	transfer := m.incoming[transferID]
	if transfer != nil {
		archivePath = transfer.ArchivePath
		transfer.ArchivePath = ""
		transfer.ArchiveBytes = nil
	}
	m.mu.Unlock()
	if archivePath != "" {
		_ = os.Remove(archivePath)
	}
}

func (a *Application) appSessionID() string {
	if a == nil {
		return ""
	}
	if a.sessionID != "" {
		return a.sessionID
	}
	if a.transfers == nil {
		return ""
	}
	return a.transfers.SessionID()
}

func (a *Application) handleFileTransferMessage(clipData *protocol.ClipboardData) (bool, error) {
	if clipData == nil {
		return true, nil
	}
	switch clipData.Type {
	case constants.TypeFileRequest:
		request, err := protocol.DecodePayload[protocol.FileRequest](clipData.Payload)
		if err != nil {
			return true, fmt.Errorf("decode file_request: %w", err)
		}
		// 文件发送是 IO 密集型操作，必须异步执行，不能阻塞 WebSocket 读循环。
		// 否则 readLoop 长时间不消费新消息，导致心跳超时 + 连接死亡。
		// 注意：不能用 TargetSessionID 过滤，因为 TargetSessionID 是请求者 ID，
		// 自发自收场景下 TargetSessionID 就是自己的 session ID。
		// handleFileRequest 内部通过 getOutgoingMutable 返回 nil 天然过滤非发送端。
		go func() {
			if err := a.handleFileRequest(request); err != nil {
				slog.Warn("应用：处理文件传输请求失败", "transfer_id", request.TransferID, "error", err)
			}
		}()
		return true, nil
	case constants.TypeFileChunk:
		chunk, err := protocol.DecodePayload[protocol.FileChunk](clipData.Payload)
		if err != nil {
			return true, fmt.Errorf("decode file_chunk: %w", err)
		}
		if chunk.TargetSessionID != a.appSessionID() {
			return true, nil
		}
		return true, a.handleFileChunk(chunk)
	case constants.TypeFileComplete:
		complete, err := protocol.DecodePayload[protocol.FileComplete](clipData.Payload)
		if err != nil {
			return true, fmt.Errorf("decode file_complete: %w", err)
		}
		if complete.TargetSessionID != a.appSessionID() {
			return true, nil
		}
		// handleFileComplete 可能执行落盘、状态更新和剪贴板写回等操作，
		// 需要异步执行，避免阻塞 WebSocket 读循环。
		go func() {
			if err := a.handleFileComplete(complete); err != nil {
				slog.Warn("应用：处理文件传输完成失败", "transfer_id", complete.TransferID, "error", err)
			}
		}()
		return true, nil
	case constants.TypeFileError:
		fileErr, err := protocol.DecodePayload[protocol.FileError](clipData.Payload)
		if err != nil {
			return true, fmt.Errorf("decode file_error: %w", err)
		}
		if fileErr.TargetSessionID != a.appSessionID() {
			return true, nil
		}
		return true, a.handleFileError(fileErr)
	case constants.TypeFileRelease:
		release, err := protocol.DecodePayload[protocol.FileRelease](clipData.Payload)
		if err != nil {
			return true, fmt.Errorf("decode file_release: %w", err)
		}
		return true, a.handleFileRelease(release)
	default:
		return false, nil
	}
}

func (a *Application) requestFileTransfer(item *history.HistoryItem) error {
	if item == nil {
		return ErrNoActiveHistoryItem
	}
	manifest, err := protocol.DecodePayload[protocol.FileStubManifest](item.Payload)
	if err != nil {
		return fmt.Errorf("decode file manifest: %w", err)
	}
	resumeFrom := 0
	if item.State == history.StateFailed && item.LastChunkIdx >= 0 {
		resumeFrom = item.LastChunkIdx + 1
	}
	requestAlreadyActive := false
	if _, err := a.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
		incoming.Manifest = *manifest
		incoming.RequestedResumeFrom = resumeFrom
		if incoming.RequestActive {
			requestAlreadyActive = true
			return nil
		}
		incoming.RequestActive = true
		return nil
	}); err != nil {
		a.transfers.RegisterIncoming(*manifest, item.ID, item.LastChunkIdx)
		_, _ = a.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
			incoming.RequestedResumeFrom = resumeFrom
			incoming.RequestActive = true
			return nil
		})
	}
	if requestAlreadyActive {
		slog.Debug("应用：跳过重复的文件传输请求，已有请求进行中", "transfer_id", manifest.TransferID)
		return nil
	}

	request := protocol.FileRequest{
		TransferID:      manifest.TransferID,
		EntryID:         manifest.EntryID,
		TargetSessionID: a.appSessionID(),
		ResumeFromChunk: resumeFrom,
	}
	data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileRequest, request)
	if err != nil {
		return fmt.Errorf("encode file request: %w", err)
	}
	prevState := item.State
	if !a.history.UpdateState(item.ID, history.StateDownloading) && item.State != history.StateDownloading {
		_, _ = a.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
			incoming.RequestActive = false
			return nil
		})
		return fmt.Errorf("update item %s to downloading", item.ID)
	}
	attrs := []any{
		"transfer_id", manifest.TransferID,
		"resume_from_chunk", resumeFrom,
	}
	if names := historyItemLogNames(item); names != "" {
		attrs = append(attrs, "文件", names)
	}
	slog.Info("应用：开始请求文件传输", attrs...)
	if err := a.sendClipboardData(data); err != nil {
		_, _ = a.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
			incoming.RequestActive = false
			return nil
		})
		if prevState != history.StateDownloading {
			_, _ = a.history.Mutate(item.ID, func(next *history.HistoryItem) error {
				next.State = prevState
				return nil
			})
		}
		return err
	}
	return nil
}

func (a *Application) handleFileRequest(request *protocol.FileRequest) error {
	if request == nil || request.TransferID == "" {
		return nil
	}

	// 防止同一 transfer 的并发发送（快速连按热键或多接收端同时请求同一文件）
	a.transfers.mu.Lock()
	transfer := a.transfers.getOutgoingMutable(request.TransferID)
	if transfer == nil {
		a.transfers.mu.Unlock()
		slog.Debug("应用：收到 file_request 但无对应 outgoing transfer（非本机发送的文件）",
			"transfer_id", request.TransferID)
		return nil
	}
	if transfer.sendingTargets == nil {
		transfer.sendingTargets = make(map[string]struct{})
	}
	if _, exists := transfer.sendingTargets[request.TargetSessionID]; exists {
		a.transfers.mu.Unlock()
		slog.Debug("应用：跳过重复的文件传输请求，目标端已有发送进行中",
			"transfer_id", request.TransferID,
			"target_session_id", request.TargetSessionID)
		return nil
	}
	transfer.sendingTargets[request.TargetSessionID] = struct{}{}
	a.transfers.mu.Unlock()
	defer func() {
		a.transfers.mu.Lock()
		if t := a.transfers.getOutgoingMutable(request.TransferID); t != nil {
			delete(t.sendingTargets, request.TargetSessionID)
		}
		a.transfers.mu.Unlock()
	}()

	transfer.prepareMu.Lock()
	resumeFrom := request.ResumeFromChunk
	if !hasReusableOutgoingArchive(transfer) {
		resumeFrom = 0
	}
	if err := a.prepareOutgoingArchive(transfer); err != nil {
		transfer.prepareMu.Unlock()
		return a.sendFileError(request.TransferID, request.TargetSessionID, "archive_failed", err.Error(), true)
	}
	transfer.prepareMu.Unlock()
	attrs := []any{
		"transfer_id", request.TransferID,
		"target_session_id", request.TargetSessionID,
		"resume_from_chunk", resumeFrom,
		"archive_mode", transfer.ArchiveMode,
		"archive_sha256", transfer.ArchiveSHA256,
		"archive_size", transfer.ArchiveSize,
		"chunk_count", transfer.ChunkCount,
		"archive_bytes_len", len(transfer.ArchiveBytes),
		"archive_path", transfer.ArchivePath,
		"manifest_kind", transfer.Manifest.Kind,
		"manifest_archive_format", transfer.Manifest.ArchiveFormat,
		"uses_raw", usesRawTransfer(transfer.Manifest),
	}
	if names := protocol.BracketedNames(protocol.ManifestNames(&transfer.Manifest)); names != "" {
		attrs = append(attrs, "文件", names)
	}
	slog.Info("应用：开始发送文件内容", attrs...)
	if err := a.sendArchiveChunks(transfer, request.TargetSessionID, resumeFrom); err != nil {
		return a.sendFileError(request.TransferID, request.TargetSessionID, "send_failed", err.Error(), true)
	}
	complete := protocol.FileComplete{
		TransferID:       request.TransferID,
		TargetSessionID:  request.TargetSessionID,
		ArchiveMode:      transfer.ArchiveMode.String(),
		ArchiveSHA256:    transfer.ArchiveSHA256,
		ActualTotalBytes: transfer.ArchiveSize,
	}
	data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileComplete, complete)
	if err != nil {
		return err
	}
	if err := a.sendClipboardData(data); err != nil {
		return err
	}
	a.transfers.scheduleOutgoingArchiveCleanup(request.TransferID, fileTransferArchiveRetention)
	return nil
}

func hasReusableOutgoingArchive(transfer *outgoingTransfer) bool {
	if transfer == nil {
		return false
	}
	if len(transfer.ArchiveBytes) > 0 {
		return true
	}
	if transfer.ArchivePath == "" {
		return false
	}
	_, err := os.Stat(transfer.ArchivePath)
	return err == nil
}

func (a *Application) prepareOutgoingArchive(transfer *outgoingTransfer) error {
	if transfer.cleanupTimer != nil {
		transfer.cleanupTimer.Stop()
		transfer.cleanupTimer = nil
	}
	transfer.cleanupGen++
	if len(transfer.ArchiveBytes) > 0 {
		transfer.ArchiveMode = transferArchiveModeMemory
		return nil
	}
	if transfer.ArchivePath != "" {
		if _, err := os.Stat(transfer.ArchivePath); err == nil {
			transfer.ArchiveMode = transferArchiveModeDisk
			return nil
		}
	}

	if a.canUseMemoryArchive(transfer.Manifest.EstimatedTotalBytes) {
		if err := a.prepareOutgoingArchiveInMemory(transfer); err == nil {
			return nil
		} else if !errors.Is(err, errMemoryArchiveTooLarge) {
			slog.Warn("application: memory archive preparation failed, falling back to disk", "transfer_id", transfer.Manifest.TransferID, "error", err)
		}
	}

	return a.prepareOutgoingArchiveOnDisk(transfer)
}

func (a *Application) canUseMemoryArchive(estimatedBytes int64) bool {
	if a == nil || a.p2p == nil || a.p2p.ReadyPeerCount() == 0 {
		return false
	}
	if estimatedBytes > a.fileTransferMemoryThresholdBytes() {
		return false
	}
	return true
}

func (a *Application) prepareOutgoingArchiveInMemory(transfer *outgoingTransfer) error {
	thresholdBytes := a.fileTransferMemoryThresholdBytes()
	if usesRawTransfer(transfer.Manifest) {
		source, err := rawTransferSourcePath(transfer.SourcePaths)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if int64(len(data)) > thresholdBytes {
			return errMemoryArchiveTooLarge
		}
		sum := sha256.Sum256(data)
		transfer.ArchiveMode = transferArchiveModeMemory
		transfer.ArchiveBytes = append([]byte(nil), data...)
		transfer.BaseDir = ""
		transfer.ArchivePath = ""
		transfer.ArchiveSHA256 = fmt.Sprintf("%x", sum[:])
		transfer.ArchiveSize = int64(len(data))
		transfer.ChunkCount = chunkCountForSize(transfer.ArchiveSize)
		return nil
	}

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, source := range transfer.SourcePaths {
		if err := addPathToZip(writer, source, filepath.Base(source)); err != nil {
			_ = writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if int64(buf.Len()) > thresholdBytes {
		return errMemoryArchiveTooLarge
	}
	archiveBytes := append([]byte(nil), buf.Bytes()...)
	sum := sha256.Sum256(archiveBytes)
	transfer.ArchiveMode = transferArchiveModeMemory
	transfer.ArchiveBytes = archiveBytes
	transfer.BaseDir = ""
	transfer.ArchivePath = ""
	transfer.ArchiveSHA256 = fmt.Sprintf("%x", sum[:])
	transfer.ArchiveSize = int64(len(archiveBytes))
	transfer.ChunkCount = chunkCountForSize(transfer.ArchiveSize)
	return nil
}

func (a *Application) prepareOutgoingArchiveOnDisk(transfer *outgoingTransfer) error {
	if transfer.BaseDir == "" {
		baseDir, err := newTransferTempDir(transfer.Manifest.TransferID)
		if err != nil {
			return err
		}
		transfer.BaseDir = baseDir
	}
	if err := os.MkdirAll(transfer.BaseDir, 0o755); err != nil {
		return err
	}

	archivePath := filepath.Join(transfer.BaseDir, transferPayloadTempName(transfer.Manifest))
	if usesRawTransfer(transfer.Manifest) {
		source, err := rawTransferSourcePath(transfer.SourcePaths)
		if err != nil {
			return err
		}
		if err := copyFile(source, archivePath); err != nil {
			return err
		}
	} else {
		file, err := os.Create(archivePath)
		if err != nil {
			return err
		}
		defer file.Close()
		writer := zip.NewWriter(file)
		for _, source := range transfer.SourcePaths {
			if err := addPathToZip(writer, source, filepath.Base(source)); err != nil {
				_ = writer.Close()
				return err
			}
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
	sum, size, err := fileSHA256(archivePath)
	if err != nil {
		return err
	}
	transfer.ArchiveMode = transferArchiveModeDisk
	transfer.ArchiveBytes = nil
	transfer.ArchivePath = archivePath
	transfer.ArchiveSHA256 = sum
	transfer.ArchiveSize = size
	transfer.ChunkCount = chunkCountForSize(size)
	return nil
}

func addPathToZip(writer *zip.Writer, sourcePath string, zipName string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink not supported in transfer: %s", sourcePath)
	}
	if info.IsDir() {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(zipName) + "/"
		if _, err := writer.CreateHeader(header); err != nil {
			return err
		}
		entries, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := addPathToZip(writer, filepath.Join(sourcePath, entry.Name()), filepath.Join(zipName, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(zipName)
	header.Method = zip.Deflate
	w, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

func fileSHA256(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), size, nil
}

func chunkCountForSize(size int64) int {
	if size <= 0 {
		return 0
	}
	chunks := int(size / fileTransferChunkSize)
	if size%fileTransferChunkSize != 0 {
		chunks++
	}
	return chunks
}

func fileChunkAAD(transferID string, chunkIndex int) []byte {
	// 绑定 transfer_id 和 chunk_index，防止分片被跨传输或跨位置复用。
	return []byte(fmt.Sprintf("%s|%d", transferID, chunkIndex))
}

func (a *Application) sendArchiveChunks(transfer *outgoingTransfer, targetSessionID string, resumeFrom int) error {
	reader, err := a.archiveChunkReader(transfer, resumeFrom)
	if err != nil {
		return err
	}
	defer reader.Close()
	buffer := make([]byte, fileTransferChunkSize)
	for chunkIndex := resumeFrom; ; chunkIndex++ {
		n, readErr := io.ReadFull(reader, buffer)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			// use partial chunk
		} else if readErr != nil {
			return readErr
		}
		raw := append([]byte(nil), buffer[:n]...)
		if len(raw) == 0 {
			break
		}
		sum := sha256.Sum256(raw)
		encodedChunk := base64.StdEncoding.EncodeToString(raw)
		if a != nil && a.cfg != nil && a.cfg.E2EEEnabled && a.encKey != nil {
			encrypted, err := pkgcrypto.EncryptWithAAD(a.encKey, raw, fileChunkAAD(transfer.Manifest.TransferID, chunkIndex))
			if err != nil {
				return fmt.Errorf("encrypt chunk %d: %w", chunkIndex, err)
			}
			encodedChunk, err = pkgcrypto.EncodeToJSONString(encrypted)
			if err != nil {
				return fmt.Errorf("encode encrypted chunk %d: %w", chunkIndex, err)
			}
		}
		chunk := protocol.FileChunk{
			TransferID:      transfer.Manifest.TransferID,
			TargetSessionID: targetSessionID,
			ArchiveMode:     transfer.ArchiveMode.String(),
			ChunkIndex:      chunkIndex,
			TotalChunks:     transfer.ChunkCount,
			ChunkData:       encodedChunk,
			ChunkSHA256:     fmt.Sprintf("%x", sum[:]),
		}
		data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileChunk, chunk)
		if err != nil {
			return err
		}
		if err := a.sendClipboardData(data); err != nil {
			return err
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil
}

func (a *Application) archiveChunkReader(transfer *outgoingTransfer, resumeFrom int) (io.ReadCloser, error) {
	offset := int64(resumeFrom) * fileTransferChunkSize
	if len(transfer.ArchiveBytes) > 0 {
		reader := bytes.NewReader(transfer.ArchiveBytes)
		if offset > 0 {
			if _, err := reader.Seek(offset, io.SeekStart); err != nil {
				return nil, err
			}
		}
		return io.NopCloser(reader), nil
	}

	file, err := os.Open(transfer.ArchivePath)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
	}
	return &fileChunkReader{file: file}, nil
}

type fileChunkReader struct {
	file *os.File
}

func (r *fileChunkReader) Read(p []byte) (int, error) {
	return r.file.Read(p)
}

func (r *fileChunkReader) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

func (a *Application) sendFileError(transferID, targetSessionID, code, message string, retryable bool) error {
	data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileError, protocol.FileError{
		TransferID:      transferID,
		TargetSessionID: targetSessionID,
		ErrorCode:       code,
		ErrorMessage:    message,
		Retryable:       retryable,
	})
	if err != nil {
		return err
	}
	return a.sendClipboardData(data)
}

func (a *Application) sendFileRelease(transferID, targetSessionID, reason string) error {
	data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileRelease, protocol.FileRelease{
		TransferID:      transferID,
		TargetSessionID: targetSessionID,
		ReleaseReason:   reason,
	})
	if err != nil {
		return err
	}
	return a.sendClipboardData(data)
}

func (a *Application) handleFileChunk(chunk *protocol.FileChunk) error {
	if chunk == nil {
		return nil
	}
	duplicateChunk := false
	transfer, err := a.transfers.mutateIncoming(chunk.TransferID, func(transfer *incomingTransfer) error {
		if transfer.Completed {
			duplicateChunk = true
			return nil
		}
		chunkMode := normalizeArchiveMode(chunk.ArchiveMode)
		if transfer.BaseDir == "" {
			baseDir, err := newTransferTempDir(chunk.TransferID)
			if err != nil {
				return err
			}
			transfer.BaseDir = baseDir
			transfer.ExtractDir = filepath.Join(baseDir, fileTransferExtracted)
			transfer.ArchiveMode = chunkMode
			transfer.StorageMode = chunkMode
			if transfer.StorageMode == transferArchiveModeDisk {
				transfer.ArchivePath = filepath.Join(baseDir, transferPayloadTempName(transfer.Manifest))
			}
		}
		if transfer.ArchiveMode == "" {
			transfer.ArchiveMode = chunkMode
		}
		if transfer.StorageMode == "" {
			transfer.StorageMode = chunkMode
		}
		expectedIdx := transfer.LastChunkIdx + 1
		if chunk.ChunkIndex == 0 && transfer.RequestedResumeFrom > 0 {
			expectedIdx = 0
		}
		if chunk.ChunkIndex < expectedIdx {
			duplicateChunk = true
			return nil
		}
		if transfer.ArchiveMode != chunkMode && chunk.ChunkIndex != 0 {
			return fmt.Errorf("unexpected archive mode change %q -> %q", transfer.ArchiveMode, chunkMode)
		}
		if chunk.ChunkIndex == 0 {
			transfer.ArchiveMode = chunkMode
			transfer.StorageMode = chunkMode
			transfer.LastChunkIdx = -1
			transfer.TotalChunks = 0
			transfer.ArchiveBytes = nil
			if transfer.ArchivePath == "" && transfer.StorageMode == transferArchiveModeDisk {
				transfer.ArchivePath = filepath.Join(transfer.BaseDir, transferPayloadTempName(transfer.Manifest))
			} else if transfer.StorageMode == transferArchiveModeMemory && transfer.ArchivePath != "" {
				_ = os.Remove(transfer.ArchivePath)
			}
		}
		if chunk.ChunkIndex != expectedIdx {
			return fmt.Errorf("unexpected chunk index %d, want %d", chunk.ChunkIndex, expectedIdx)
		}
		var raw []byte
		var decodeErr error
		if a != nil && a.cfg != nil && a.cfg.E2EEEnabled && a.encKey != nil {
			encrypted, encErr := pkgcrypto.DecodeFromJSONString(chunk.ChunkData)
			if encErr != nil {
				return fmt.Errorf("decode encrypted chunk %d: %w", chunk.ChunkIndex, encErr)
			}
			raw, decodeErr = pkgcrypto.DecryptWithAAD(a.encKey, encrypted, fileChunkAAD(chunk.TransferID, chunk.ChunkIndex))
			if decodeErr != nil {
				return fmt.Errorf("decrypt chunk %d: %w", chunk.ChunkIndex, decodeErr)
			}
		} else {
			raw, decodeErr = base64.StdEncoding.DecodeString(chunk.ChunkData)
			if decodeErr != nil {
				return decodeErr
			}
		}
		sum := sha256.Sum256(raw)
		if fmt.Sprintf("%x", sum[:]) != chunk.ChunkSHA256 {
			return errors.New("chunk sha256 mismatch")
		}

		if transfer.StorageMode == transferArchiveModeMemory {
			thresholdBytes := a.fileTransferMemoryThresholdBytes()
			if int64(len(transfer.ArchiveBytes)+len(raw)) <= thresholdBytes {
				transfer.ArchiveBytes = append(transfer.ArchiveBytes, raw...)
			} else {
				if transfer.ArchivePath == "" {
					transfer.ArchivePath = filepath.Join(transfer.BaseDir, transferPayloadTempName(transfer.Manifest))
				}
				if err := writeArchiveBytesToDisk(transfer.ArchivePath, transfer.ArchiveBytes, true); err != nil {
					return err
				}
				transfer.ArchiveBytes = nil
				transfer.StorageMode = transferArchiveModeDisk
				if err := appendArchiveChunkToDisk(transfer.ArchivePath, raw, false); err != nil {
					return err
				}
			}
		} else {
			truncate := chunk.ChunkIndex == 0
			if err := appendArchiveChunkToDisk(transfer.ArchivePath, raw, truncate); err != nil {
				return err
			}
		}
		transfer.LastChunkIdx = chunk.ChunkIndex
		transfer.TotalChunks = chunk.TotalChunks
		transfer.RequestedResumeFrom = 0
		return nil
	})
	if err != nil {
		return a.failIncomingTransfer(chunk.TransferID, err)
	}
	if duplicateChunk {
		slog.Debug("应用：忽略重复的文件分片", "transfer_id", chunk.TransferID, "chunk_index", chunk.ChunkIndex)
		return nil
	}
	_, _ = a.history.MutateByTransferID(chunk.TransferID, func(item *history.HistoryItem) error {
		item.LastChunkIdx = transfer.LastChunkIdx
		if item.State != history.StateDownloading {
			item.State = history.StateDownloading
		}
		return nil
	})
	return nil
}

func (a *Application) fileTransferMemoryThresholdBytes() int64 {
	if a == nil || a.cfg == nil {
		return int64(constants.DefaultFileMemoryThresholdMiB) << 20
	}
	return a.cfg.FileMemoryThresholdBytes()
}

func (a *Application) handleFileComplete(complete *protocol.FileComplete) error {
	if complete == nil {
		return nil
	}
	duplicateComplete := false
	transfer, err := a.transfers.mutateIncoming(complete.TransferID, func(transfer *incomingTransfer) error {
		if transfer.Completed || transfer.Completing {
			duplicateComplete = true
			return nil
		}
		transfer.Completing = true
		return nil
	})
	if err != nil || transfer == nil {
		return nil
	}
	if duplicateComplete {
		slog.Debug("应用：忽略重复的文件传输完成消息", "transfer_id", complete.TransferID)
		return nil
	}
	sum, size, err := incomingArchiveSHA256(transfer)
	if err != nil {
		slog.Warn("application: incomingArchiveSHA256 failed",
			"transfer_id", complete.TransferID,
			"error", err,
			"storage_mode", transfer.StorageMode,
			"archive_mode", transfer.ArchiveMode,
			"archive_bytes_len", len(transfer.ArchiveBytes),
			"archive_path", transfer.ArchivePath,
			"last_chunk_idx", transfer.LastChunkIdx,
			"total_chunks", transfer.TotalChunks,
			"uses_raw", usesRawTransfer(transfer.Manifest),
			"manifest_kind", transfer.Manifest.Kind,
			"manifest_archive_format", transfer.Manifest.ArchiveFormat,
		)
		return a.failIncomingTransfer(complete.TransferID, err)
	}
	if sum != complete.ArchiveSHA256 {
		slog.Warn("application: archive sha256 mismatch diagnostic",
			"transfer_id", complete.TransferID,
			"local_sha256", sum,
			"remote_sha256", complete.ArchiveSHA256,
			"local_size", size,
			"remote_size", complete.ActualTotalBytes,
			"storage_mode", transfer.StorageMode,
			"archive_mode", transfer.ArchiveMode,
			"remote_archive_mode", complete.ArchiveMode,
			"archive_bytes_len", len(transfer.ArchiveBytes),
			"archive_path", transfer.ArchivePath,
			"last_chunk_idx", transfer.LastChunkIdx,
			"total_chunks", transfer.TotalChunks,
			"uses_raw", usesRawTransfer(transfer.Manifest),
			"manifest_kind", transfer.Manifest.Kind,
			"manifest_archive_format", transfer.Manifest.ArchiveFormat,
		)
		return a.failIncomingTransfer(complete.TransferID, errors.New("archive sha256 mismatch"))
	}
	if size != complete.ActualTotalBytes {
		return a.failIncomingTransfer(complete.TransferID, fmt.Errorf("archive size mismatch: got %d want %d", size, complete.ActualTotalBytes))
	}
	storedItem := a.history.GetByTransferID(complete.TransferID)
	if storedItem != nil && len(storedItem.ReservedPaths) == 0 {
		updatedItem, err := a.ensureReplayTargetPaths(storedItem)
		if err != nil {
			return a.failIncomingTransfer(complete.TransferID, err)
		}
		storedItem = updatedItem
	}
	localPaths, reusedExisting, err := tryReuseIncomingMaterializedPaths(transfer, storedItem, sum, size)
	if err != nil {
		return a.failIncomingTransfer(complete.TransferID, err)
	}
	if !reusedExisting {
		localPaths, err = materializeIncomingTransfer(transfer, storedItem)
		if err != nil {
			return a.failIncomingTransfer(complete.TransferID, err)
		}
	}
	updated, err := a.history.MutateByTransferID(complete.TransferID, func(item *history.HistoryItem) error {
		item.State = history.StateReadyToPaste
		item.LocalPaths = append([]string(nil), localPaths...)
		if len(item.ReservedPaths) == 0 {
			item.ReservedPaths = append([]string(nil), localPaths...)
		}
		// 文件完成后只进入 ready_to_paste，禁止隐式自动真实粘贴。
		item.PendingReplayMode = string(ReplayModeNone)
		item.LastChunkIdx = transfer.LastChunkIdx
		item.ErrorMessage = ""
		return nil
	})
	_, _ = a.transfers.mutateIncoming(complete.TransferID, func(incoming *incomingTransfer) error {
		incoming.RequestActive = false
		incoming.Completing = false
		incoming.Completed = true
		return nil
	})
	a.transfers.deleteIncomingArchive(complete.TransferID)
	if releaseErr := a.sendFileRelease(complete.TransferID, a.appSessionID(), "received_ok"); releaseErr != nil {
		slog.Warn("application: send file_release failed", "transfer_id", complete.TransferID, "error", releaseErr)
	}
	if err != nil {
		return err
	}
	if updated != nil {
		attrs := []any{
			"transfer_id", complete.TransferID,
		}
		if names := historyItemLogNames(updated); names != "" {
			attrs = append(attrs, "文件", names)
		}
		if targets := pathLogNames(updated.LocalPaths); targets != "" {
			attrs = append(attrs, "落地路径", targets)
		}
		if len(updated.ReservedPaths) > 0 {
			attrs = append(attrs, "占位路径", updated.ReservedPaths[0])
		}
		slog.Info("应用：文件传输已完成", attrs...)
	}
	return nil
}

func tryReuseIncomingMaterializedPaths(transfer *incomingTransfer, item *history.HistoryItem, archiveSHA256 string, archiveSize int64) ([]string, bool, error) {
	if transfer == nil || item == nil || !usesRawTransfer(transfer.Manifest) || len(item.ReservedPaths) == 0 {
		return nil, false, nil
	}
	targetPath, err := normalizeReplayTargetPath(item.ReservedPaths[0])
	if err != nil {
		return nil, false, err
	}
	match, err := fileMatchesArchive(targetPath, archiveSHA256, archiveSize)
	if err != nil {
		return nil, false, err
	}
	if !match {
		return nil, false, nil
	}
	slog.Info("应用：目标路径已存在同名同内容文件，直接复用",
		"transfer_id", transfer.Manifest.TransferID,
		"path", targetPath,
	)
	return []string{targetPath}, true, nil
}

func fileMatchesArchive(path string, archiveSHA256 string, archiveSize int64) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() || info.Size() != archiveSize {
		return false, nil
	}
	sum, size, err := fileSHA256(path)
	if err != nil {
		return false, err
	}
	return size == archiveSize && sum == archiveSHA256, nil
}

func writeArchiveBytesToDisk(path string, data []byte, truncate bool) error {
	flag := os.O_CREATE | os.O_WRONLY
	if truncate {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_APPEND
	}
	file, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if len(data) == 0 {
		return nil
	}
	_, err = file.Write(data)
	return err
}

func appendArchiveChunkToDisk(path string, raw []byte, truncate bool) error {
	return writeArchiveBytesToDisk(path, raw, truncate)
}

func incomingArchiveSHA256(transfer *incomingTransfer) (string, int64, error) {
	if transfer == nil {
		return "", 0, errors.New("incoming transfer not found")
	}
	if len(transfer.ArchiveBytes) > 0 {
		sum := sha256.Sum256(transfer.ArchiveBytes)
		return fmt.Sprintf("%x", sum[:]), int64(len(transfer.ArchiveBytes)), nil
	}
	if transfer.ArchivePath == "" && usesRawTransfer(transfer.Manifest) {
		sum := sha256.Sum256(nil)
		return fmt.Sprintf("%x", sum[:]), 0, nil
	}
	return fileSHA256(transfer.ArchivePath)
}

func extractIncomingArchiveSafely(transfer *incomingTransfer) ([]string, error) {
	if transfer == nil {
		return nil, errors.New("incoming transfer not found")
	}
	if err := ensureExtractDiskSpace(transfer); err != nil {
		return nil, err
	}
	if len(transfer.ArchiveBytes) > 0 {
		return extractZipBytesSafely(transfer.ArchiveBytes, transfer.ExtractDir)
	}
	return extractZipSafely(transfer.ArchivePath, transfer.ExtractDir)
}

func materializeIncomingTransfer(transfer *incomingTransfer, item *history.HistoryItem) ([]string, error) {
	if transfer == nil {
		return nil, errors.New("incoming transfer not found")
	}

	manifest := &transfer.Manifest
	reservedPaths := []string(nil)
	createdAt := time.Now()
	if item != nil {
		reservedPaths = append([]string(nil), item.ReservedPaths...)
		createdAt = item.CreatedAt
	}
	if len(reservedPaths) == 0 {
		paths, err := plannedReplayTargetsForManifest(manifest, createdAt)
		if err != nil {
			return nil, err
		}
		reservedPaths = paths
	}
	reservedPaths, err := normalizeReplayTargetPaths(reservedPaths)
	if err != nil {
		return nil, err
	}
	if usesRawTransfer(*manifest) {
		if len(reservedPaths) == 0 {
			return nil, errors.New("missing reserved path for raw file")
		}
		if err := ensureParentDir(reservedPaths[0]); err != nil {
			return nil, err
		}
		switch {
		case len(transfer.ArchiveBytes) > 0:
			if err := os.WriteFile(reservedPaths[0], transfer.ArchiveBytes, 0o644); err != nil {
				return nil, err
			}
		case transfer.ArchivePath != "":
			if err := moveFile(transfer.ArchivePath, reservedPaths[0]); err != nil {
				return nil, err
			}
		default:
			if err := os.WriteFile(reservedPaths[0], nil, 0o644); err != nil {
				return nil, err
			}
		}
		return reservedPaths, nil
	}

	switch manifest.Kind {
	case protocol.FileKindMultiFile:
		if len(reservedPaths) == 0 {
			return nil, errors.New("missing reserved directory for bundle")
		}
		targetDir := reservedPaths[0]
		if err := ensureParentDir(targetDir); err != nil {
			return nil, err
		}
		var extracted []string
		var err error
		if len(transfer.ArchiveBytes) > 0 {
			extracted, err = extractZipBytesSafely(transfer.ArchiveBytes, targetDir)
		} else {
			extracted, err = extractZipSafely(transfer.ArchivePath, targetDir)
		}
		if err != nil {
			return nil, err
		}
		if len(extracted) == 0 {
			return nil, fmt.Errorf("bundle extract produced no files in %s", targetDir)
		}
		return extracted, nil
	case protocol.FileKindFolder:
		if len(reservedPaths) == 0 {
			return nil, errors.New("missing reserved directory for folder")
		}
		targetDir := reservedPaths[0]
		parentDir := filepath.Dir(targetDir)
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return nil, err
		}
		var extracted []string
		var err error
		if len(transfer.ArchiveBytes) > 0 {
			extracted, err = extractZipBytesSafely(transfer.ArchiveBytes, parentDir)
		} else {
			extracted, err = extractZipSafely(transfer.ArchivePath, parentDir)
		}
		if err != nil {
			return nil, err
		}
		if len(extracted) != 1 {
			return nil, fmt.Errorf("expected single extracted folder, got %d", len(extracted))
		}
		if extracted[0] != targetDir {
			if err := movePath(extracted[0], targetDir); err != nil {
				return nil, err
			}
		}
		return []string{targetDir}, nil
	default:
		extracted, err := extractIncomingArchiveSafely(transfer)
		if err != nil {
			return nil, err
		}
		if len(extracted) != 1 {
			return nil, fmt.Errorf("expected single extracted path, got %d", len(extracted))
		}
		info, err := os.Stat(extracted[0])
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("expected file payload, got directory %s", extracted[0])
		}
		if len(reservedPaths) == 0 {
			return nil, errors.New("missing reserved path for file")
		}
		if err := moveFile(extracted[0], reservedPaths[0]); err != nil {
			return nil, err
		}
		return reservedPaths, nil
	}
}

func ensureParentDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func moveFile(src, dst string) error {
	if err := ensureParentDir(dst); err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func movePath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return moveFile(src, dst)
	}
	if err := ensureParentDir(dst); err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyDir(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := dst
		if rel != "." {
			targetPath = filepath.Join(dst, rel)
		}
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode().Perm())
		}
		return copyFile(path, targetPath)
	})
}

func copyFile(src, dst string) error {
	if err := ensureParentDir(dst); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// ensureExtractDiskSpace 在解压前检查目标目录所在磁盘是否有足够剩余空间。
func ensureExtractDiskSpace(transfer *incomingTransfer) error {
	requiredBytes := estimatedRequiredExtractBytes(transfer.Manifest.EstimatedTotalBytes)
	checkPath, err := diskSpaceCheckPath(transfer.ExtractDir)
	if err != nil {
		return fmt.Errorf("resolve disk space check path: %w", err)
	}
	availableBytes, err := availableDiskSpace(checkPath)
	if err != nil {
		return fmt.Errorf("check available disk space: %w", err)
	}
	if availableBytes < requiredBytes {
		return fmt.Errorf(
			"insufficient disk space for extraction: available=%d required=%d target=%s",
			availableBytes, requiredBytes, transfer.ExtractDir,
		)
	}
	return nil
}

// estimatedRequiredExtractBytes 根据预估解压大小增加 10% 安全余量。
func estimatedRequiredExtractBytes(estimatedTotalBytes int64) uint64 {
	if estimatedTotalBytes <= 0 {
		return 0
	}
	requiredBytes := uint64(estimatedTotalBytes)
	return requiredBytes + requiredBytes/10
}

// diskSpaceCheckPath 返回用于查询磁盘剩余空间的已存在路径。
func diskSpaceCheckPath(targetDir string) (string, error) {
	current := filepath.Clean(targetDir)
	for {
		info, err := os.Stat(current)
		if err == nil && info.IsDir() {
			return current, nil
		}
		if err == nil {
			return filepath.Dir(current), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent directory for %s", targetDir)
		}
		current = parent
	}
}

func extractZipSafely(archivePath, targetDir string) ([]string, error) {
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return extractZipReaderSafely(&reader.Reader, targetDir)
}

func extractZipBytesSafely(archiveBytes []byte, targetDir string) ([]string, error) {
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		return nil, err
	}
	return extractZipReaderSafely(reader, targetDir)
}

func extractZipReaderSafely(reader *zip.Reader, targetDir string) ([]string, error) {
	requiredBytes, err := zipAdvertisedExtractBytes(reader)
	if err != nil {
		return nil, err
	}
	if err := ensureExtractDiskSpaceForTarget(targetDir, requiredBytes); err != nil {
		return nil, err
	}

	topLevel := make(map[string]string)
	extractedBytes := uint64(0)
	for _, file := range reader.File {
		normalizedName := strings.ReplaceAll(file.Name, "\\", "/")
		if hasPathTraversalComponent(normalizedName) {
			return nil, fmt.Errorf("zip entry contains parent traversal: %s", file.Name)
		}
		cleanName := path.Clean(normalizedName)
		if path.IsAbs(cleanName) || cleanName == "." {
			return nil, fmt.Errorf("invalid zip entry path: %s", file.Name)
		}
		sanitizedName := sanitizeArchiveEntryPath(cleanName)
		if sanitizedName == "" || sanitizedName == "." {
			return nil, fmt.Errorf("invalid sanitized zip entry path: %s", file.Name)
		}
		destPath := filepath.Join(targetDir, filepath.FromSlash(sanitizedName))
		rel, err := filepath.Rel(targetDir, destPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("zip slip detected: %s", file.Name)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink entry not allowed: %s", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return nil, err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return nil, err
			}
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode().Perm())
			if err != nil {
				rc.Close()
				return nil, err
			}
			written, copyErr := copyReaderWithLimit(out, rc, file.UncompressedSize64)
			closeErr := out.Close()
			rc.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if written != file.UncompressedSize64 {
				return nil, fmt.Errorf("zip entry size mismatch: %s", file.Name)
			}
			if extractedBytes > ^uint64(0)-written {
				return nil, fmt.Errorf("zip extraction size overflow: %s", file.Name)
			}
			extractedBytes += written
			if extractedBytes > requiredBytes {
				return nil, fmt.Errorf("zip extraction exceeded advertised byte limit: %s", file.Name)
			}
		}
		top := strings.Split(filepath.FromSlash(sanitizedName), string(filepath.Separator))[0]
		if top != "" {
			topLevel[top] = filepath.Join(targetDir, top)
		}
	}
	paths := make([]string, 0, len(topLevel))
	for _, path := range topLevel {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// sanitizeFileName 清理单个文件名组件，去除不安全字符。
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, "\x00", "")
	for _, c := range []string{"/", "\\", "<", ">", ":", "\"", "|", "?", "*"} {
		name = strings.ReplaceAll(name, c, "_")
	}
	name = strings.Trim(name, " .")
	if name == "." || name == ".." {
		name = ""
	}
	if len(name) > 255 {
		name = name[:255]
	}
	if name == "" {
		name = "_unnamed"
	}
	return name
}

// sanitizeArchiveEntryPath 仅清理路径中的各级文件名组件，不修改目录分隔符语义。
func sanitizeArchiveEntryPath(entryPath string) string {
	entryPath = strings.ReplaceAll(entryPath, "\\", "/")
	parts := strings.Split(entryPath, "/")
	for i, part := range parts {
		parts[i] = sanitizeFileName(part)
	}
	return strings.Join(parts, "/")
}

func normalizeReplayTargetPath(targetPath string) (string, error) {
	baseDir := replayTempRootDir()
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}
	cleaned := filepath.Clean(strings.TrimSpace(targetPath))
	if cleaned == "" || cleaned == "." {
		return "", errors.New("empty replay target path")
	}
	if filepath.IsAbs(cleaned) {
		return ensurePathWithinBase(baseDir, cleaned)
	}
	return safeJoinUnderBase(baseDir, cleaned)
}

func normalizeReplayTargetPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		next, err := normalizeReplayTargetPath(path)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, next)
	}
	return normalized, nil
}

func safeJoinUnderBase(baseDir string, elems ...string) (string, error) {
	return ensurePathWithinBase(baseDir, filepath.Join(append([]string{baseDir}, elems...)...))
}

func ensurePathWithinBase(baseDir, candidate string) (string, error) {
	baseAbs, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		return "", err
	}
	candidateAbs, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes replay temp dir: %s", candidate)
	}
	return candidateAbs, nil
}

func hasPathTraversalComponent(input string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(input, "\\", "/"), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func zipAdvertisedExtractBytes(reader *zip.Reader) (uint64, error) {
	if reader == nil {
		return 0, errors.New("nil zip reader")
	}
	var total uint64
	for _, file := range reader.File {
		if file == nil || file.FileInfo().IsDir() {
			continue
		}
		if total > ^uint64(0)-file.UncompressedSize64 {
			return 0, fmt.Errorf("zip advertised size overflow: %s", file.Name)
		}
		total += file.UncompressedSize64
	}
	return total, nil
}

func ensureExtractDiskSpaceForTarget(targetDir string, requiredBytes uint64) error {
	if requiredBytes == 0 {
		return nil
	}
	checkPath, err := diskSpaceCheckPath(targetDir)
	if err != nil {
		return fmt.Errorf("resolve disk space check path: %w", err)
	}
	availableBytes, err := availableDiskSpace(checkPath)
	if err != nil {
		return fmt.Errorf("check available disk space: %w", err)
	}
	if availableBytes < requiredBytes {
		return fmt.Errorf(
			"insufficient disk space for extraction: available=%d required=%d target=%s",
			availableBytes, requiredBytes, targetDir,
		)
	}
	return nil
}

func copyReaderWithLimit(dst io.Writer, src io.Reader, limit uint64) (uint64, error) {
	buf := make([]byte, 32*1024)
	var written uint64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if uint64(n) > limit-written {
				return written, errors.New("reader exceeded advertised size")
			}
			wn, writeErr := dst.Write(buf[:n])
			written += uint64(wn)
			if writeErr != nil {
				return written, writeErr
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

func (a *Application) handleFileError(fileErr *protocol.FileError) error {
	if fileErr == nil {
		return nil
	}
	return a.failIncomingTransfer(fileErr.TransferID, errors.New(fileErr.ErrorMessage))
}

func (a *Application) handleFileRelease(release *protocol.FileRelease) error {
	if release == nil || release.TransferID == "" || a.transfers == nil {
		return nil
	}
	a.transfers.releaseOutgoingArchive(release.TransferID)
	return nil
}

func (a *Application) failIncomingTransfer(transferID string, err error) error {
	if err == nil {
		return nil
	}
	slog.Warn("application: file transfer failed", "transfer_id", transferID, "error", err)
	_, mutateErr := a.history.MutateByTransferID(transferID, func(item *history.HistoryItem) error {
		item.State = history.StateFailed
		item.ErrorMessage = err.Error()
		return nil
	})
	if mutateErr != nil {
		return mutateErr
	}
	_, _ = a.transfers.mutateIncoming(transferID, func(incoming *incomingTransfer) error {
		incoming.RequestActive = false
		incoming.Completing = false
		return nil
	})
	return err
}
