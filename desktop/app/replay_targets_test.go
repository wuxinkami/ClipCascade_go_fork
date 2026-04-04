package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

func TestStorePendingReplayModeLastActionWins(t *testing.T) {
	manager := history.NewManager(10)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		TransferID:      "transfer-1",
		EntryID:         "entry-1",
		Kind:            protocol.FileKindMultiFile,
		DisplayName:     "a.txt and 1 more",
		TopLevelNames:   []string{"a.txt", "b.txt"},
	}
	item := &history.HistoryItem{
		ID:           "history-1",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		Payload:      mustManifestPayload(t, manifest),
		TransferID:   manifest.TransferID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		LastChunkIdx: 0,
	}
	manager.AddItem(item)
	if !manager.UpdateState(item.ID, history.StateDownloading) {
		t.Fatalf("UpdateState(%q, downloading) = false", item.ID)
	}

	app := &Application{history: manager}

	stored := manager.GetByID(item.ID)
	if _, err := app.storePendingReplayMode(stored, ReplayModeSystemClipboardPaste); err != nil {
		t.Fatalf("storePendingReplayMode(system) error = %v", err)
	}
	stored = manager.GetByID(item.ID)
	if _, err := app.storePendingReplayMode(stored, ReplayModePathPlaceholderPaste); err != nil {
		t.Fatalf("storePendingReplayMode(placeholder) error = %v", err)
	}

	updated := manager.GetByID(item.ID)
	if updated.PendingReplayMode != string(ReplayModePathPlaceholderPaste) {
		t.Fatalf("PendingReplayMode = %q, want %q", updated.PendingReplayMode, ReplayModePathPlaceholderPaste)
	}
}

func TestCompletePendingReplayModePlaceholderClearsPendingState(t *testing.T) {
	manager := history.NewManager(10)
	manifest := protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		TransferID:      "transfer-1",
		EntryID:         "entry-1",
		Kind:            protocol.FileKindMultiFile,
		DisplayName:     "a.txt and 1 more",
		TopLevelNames:   []string{"a.txt", "b.txt"},
	}
	item := &history.HistoryItem{
		ID:                "history-1",
		Type:              constants.TypeFileStub,
		State:             history.StateOffered,
		Payload:           mustManifestPayload(t, manifest),
		TransferID:        manifest.TransferID,
		PendingReplayMode: string(ReplayModePathPlaceholderPaste),
		LocalPaths:        []string{"/tmp/202603290015/a.txt", "/tmp/202603290015/b.txt"},
		ReservedPaths:     []string{"/tmp/202603290015"},
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	manager.AddItem(item)
	if !manager.UpdateState(item.ID, history.StateDownloading) {
		t.Fatalf("UpdateState(%q, downloading) = false", item.ID)
	}
	if _, err := manager.Mutate(item.ID, func(next *history.HistoryItem) error {
		next.State = history.StateReadyToPaste
		next.PendingReplayMode = string(ReplayModePathPlaceholderPaste)
		next.LocalPaths = append([]string(nil), item.LocalPaths...)
		next.ReservedPaths = append([]string(nil), item.ReservedPaths...)
		return nil
	}); err != nil {
		t.Fatalf("Mutate(%q, ready_to_paste) error = %v", item.ID, err)
	}

	origNotify := notifyFn
	notifyFn = func(title, message string) {}
	t.Cleanup(func() { notifyFn = origNotify })

	app := &Application{history: manager}
	if err := app.completePendingReplayMode(manifest.TransferID); err != nil {
		t.Fatalf("completePendingReplayMode() error = %v", err)
	}

	updated := manager.GetByID(item.ID)
	if updated.State != history.StateConsumed {
		t.Fatalf("State = %q, want %q", updated.State, history.StateConsumed)
	}
	if updated.PendingReplayMode != string(ReplayModeNone) {
		t.Fatalf("PendingReplayMode = %q, want empty", updated.PendingReplayMode)
	}
}

func TestReservedPathsForManifestMultiFileUsesSubsecondPrecision(t *testing.T) {
	manifestA := &protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		TransferID:      "transfer-a",
		EntryID:         "entry-a",
		Kind:            protocol.FileKindMultiFile,
		DisplayName:     "a.txt and 1 more",
		TopLevelNames:   []string{"a.txt", "b.txt"},
	}
	manifestB := &protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		TransferID:      "transfer-b",
		EntryID:         "entry-b",
		Kind:            protocol.FileKindMultiFile,
		DisplayName:     "c.txt and 1 more",
		TopLevelNames:   []string{"c.txt", "d.txt"},
	}
	base := time.Date(2026, 3, 15, 8, 0, 0, 123000000, time.UTC)
	other := time.Date(2026, 3, 15, 8, 0, 0, 456000000, time.UTC)

	pathsA, err := reservedPathsForManifest(manifestA, base)
	if err != nil {
		t.Fatalf("reservedPathsForManifest(a) error = %v", err)
	}
	pathsB, err := reservedPathsForManifest(manifestB, other)
	if err != nil {
		t.Fatalf("reservedPathsForManifest(b) error = %v", err)
	}
	if len(pathsA) != 1 || len(pathsB) != 1 {
		t.Fatalf("pathsA=%#v pathsB=%#v, want one path each", pathsA, pathsB)
	}
	if pathsA[0] == pathsB[0] {
		t.Fatalf("reserved paths collided: %q", pathsA[0])
	}
	if filepath.Base(pathsA[0]) != "20260315080000_123000000" {
		t.Fatalf("base(pathsA[0]) = %q, want %q", filepath.Base(pathsA[0]), "20260315080000_123000000")
	}
	if filepath.Base(pathsB[0]) != "20260315080000_456000000" {
		t.Fatalf("base(pathsB[0]) = %q, want %q", filepath.Base(pathsB[0]), "20260315080000_456000000")
	}
}

func TestReservedPathsForManifestSingleFolderUsesOriginalName(t *testing.T) {
	manifest := &protocol.FileStubManifest{
		ProtocolVersion: protocol.FileProtocolVersion,
		TransferID:      "transfer-folder",
		EntryID:         "entry-folder",
		Kind:            protocol.FileKindFolder,
		DisplayName:     "photos",
		TopLevelNames:   []string{"photos"},
	}

	paths, err := reservedPathsForManifest(manifest, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("reservedPathsForManifest() error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("paths = %#v, want exactly one path", paths)
	}
	if filepath.Base(paths[0]) != "photos" {
		t.Fatalf("base(paths[0]) = %q, want %q", filepath.Base(paths[0]), "photos")
	}
}
