package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clipcascade/desktop/config"
	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	pkgcrypto "github.com/clipcascade/pkg/crypto"
	"github.com/clipcascade/pkg/protocol"
)

func TestCreateFileStubManifestUsesSessionAndDisplayMetadata(t *testing.T) {
	baseDir := t.TempDir()
	first := filepath.Join(baseDir, "report.txt")
	second := filepath.Join(baseDir, "photos")
	if err := os.WriteFile(first, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(second, "a.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manifest := createFileStubManifest("entry-1", "transfer-1", "session-1", "desktop-a", []string{first, second})
	if manifest.ProtocolVersion != protocol.FileProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", manifest.ProtocolVersion, protocol.FileProtocolVersion)
	}
	if manifest.TransferID != "transfer-1" {
		t.Fatalf("TransferID = %q, want %q", manifest.TransferID, "transfer-1")
	}
	if manifest.SourceSessionID != "session-1" {
		t.Fatalf("SourceSessionID = %q, want %q", manifest.SourceSessionID, "session-1")
	}
	if manifest.SourceDevice != "desktop-a" {
		t.Fatalf("SourceDevice = %q, want %q", manifest.SourceDevice, "desktop-a")
	}
	if manifest.EntryCount != 2 {
		t.Fatalf("EntryCount = %d, want 2", manifest.EntryCount)
	}
	if manifest.DisplayName != "report.txt and 1 more" {
		t.Fatalf("DisplayName = %q, want %q", manifest.DisplayName, "report.txt and 1 more")
	}
	if len(manifest.TopLevelNames) != 2 {
		t.Fatalf("len(TopLevelNames) = %d, want 2", len(manifest.TopLevelNames))
	}
}

func TestCreateFileStubManifestSingleFolderSetsFolderKind(t *testing.T) {
	baseDir := t.TempDir()
	folder := filepath.Join(baseDir, "photos")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "a.jpg"), []byte("img"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manifest := createFileStubManifest("entry-folder", "transfer-folder", "session-folder", "desktop-folder", []string{folder})
	if manifest.Kind != "folder" {
		t.Fatalf("Kind = %q, want %q", manifest.Kind, "folder")
	}
	if manifest.DisplayName != "photos" {
		t.Fatalf("DisplayName = %q, want %q", manifest.DisplayName, "photos")
	}
	if manifest.EstimatedTotalBytes != int64(len("img")) {
		t.Fatalf("EstimatedTotalBytes = %d, want %d", manifest.EstimatedTotalBytes, len("img"))
	}
	if manifest.ArchiveFormat != "zip" {
		t.Fatalf("ArchiveFormat = %q, want %q", manifest.ArchiveFormat, "zip")
	}
}

func TestCreateFileStubManifestSingleFileUsesRawArchiveFormat(t *testing.T) {
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manifest := createFileStubManifest("entry-single", "transfer-single", "session-single", "desktop-single", []string{filePath})
	if manifest.Kind != protocol.FileKindSingleFile {
		t.Fatalf("Kind = %q, want %q", manifest.Kind, protocol.FileKindSingleFile)
	}
	if manifest.ArchiveFormat != "raw" {
		t.Fatalf("ArchiveFormat = %q, want %q", manifest.ArchiveFormat, "raw")
	}
}

func TestRegisterOutgoingReusesTransferForUnchangedSourceContent(t *testing.T) {
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := newTransferManager("session-local")
	first, err := manager.RegisterOutgoing([]string{filePath}, "desktop-a")
	if err != nil {
		t.Fatalf("first RegisterOutgoing() error = %v", err)
	}
	second, err := manager.RegisterOutgoing([]string{filePath}, "desktop-a")
	if err != nil {
		t.Fatalf("second RegisterOutgoing() error = %v", err)
	}

	if first.Manifest.TransferID != second.Manifest.TransferID {
		t.Fatalf("TransferID changed for unchanged content: %q vs %q", first.Manifest.TransferID, second.Manifest.TransferID)
	}
}

func TestRegisterOutgoingCreatesNewTransferWhenContentChangesButSizeMatches(t *testing.T) {
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("abcde"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := newTransferManager("session-local")
	first, err := manager.RegisterOutgoing([]string{filePath}, "desktop-a")
	if err != nil {
		t.Fatalf("first RegisterOutgoing() error = %v", err)
	}

	if err := os.WriteFile(filePath, []byte("vwxyz"), 0o644); err != nil {
		t.Fatalf("overwrite WriteFile() error = %v", err)
	}

	second, err := manager.RegisterOutgoing([]string{filePath}, "desktop-a")
	if err != nil {
		t.Fatalf("second RegisterOutgoing() error = %v", err)
	}

	if first.Manifest.TransferID == second.Manifest.TransferID {
		t.Fatalf("TransferID reused after content change with same size: %q", first.Manifest.TransferID)
	}
}

func TestHandleFileTransferMessageIgnoresChunkForOtherSession(t *testing.T) {
	app := &Application{sessionID: "session-local", transfers: newTransferManager("session-local")}
	data, err := protocol.NewClipboardDataWithPayload(constants.TypeFileChunk, protocol.FileChunk{
		TransferID:      "transfer-1",
		TargetSessionID: "session-other",
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString([]byte("abc")),
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256([]byte("abc"))),
	})
	if err != nil {
		t.Fatalf("NewClipboardDataWithPayload() error = %v", err)
	}

	handled, err := app.handleFileTransferMessage(data)
	if err != nil {
		t.Fatalf("handleFileTransferMessage() error = %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}
}

func TestHandleFileChunkAndCompleteUpdatesHistoryToReadyToPaste(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-1",
		TransferID:      "transfer-1",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		DisplayName:     "hello.txt",
		EntryCount:      1,
	}
	item := &history.HistoryItem{
		ID:           "history-1",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		SourceDevice: manifest.SourceDevice,
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	archiveBytes := buildZipBytes(t, map[string]string{"hello.txt": "hello world"})
	chunkSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(archiveBytes),
		ChunkSHA256:     fmt.Sprintf("%x", chunkSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	archiveSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveSHA256:    fmt.Sprintf("%x", archiveSum[:]),
		ActualTotalBytes: int64(len(archiveBytes)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
	if stored.LastChunkIdx != 0 {
		t.Fatalf("LastChunkIdx = %d, want 0", stored.LastChunkIdx)
	}
	if len(stored.LocalPaths) != 1 {
		t.Fatalf("len(LocalPaths) = %d, want 1", len(stored.LocalPaths))
	}
	content, err := os.ReadFile(stored.LocalPaths[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "hello world" {
		t.Fatalf("content = %q, want %q", string(content), "hello world")
	}
	archivePath := filepath.Join(filepath.Dir(filepath.Dir(stored.LocalPaths[0])), fileTransferArchive)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archivePath should be removed after extract, stat err = %v", err)
	}
}

func TestPrepareOutgoingArchiveInMemoryKeepsArchiveOffDisk(t *testing.T) {
	app := &Application{}
	baseDir := t.TempDir()
	source := filepath.Join(baseDir, "hello.txt")
	if err := os.WriteFile(source, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	transfer := &outgoingTransfer{
		Manifest: protocol.FileStubManifest{
			TransferID:          "transfer-memory-outgoing",
			EstimatedTotalBytes: int64(len("hello world")),
		},
		SourcePaths: []string{source},
	}
	if err := app.prepareOutgoingArchiveInMemory(transfer); err != nil {
		t.Fatalf("prepareOutgoingArchiveInMemory() error = %v", err)
	}

	if transfer.ArchiveMode != transferArchiveModeMemory {
		t.Fatalf("ArchiveMode = %q, want %q", transfer.ArchiveMode, transferArchiveModeMemory)
	}
	if len(transfer.ArchiveBytes) == 0 {
		t.Fatal("ArchiveBytes should not be empty")
	}
	if transfer.ArchivePath != "" {
		t.Fatalf("ArchivePath = %q, want empty", transfer.ArchivePath)
	}
	if transfer.BaseDir != "" {
		t.Fatalf("BaseDir = %q, want empty", transfer.BaseDir)
	}
}

func TestPrepareOutgoingArchiveInMemoryRawSingleFileKeepsOriginalBytes(t *testing.T) {
	app := &Application{}
	baseDir := t.TempDir()
	source := filepath.Join(baseDir, "hello.txt")
	payload := []byte("hello raw world")
	if err := os.WriteFile(source, payload, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	transfer := &outgoingTransfer{
		Manifest: protocol.FileStubManifest{
			TransferID:          "transfer-memory-raw",
			Kind:                protocol.FileKindSingleFile,
			ArchiveFormat:       "raw",
			EstimatedTotalBytes: int64(len(payload)),
		},
		SourcePaths: []string{source},
	}
	if err := app.prepareOutgoingArchiveInMemory(transfer); err != nil {
		t.Fatalf("prepareOutgoingArchiveInMemory() error = %v", err)
	}

	if transfer.ArchiveMode != transferArchiveModeMemory {
		t.Fatalf("ArchiveMode = %q, want %q", transfer.ArchiveMode, transferArchiveModeMemory)
	}
	if string(transfer.ArchiveBytes) != string(payload) {
		t.Fatalf("ArchiveBytes = %q, want %q", string(transfer.ArchiveBytes), string(payload))
	}
}

func TestHandleFileChunkAndCompleteMemoryModeAvoidsPayloadZipOnDisk(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-memory",
		TransferID:      "transfer-memory",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		DisplayName:     "hello.txt",
		EntryCount:      1,
	}
	item := &history.HistoryItem{
		ID:           "history-memory",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		SourceDevice: manifest.SourceDevice,
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	archiveBytes := buildZipBytes(t, map[string]string{"hello.txt": "hello world"})
	chunkSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(archiveBytes),
		ChunkSHA256:     fmt.Sprintf("%x", chunkSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	archiveSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", archiveSum[:]),
		ActualTotalBytes: int64(len(archiveBytes)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
	archivePath := filepath.Join(filepath.Dir(filepath.Dir(stored.LocalPaths[0])), fileTransferArchive)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archivePath should not exist for memory mode, stat err = %v", err)
	}
}

func TestHandleFileChunkAndCompleteRawSingleFileWritesReservedPath(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-raw",
		TransferID:      "transfer-raw",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "hello.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"hello.txt"},
	}
	reservedDir := testReplayPath(t)
	reservedPath := filepath.Join(reservedDir, "hello.txt")
	item := &history.HistoryItem{
		ID:            "history-raw",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		DisplayName:   manifest.DisplayName,
		Payload:       mustManifestPayload(t, manifest),
		TransferID:    manifest.TransferID,
		SourceDevice:  manifest.SourceDevice,
		LastChunkIdx:  -1,
		ReservedPaths: []string{reservedPath},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	raw := []byte("hello raw world")
	rawSum := sha256.Sum256(raw)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", rawSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", rawSum[:]),
		ActualTotalBytes: int64(len(raw)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
	if len(stored.LocalPaths) != 1 || stored.LocalPaths[0] != reservedPath {
		t.Fatalf("LocalPaths = %#v, want [%q]", stored.LocalPaths, reservedPath)
	}
	content, err := os.ReadFile(reservedPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != string(raw) {
		t.Fatalf("content = %q, want %q", string(content), string(raw))
	}
}

func TestHandleFileChunkAndCompleteRawSingleFileReusesExistingIdenticalTarget(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-raw-reuse",
		TransferID:      "transfer-raw-reuse",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "hello.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"hello.txt"},
	}
	reservedDir := testReplayPath(t)
	reservedPath := filepath.Join(reservedDir, "hello.txt")
	raw := []byte("hello raw world")
	if err := os.WriteFile(reservedPath, raw, 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	oldTime := now.Add(-time.Hour)
	if err := os.Chtimes(reservedPath, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	item := &history.HistoryItem{
		ID:            "history-raw-reuse",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		DisplayName:   manifest.DisplayName,
		Payload:       mustManifestPayload(t, manifest),
		TransferID:    manifest.TransferID,
		SourceDevice:  manifest.SourceDevice,
		LastChunkIdx:  -1,
		ReservedPaths: []string{reservedPath},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	rawSum := sha256.Sum256(raw)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", rawSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", rawSum[:]),
		ActualTotalBytes: int64(len(raw)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	info, err := os.Stat(reservedPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.ModTime().Equal(oldTime) {
		t.Fatalf("ModTime = %v, want unchanged %v", info.ModTime(), oldTime)
	}
}

func TestHandleFileChunkAndCompleteRawSingleFileOverwritesChangedTarget(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-raw-overwrite",
		TransferID:      "transfer-raw-overwrite",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "hello.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"hello.txt"},
	}
	reservedDir := testReplayPath(t)
	reservedPath := filepath.Join(reservedDir, "hello.txt")
	if err := os.WriteFile(reservedPath, []byte("old content"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing) error = %v", err)
	}
	item := &history.HistoryItem{
		ID:            "history-raw-overwrite",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		DisplayName:   manifest.DisplayName,
		Payload:       mustManifestPayload(t, manifest),
		TransferID:    manifest.TransferID,
		SourceDevice:  manifest.SourceDevice,
		LastChunkIdx:  -1,
		ReservedPaths: []string{reservedPath},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	raw := []byte("new content")
	rawSum := sha256.Sum256(raw)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", rawSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", rawSum[:]),
		ActualTotalBytes: int64(len(raw)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	content, err := os.ReadFile(reservedPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != string(raw) {
		t.Fatalf("content = %q, want %q", string(content), string(raw))
	}
}

func TestHandleFileChunkIgnoresDuplicateChunk(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-dup-chunk",
		TransferID:      "transfer-dup-chunk",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "dup.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"dup.txt"},
	}
	item := &history.HistoryItem{
		ID:           "history-dup-chunk",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		SourceDevice: manifest.SourceDevice,
		LastChunkIdx: -1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	raw := []byte("dup")
	chunk := &protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256(raw)),
	}
	if err := app.handleFileChunk(chunk); err != nil {
		t.Fatalf("first handleFileChunk() error = %v", err)
	}
	if err := app.handleFileChunk(chunk); err != nil {
		t.Fatalf("second handleFileChunk() error = %v", err)
	}
	incoming := app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found")
	}
	if incoming.LastChunkIdx != 0 {
		t.Fatalf("LastChunkIdx = %d, want 0", incoming.LastChunkIdx)
	}
	if string(incoming.ArchiveBytes) != string(raw) {
		t.Fatalf("ArchiveBytes = %q, want %q", string(incoming.ArchiveBytes), string(raw))
	}
}

func TestHandleFileCompleteIgnoresDuplicateAfterSuccess(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 123456789, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-dup-complete",
		TransferID:      "transfer-dup-complete",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "dup-complete.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"dup-complete.txt"},
	}
	reservedPath := testReplayPath(t, "dup-complete.txt")
	item := &history.HistoryItem{
		ID:            "history-dup-complete",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		DisplayName:   manifest.DisplayName,
		Payload:       mustManifestPayload(t, manifest),
		TransferID:    manifest.TransferID,
		SourceDevice:  manifest.SourceDevice,
		LastChunkIdx:  -1,
		ReservedPaths: []string{reservedPath},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	raw := []byte("dup complete")
	rawSum := sha256.Sum256(raw)
	chunk := &protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", rawSum),
	}
	if err := app.handleFileChunk(chunk); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	complete := &protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", rawSum),
		ActualTotalBytes: int64(len(raw)),
	}
	if err := app.handleFileComplete(complete); err != nil {
		t.Fatalf("first handleFileComplete() error = %v", err)
	}
	if err := app.handleFileComplete(complete); err != nil {
		t.Fatalf("second handleFileComplete() error = %v", err)
	}
	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
}

func TestHandleFileCompleteIgnoresDuplicateWhileCompletionInProgress(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		transfers: newTransferManager("session-local"),
	}
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-dup-inflight-complete",
		TransferID:      "transfer-dup-inflight-complete",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindMultiFile,
		ArchiveFormat:   "zip",
		DisplayName:     "a.txt and 1 more",
		EntryCount:      2,
		TopLevelNames:   []string{"a.txt", "b.txt"},
	}
	app.transfers.RegisterIncoming(manifest, "history-dup-inflight-complete", -1)
	_, err := app.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
		incoming.ArchiveMode = transferArchiveModeDisk
		incoming.StorageMode = transferArchiveModeDisk
		incoming.ArchivePath = filepath.Join(t.TempDir(), "missing-payload.zip")
		incoming.Completing = true
		return nil
	})
	if err != nil {
		t.Fatalf("mutateIncoming() error = %v", err)
	}

	complete := &protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeDisk),
		ArchiveSHA256:    "irrelevant",
		ActualTotalBytes: 123,
	}
	if err := app.handleFileComplete(complete); err != nil {
		t.Fatalf("handleFileComplete() error = %v, want nil for duplicate in-flight complete", err)
	}
}

func TestHandleFileChunkAndCompleteMultiFileExtractsIntoReservedDirectory(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-bundle",
		TransferID:      "transfer-bundle",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindMultiFile,
		ArchiveFormat:   "zip",
		DisplayName:     "a.txt and 1 more",
		EntryCount:      2,
		TopLevelNames:   []string{"a.txt", "b.txt"},
	}
	reservedDir := testReplayPath(t, "20260315230000")
	item := &history.HistoryItem{
		ID:            "history-bundle",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		DisplayName:   manifest.DisplayName,
		Payload:       mustManifestPayload(t, manifest),
		TransferID:    manifest.TransferID,
		SourceDevice:  manifest.SourceDevice,
		LastChunkIdx:  -1,
		ReservedPaths: []string{reservedDir},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	archiveBytes := buildZipBytes(t, map[string]string{
		"a.txt": "hello",
		"b.txt": "world",
	})
	archiveSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(archiveBytes),
		ChunkSHA256:     fmt.Sprintf("%x", archiveSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", archiveSum[:]),
		ActualTotalBytes: int64(len(archiveBytes)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
	if len(stored.LocalPaths) != 2 {
		t.Fatalf("len(LocalPaths) = %d, want 2", len(stored.LocalPaths))
	}
	if len(stored.ReservedPaths) != 1 || stored.ReservedPaths[0] != reservedDir {
		t.Fatalf("ReservedPaths = %#v, want [%q]", stored.ReservedPaths, reservedDir)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		path := filepath.Join(reservedDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected extracted file at %q: %v", path, err)
		}
	}
}

func TestHandleFileChunkAndCompleteSingleFolderPreservesOriginalFolderName(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-folder",
		TransferID:      "transfer-folder",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindFolder,
		ArchiveFormat:   "zip",
		DisplayName:     "photos",
		EntryCount:      1,
		TopLevelNames:   []string{"photos"},
	}
	reservedDir := testReplayPath(t, "photos")
	item := &history.HistoryItem{
		ID:          "history-folder",
		Type:        constants.TypeFileStub,
		State:       history.StateOffered,
		DisplayName: manifest.DisplayName,
		Payload:     mustManifestPayload(t, manifest),
		TransferID:  manifest.TransferID,
		ReservedPaths: []string{
			reservedDir,
		},
		SourceDevice: manifest.SourceDevice,
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	archiveBytes := buildZipBytes(t, map[string]string{
		"photos/a.jpg": "img",
	})
	archiveSum := sha256.Sum256(archiveBytes)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(archiveBytes),
		ChunkSHA256:     fmt.Sprintf("%x", archiveSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", archiveSum[:]),
		ActualTotalBytes: int64(len(archiveBytes)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if len(stored.LocalPaths) != 1 {
		t.Fatalf("len(LocalPaths) = %d, want 1", len(stored.LocalPaths))
	}
	if got := filepath.Base(stored.LocalPaths[0]); !strings.HasPrefix(got, "photos") {
		t.Fatalf("folder name = %q, want photos prefix", got)
	}
	if _, err := os.Stat(filepath.Join(stored.LocalPaths[0], "a.jpg")); err != nil {
		t.Fatalf("expected extracted file inside preserved folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stored.LocalPaths[0], "photos", "a.jpg")); !os.IsNotExist(err) {
		t.Fatalf("folder should not be nested under itself, stat err = %v", err)
	}
}

func TestHandleFileCompleteClearsPendingReplayModeAndWaitsForExplicitReplay(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-pending-real",
		TransferID:      "transfer-pending-real",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "hello.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"hello.txt"},
	}
	reservedPath := testReplayPath(t, "hello.txt")
	item := &history.HistoryItem{
		ID:                "history-pending-real",
		Type:              constants.TypeFileStub,
		State:             history.StateOffered,
		DisplayName:       manifest.DisplayName,
		Payload:           mustManifestPayload(t, manifest),
		TransferID:        manifest.TransferID,
		SourceDevice:      manifest.SourceDevice,
		LastChunkIdx:      -1,
		PendingReplayMode: string(ReplayModeSystemClipboardPaste),
		ReservedPaths:     []string{reservedPath},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	app.history.AddItem(item)
	if !app.history.UpdateState(item.ID, history.StateDownloading) {
		t.Fatalf("UpdateState(%q, downloading) = false", item.ID)
	}
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	raw := []byte("hello raw world")
	rawSum := sha256.Sum256(raw)
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", rawSum[:]),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}
	if err := app.handleFileComplete(&protocol.FileComplete{
		TransferID:       manifest.TransferID,
		TargetSessionID:  app.appSessionID(),
		ArchiveMode:      string(transferArchiveModeMemory),
		ArchiveSHA256:    fmt.Sprintf("%x", rawSum[:]),
		ActualTotalBytes: int64(len(raw)),
	}); err != nil {
		t.Fatalf("handleFileComplete() error = %v", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateReadyToPaste {
		t.Fatalf("state = %q, want %q", stored.State, history.StateReadyToPaste)
	}
	// 传输完成后禁止自动真实粘贴，pending 标记应被清零，等待用户再次热键触发。
	if stored.PendingReplayMode != string(ReplayModeNone) {
		t.Fatalf("PendingReplayMode = %q, want %q", stored.PendingReplayMode, ReplayModeNone)
	}
	if len(stored.LocalPaths) != 1 || stored.LocalPaths[0] != reservedPath {
		t.Fatalf("LocalPaths = %#v, want [%q]", stored.LocalPaths, reservedPath)
	}
}

func TestNewTransferTempDirUsesSystemTempRoot(t *testing.T) {
	dir, err := newTransferTempDir("transfer-12345678")
	if err != nil {
		t.Fatalf("newTransferTempDir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	if filepath.Dir(dir) != os.TempDir() {
		t.Fatalf("parent dir = %q, want %q", filepath.Dir(dir), os.TempDir())
	}
	if !strings.HasPrefix(filepath.Base(dir), fileTransferTempPrefix) {
		t.Fatalf("base dir = %q, want prefix %q", filepath.Base(dir), fileTransferTempPrefix)
	}
}

func TestExtractZipSafelyRejectsZipSlip(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "payload.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := entry.Write([]byte("bad")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = extractZipSafely(archivePath, filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("extractZipSafely() error = nil, want zip-slip failure")
	}
	if !strings.Contains(err.Error(), "parent traversal") && !strings.Contains(err.Error(), "zip slip") {
		t.Fatalf("error = %v, want zip-slip related failure", err)
	}
}

func TestSanitizeFileName(t *testing.T) {
	longName := strings.Repeat("a", 300)
	testCases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "normal file name unchanged",
			in:   "report.txt",
			want: "report.txt",
		},
		{
			name: "replace windows invalid characters",
			in:   "file<name>.txt",
			want: "file_name_.txt",
		},
		{
			name: "remove null bytes",
			in:   "bad\x00name.txt",
			want: "badname.txt",
		},
		{
			name: "truncate overlong name",
			in:   longName,
			want: strings.Repeat("a", 255),
		},
		{
			name: "blank after trim becomes unnamed",
			in:   " .. ",
			want: "_unnamed",
		},
		{
			name: "path separators replaced",
			in:   "dir/file.txt",
			want: "dir_file.txt",
		},
		{
			name: "windows separators replaced",
			in:   `dir\file.txt`,
			want: "dir_file.txt",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeFileName(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeFileName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeReplayTargetPathRejectsOutsideTempRoot(t *testing.T) {
	outside := filepath.Join(filepath.Dir(replayTempRootDir()), "evil.txt")
	if _, err := normalizeReplayTargetPath(outside); err == nil {
		t.Fatalf("normalizeReplayTargetPath(%q) error = nil, want rejection", outside)
	}
}

func TestExtractZipSafelySanitizesPathComponents(t *testing.T) {
	archiveBytes := buildZipBytes(t, map[string]string{
		"bad<dir>/bad\x00file?.txt": "hello",
	})
	targetDir := filepath.Join(t.TempDir(), "out")

	paths, err := extractZipBytesSafely(archiveBytes, targetDir)
	if err != nil {
		t.Fatalf("extractZipBytesSafely() error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("len(paths) = %d, want 1", len(paths))
	}

	wantPath := filepath.Join(targetDir, "bad_dir_", "badfile_.txt")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("sanitized extracted file missing at %q: %v", wantPath, err)
	}
	if paths[0] != filepath.Join(targetDir, "bad_dir_") {
		t.Fatalf("top-level path = %q, want %q", paths[0], filepath.Join(targetDir, "bad_dir_"))
	}
}

func TestHandleFileChunkChecksumMismatchFailsTransfer(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-err",
		TransferID:      "transfer-err",
		SourceSessionID: "session-remote",
		DisplayName:     "bad.bin",
		EntryCount:      1,
	}
	item := &history.HistoryItem{
		ID:           "history-err",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)

	err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString([]byte("payload")),
		ChunkSHA256:     strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("handleFileChunk() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "chunk sha256 mismatch") {
		t.Fatalf("error = %v, want chunk sha256 mismatch", err)
	}

	stored := app.history.GetByTransferID(manifest.TransferID)
	if stored == nil {
		t.Fatal("history item not found")
	}
	if stored.State != history.StateFailed {
		t.Fatalf("state = %q, want %q", stored.State, history.StateFailed)
	}
	if !strings.Contains(stored.ErrorMessage, "chunk sha256 mismatch") {
		t.Fatalf("ErrorMessage = %q, want checksum failure", stored.ErrorMessage)
	}
}

func TestHandleFileReleaseClearsOutgoingArchiveState(t *testing.T) {
	app := &Application{transfers: newTransferManager("session-local")}
	baseDir := t.TempDir()
	transfer, err := app.transfers.RegisterOutgoing([]string{baseDir}, "desktop-local")
	if err != nil {
		t.Fatalf("RegisterOutgoing() error = %v", err)
	}

	archiveDir := filepath.Join(t.TempDir(), "outgoing")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	archivePath := filepath.Join(archiveDir, fileTransferArchive)
	if err := os.WriteFile(archivePath, []byte("archive"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	app.transfers.mu.Lock()
	stored := app.transfers.getOutgoingMutable(transfer.Manifest.TransferID)
	stored.ArchiveMode = transferArchiveModeMemory
	stored.ArchiveBytes = []byte("archive-bytes")
	stored.BaseDir = archiveDir
	stored.ArchivePath = archivePath
	stored.ArchiveSHA256 = "sum"
	stored.ArchiveSize = int64(len(stored.ArchiveBytes))
	stored.ChunkCount = 1
	app.transfers.mu.Unlock()

	if err := app.handleFileRelease(&protocol.FileRelease{TransferID: transfer.Manifest.TransferID, TargetSessionID: "session-target", ReleaseReason: "received_ok"}); err != nil {
		t.Fatalf("handleFileRelease() error = %v", err)
	}

	released := app.transfers.GetOutgoing(transfer.Manifest.TransferID)
	if released == nil {
		t.Fatal("outgoing transfer missing after release")
	}
	if len(released.ArchiveBytes) != 0 {
		t.Fatalf("ArchiveBytes len = %d, want 0", len(released.ArchiveBytes))
	}
	if released.ArchivePath != "" {
		t.Fatalf("ArchivePath = %q, want empty", released.ArchivePath)
	}
	if released.BaseDir != "" {
		t.Fatalf("BaseDir = %q, want empty", released.BaseDir)
	}
	if _, err := os.Stat(archiveDir); !os.IsNotExist(err) {
		t.Fatalf("archiveDir should be removed, stat err = %v", err)
	}
}

func TestHandleFileReleaseKeepsArchiveWhileAnotherTargetIsStillSending(t *testing.T) {
	app := &Application{transfers: newTransferManager("session-local")}
	baseDir := t.TempDir()
	transfer, err := app.transfers.RegisterOutgoing([]string{baseDir}, "desktop-local")
	if err != nil {
		t.Fatalf("RegisterOutgoing() error = %v", err)
	}

	archiveDir := filepath.Join(t.TempDir(), "outgoing-busy")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	archivePath := filepath.Join(archiveDir, fileTransferArchive)
	if err := os.WriteFile(archivePath, []byte("archive"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	app.transfers.mu.Lock()
	stored := app.transfers.getOutgoingMutable(transfer.Manifest.TransferID)
	stored.ArchiveMode = transferArchiveModeMemory
	stored.ArchiveBytes = []byte("archive-bytes")
	stored.BaseDir = archiveDir
	stored.ArchivePath = archivePath
	stored.sendingTargets = map[string]struct{}{
		"session-a": {},
	}
	app.transfers.mu.Unlock()

	if err := app.handleFileRelease(&protocol.FileRelease{TransferID: transfer.Manifest.TransferID, TargetSessionID: "session-b", ReleaseReason: "received_ok"}); err != nil {
		t.Fatalf("handleFileRelease() error = %v", err)
	}

	released := app.transfers.GetOutgoing(transfer.Manifest.TransferID)
	if released == nil {
		t.Fatal("outgoing transfer missing after release")
	}
	if len(released.ArchiveBytes) == 0 {
		t.Fatal("ArchiveBytes should be preserved while another target is still sending")
	}
	if _, err := os.Stat(archiveDir); err != nil {
		t.Fatalf("archiveDir should still exist: %v", err)
	}
}

func TestRequestFileTransferIsIdempotentWhileRequestActive(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-request",
		TransferID:      "transfer-request",
		SourceSessionID: "session-remote",
		DisplayName:     "report.pdf",
		EntryCount:      1,
	}
	item := &history.HistoryItem{
		ID:           "history-request",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		LastChunkIdx: -1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	app.history.AddItem(item)

	sendCalls := 0
	origDispatch := appDispatchClipboardBodySingle
	appDispatchClipboardBodySingle = func(
		body string,
		p2pSend func(string) int,
		stompConnected func() bool,
		stompSend func(string) error,
	) (clipboardDispatchResult, error) {
		sendCalls++
		return clipboardDispatchResult{P2PSent: true}, nil
	}
	t.Cleanup(func() { appDispatchClipboardBodySingle = origDispatch })

	if err := app.requestFileTransfer(item); err != nil {
		t.Fatalf("first requestFileTransfer() error = %v", err)
	}
	if err := app.requestFileTransfer(item); err != nil {
		t.Fatalf("second requestFileTransfer() error = %v", err)
	}
	if sendCalls != 1 {
		t.Fatalf("sendCalls = %d, want 1", sendCalls)
	}

	incoming := app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found")
	}
	if !incoming.RequestActive {
		t.Fatal("RequestActive should be true while transfer is in flight")
	}
}

func TestHandleFileChunkAcceptsRestartFromChunkZeroAfterResumeRequest(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	manifest := protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-restart",
		TransferID:      "transfer-restart",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		Kind:            protocol.FileKindSingleFile,
		ArchiveFormat:   "raw",
		DisplayName:     "restart.txt",
		EntryCount:      1,
		TopLevelNames:   []string{"restart.txt"},
	}
	item := &history.HistoryItem{
		ID:           "history-restart",
		Type:         constants.TypeFileStub,
		State:        history.StateFailed,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		SourceDevice: manifest.SourceDevice,
		LastChunkIdx: 1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)
	_, err := app.transfers.mutateIncoming(manifest.TransferID, func(incoming *incomingTransfer) error {
		incoming.ArchiveMode = transferArchiveModeMemory
		incoming.StorageMode = transferArchiveModeMemory
		incoming.ArchiveBytes = []byte("stale-partial")
		incoming.LastChunkIdx = 1
		return nil
	})
	if err != nil {
		t.Fatalf("mutateIncoming() error = %v", err)
	}

	origDispatch := appDispatchClipboardBodySingle
	appDispatchClipboardBodySingle = func(
		body string,
		p2pSend func(string) int,
		stompConnected func() bool,
		stompSend func(string) error,
	) (clipboardDispatchResult, error) {
		return clipboardDispatchResult{P2PSent: true}, nil
	}
	t.Cleanup(func() { appDispatchClipboardBodySingle = origDispatch })

	if err := app.requestFileTransfer(item); err != nil {
		t.Fatalf("requestFileTransfer() error = %v", err)
	}

	incoming := app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found")
	}
	if incoming.RequestedResumeFrom != 2 {
		t.Fatalf("RequestedResumeFrom = %d, want 2", incoming.RequestedResumeFrom)
	}

	raw := []byte("fresh")
	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     3,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256(raw)),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}

	incoming = app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found after restart chunk")
	}
	if incoming.LastChunkIdx != 0 {
		t.Fatalf("LastChunkIdx = %d, want 0", incoming.LastChunkIdx)
	}
	if string(incoming.ArchiveBytes) != string(raw) {
		t.Fatalf("ArchiveBytes = %q, want %q", string(incoming.ArchiveBytes), string(raw))
	}
	if incoming.RequestedResumeFrom != 0 {
		t.Fatalf("RequestedResumeFrom = %d, want 0 after accepting restart chunk", incoming.RequestedResumeFrom)
	}
}

func TestChunkEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		EntryID:         "entry-chunk-e2ee",
		TransferID:      "transfer-chunk-e2ee",
		SourceSessionID: "session-remote",
		DisplayName:     "hello.txt",
		EntryCount:      1,
	}
	app := newChunkTestApp(t, manifest, key, true)
	raw := []byte("hello encrypted chunk")
	encrypted, err := pkgcrypto.EncryptWithAAD(key, raw, fileChunkAAD(manifest.TransferID, 0))
	if err != nil {
		t.Fatalf("EncryptWithAAD() error = %v", err)
	}
	encoded, err := pkgcrypto.EncodeToJSONString(encrypted)
	if err != nil {
		t.Fatalf("EncodeToJSONString() error = %v", err)
	}

	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       encoded,
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256(raw)),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}

	incoming := app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found")
	}
	if string(incoming.ArchiveBytes) != string(raw) {
		t.Fatalf("ArchiveBytes = %q, want %q", string(incoming.ArchiveBytes), string(raw))
	}
}

func TestChunkDecryptWrongAADFails(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		EntryID:         "entry-chunk-bad-aad",
		TransferID:      "transfer-chunk-bad-aad",
		SourceSessionID: "session-remote",
		DisplayName:     "bad.txt",
		EntryCount:      1,
	}
	app := newChunkTestApp(t, manifest, key, true)
	raw := []byte("aad must match")
	encrypted, err := pkgcrypto.EncryptWithAAD(key, raw, fileChunkAAD(manifest.TransferID, 1))
	if err != nil {
		t.Fatalf("EncryptWithAAD() error = %v", err)
	}
	encoded, err := pkgcrypto.EncodeToJSONString(encrypted)
	if err != nil {
		t.Fatalf("EncodeToJSONString() error = %v", err)
	}

	err = app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       encoded,
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256(raw)),
	})
	if err == nil {
		t.Fatal("handleFileChunk() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "decrypt chunk 0") {
		t.Fatalf("error = %v, want decrypt chunk failure", err)
	}
}

func TestChunkDecryptWithoutE2EE(t *testing.T) {
	manifest := protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		EntryID:         "entry-chunk-plain",
		TransferID:      "transfer-chunk-plain",
		SourceSessionID: "session-remote",
		DisplayName:     "plain.txt",
		EntryCount:      1,
	}
	app := newChunkTestApp(t, manifest, nil, false)
	raw := []byte("plain chunk")

	if err := app.handleFileChunk(&protocol.FileChunk{
		TransferID:      manifest.TransferID,
		TargetSessionID: app.appSessionID(),
		ArchiveMode:     string(transferArchiveModeMemory),
		ChunkIndex:      0,
		TotalChunks:     1,
		ChunkData:       base64.StdEncoding.EncodeToString(raw),
		ChunkSHA256:     fmt.Sprintf("%x", sha256.Sum256(raw)),
	}); err != nil {
		t.Fatalf("handleFileChunk() error = %v", err)
	}

	incoming := app.transfers.GetIncoming(manifest.TransferID)
	if incoming == nil {
		t.Fatal("incoming transfer not found")
	}
	if string(incoming.ArchiveBytes) != string(raw) {
		t.Fatalf("ArchiveBytes = %q, want %q", string(incoming.ArchiveBytes), string(raw))
	}
}

func newChunkTestApp(t *testing.T, manifest protocol.FileStubManifest, key []byte, e2eeEnabled bool) *Application {
	t.Helper()
	app := &Application{
		cfg:       &config.Config{E2EEEnabled: e2eeEnabled},
		encKey:    append([]byte(nil), key...),
		sessionID: "session-local",
		history:   history.NewManager(10),
		transfers: newTransferManager("session-local"),
	}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	item := &history.HistoryItem{
		ID:           "history-" + manifest.TransferID,
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		DisplayName:  manifest.DisplayName,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	app.history.AddItem(item)
	app.transfers.RegisterIncoming(manifest, item.ID, item.LastChunkIdx)
	return app
}

func buildZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return buf.Bytes()
}

func mustManifestPayload(t *testing.T, manifest protocol.FileStubManifest) string {
	t.Helper()
	payload, err := protocol.EncodePayload(manifest)
	if err != nil {
		t.Fatalf("EncodePayload() error = %v", err)
	}
	return payload
}
