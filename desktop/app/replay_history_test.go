package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clipcascade/desktop/clipboard"
	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
)

func TestReplayHistoryItemReadyTextMarksConsumedAndPastes(t *testing.T) {
	item := &history.HistoryItem{
		ID:      "text-1",
		Type:    constants.TypeText,
		State:   history.StateReady,
		Payload: "hello",
	}

	var (
		pasteCalls int
		pastedType string
		pastedData string
		markedID   string
	)

	result, err := replayHistoryItem(item, replayExecutor{
		paste: func(payload string, payloadType string, filename string) {
			pasteCalls++
			pastedType = payloadType
			pastedData = payload
		},
		markConsumed: func(id string) bool {
			markedID = id
			return true
		},
	}, replayOptions{})
	if err != nil {
		t.Fatalf("replayHistoryItem returned error: %v", err)
	}
	if pasteCalls != 1 {
		t.Fatalf("pasteCalls = %d, want 1", pasteCalls)
	}
	if pastedType != constants.TypeText {
		t.Fatalf("pastedType = %q, want %q", pastedType, constants.TypeText)
	}
	if pastedData != "hello" {
		t.Fatalf("pastedData = %q, want %q", pastedData, "hello")
	}
	if markedID != "text-1" {
		t.Fatalf("markedID = %q, want %q", markedID, "text-1")
	}
	if result.Action != replayActionClipboardStaged {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionClipboardStaged)
	}
}

func TestReplaySharedClipboardItemTextRejectsPathAndRealModes(t *testing.T) {
	manager := history.NewManager(10)
	manager.AddItem(&history.HistoryItem{
		ID:      "text-only",
		Type:    constants.TypeText,
		State:   history.StateReady,
		Payload: "hello",
	})
	app := &Application{
		history:   manager,
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}

	if _, err := app.ReplaySharedClipboardItem(ReplayModePathPlaceholderPaste); !errors.Is(err, ErrReplayModeNotApplicable) {
		t.Fatalf("ReplaySharedClipboardItem(path) error = %v, want %v", err, ErrReplayModeNotApplicable)
	}
	if _, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste); !errors.Is(err, ErrReplayModeNotApplicable) {
		t.Fatalf("ReplaySharedClipboardItem(real) error = %v, want %v", err, ErrReplayModeNotApplicable)
	}
}

func TestReplayHistoryItemReadyToPasteImageStagesPathsAndMarksConsumed(t *testing.T) {
	item := &history.HistoryItem{
		ID:         "image-1",
		Type:       constants.TypeImage,
		State:      history.StateReadyToPaste,
		LocalPaths: []string{"/tmp/image-1.png"},
	}

	var (
		staged   []string
		markedID string
	)

	result, err := replayHistoryItem(item, replayExecutor{
		stageFilePaths: func(paths []string) error {
			staged = append([]string(nil), paths...)
			return nil
		},
		markConsumed: func(id string) bool {
			markedID = id
			return true
		},
	}, replayOptions{})
	if err != nil {
		t.Fatalf("replayHistoryItem returned error: %v", err)
	}
	if len(staged) != 1 || staged[0] != "/tmp/image-1.png" {
		t.Fatalf("staged = %#v, want [/tmp/image-1.png]", staged)
	}
	if markedID != "image-1" {
		t.Fatalf("markedID = %q, want %q", markedID, "image-1")
	}
	if result.Action != replayActionClipboardStaged {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionClipboardStaged)
	}
}

func TestReplayHistoryItemConsumedImageWithoutLocalPathsRejectsLegacyFallback(t *testing.T) {
	item := &history.HistoryItem{
		ID:      "image-legacy",
		Type:    constants.TypeImage,
		State:   history.StateConsumed,
		Payload: "base64-image",
	}

	pasteCalls := 0
	stageCalls := 0
	_, err := replayHistoryItem(item, replayExecutor{
		paste: func(payload string, payloadType string, filename string) {
			pasteCalls++
		},
		stageFilePaths: func(paths []string) error {
			stageCalls++
			return nil
		},
	}, replayOptions{})
	if !errors.Is(err, ErrUnsupportedReplayState) {
		t.Fatalf("error = %v, want ErrUnsupportedReplayState", err)
	}
	if pasteCalls != 0 {
		t.Fatalf("pasteCalls = %d, want 0", pasteCalls)
	}
	if stageCalls != 0 {
		t.Fatalf("stageCalls = %d, want 0", stageCalls)
	}
}

func TestReplayHistoryItemReadyFileStagesPathsAndMarksConsumed(t *testing.T) {
	item := &history.HistoryItem{
		ID:         "file-1",
		Type:       constants.TypeFileStub,
		State:      history.StateReadyToPaste,
		LocalPaths: []string{"/tmp/a", "/tmp/b"},
	}

	var staged []string
	var markedID string
	result, err := replayHistoryItem(item, replayExecutor{
		stageFilePaths: func(paths []string) error {
			staged = append([]string(nil), paths...)
			return nil
		},
		markConsumed: func(id string) bool {
			markedID = id
			return true
		},
	}, replayOptions{})
	if err != nil {
		t.Fatalf("replayHistoryItem returned error: %v", err)
	}
	if len(staged) != 2 || staged[0] != "/tmp/a" || staged[1] != "/tmp/b" {
		t.Fatalf("staged = %#v", staged)
	}
	if markedID != "file-1" {
		t.Fatalf("markedID = %q, want file-1", markedID)
	}
	if result.Action != replayActionClipboardStaged {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionClipboardStaged)
	}
}

func TestReplayHistoryItemAutoPasteRunsForHotkeyReplay(t *testing.T) {
	item := &history.HistoryItem{
		ID:      "text-hotkey",
		Type:    constants.TypeText,
		State:   history.StateConsumed,
		Payload: "hello",
	}

	autoPasteCalls := 0
	result, err := replayHistoryItem(item, replayExecutor{
		paste: func(payload string, payloadType string, filename string) {},
		autoPaste: func() error {
			autoPasteCalls++
			return errors.New("best effort")
		},
	}, replayOptions{autoPaste: true})
	if err != nil {
		t.Fatalf("replayHistoryItem returned error: %v", err)
	}
	if autoPasteCalls != 1 {
		t.Fatalf("autoPasteCalls = %d, want 1", autoPasteCalls)
	}
	if !result.ManualPasteRequired {
		t.Fatal("result.ManualPasteRequired = false, want true")
	}
	if !result.AutoPasteAttempted {
		t.Fatal("result.AutoPasteAttempted = false, want true")
	}
}

func TestReplayHistoryItemRejectsUnsupportedState(t *testing.T) {
	item := &history.HistoryItem{
		ID:    "file-offered",
		Type:  constants.TypeText,
		State: history.StateOffered,
	}

	pasteCalls := 0
	_, err := replayHistoryItem(item, replayExecutor{
		paste: func(payload string, payloadType string, filename string) {
			pasteCalls++
		},
		markConsumed: func(id string) bool { return true },
	}, replayOptions{})
	if !errors.Is(err, ErrUnsupportedReplayState) {
		t.Fatalf("error = %v, want ErrUnsupportedReplayState", err)
	}
	if pasteCalls != 0 {
		t.Fatalf("pasteCalls = %d, want 0", pasteCalls)
	}
}

func TestReplayHistoryItemRejectsUnsupportedType(t *testing.T) {
	item := &history.HistoryItem{
		ID:    "file-ready",
		Type:  "unknown",
		State: history.StateReady,
	}

	_, err := replayHistoryItem(item, replayExecutor{}, replayOptions{})
	if !errors.Is(err, ErrUnsupportedReplayType) {
		t.Fatalf("error = %v, want ErrUnsupportedReplayType", err)
	}
}

func TestReplayHistoryItemRejectsNilItem(t *testing.T) {
	_, err := replayHistoryItem(nil, replayExecutor{}, replayOptions{})
	if !errors.Is(err, ErrNoActiveHistoryItem) {
		t.Fatalf("error = %v, want ErrNoActiveHistoryItem", err)
	}
}

func TestReplayHistoryItemReturnsStateUpdateErrorWhenReadyItemCannotBeMarkedConsumed(t *testing.T) {
	item := &history.HistoryItem{
		ID:      "text-2",
		Type:    constants.TypeText,
		State:   history.StateReady,
		Payload: "hello",
	}

	_, err := replayHistoryItem(item, replayExecutor{
		paste: func(payload string, payloadType string, filename string) {},
		markConsumed: func(id string) bool {
			return false
		},
	}, replayOptions{})
	if !errors.Is(err, ErrReplayStateUpdate) {
		t.Fatalf("error = %v, want ErrReplayStateUpdate", err)
	}
}

func TestCanReplayHistoryItem(t *testing.T) {
	tests := []struct {
		name string
		item *history.HistoryItem
		want bool
	}{
		{name: "nil", item: nil, want: false},
		{name: "ready text", item: &history.HistoryItem{Type: constants.TypeText, State: history.StateReady}, want: true},
		{name: "consumed image without local path or payload", item: &history.HistoryItem{Type: constants.TypeImage, State: history.StateConsumed}, want: false},
		{name: "consumed image without local path but with payload", item: &history.HistoryItem{Type: constants.TypeImage, State: history.StateConsumed, Payload: "base64-data"}, want: true},
		{name: "consumed image with local path", item: &history.HistoryItem{Type: constants.TypeImage, State: history.StateConsumed, LocalPaths: []string{"/tmp/one.png"}}, want: true},
		{name: "offered text", item: &history.HistoryItem{Type: constants.TypeText, State: history.StateOffered}, want: false},
		{name: "ready file without paths", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateReadyToPaste}, want: false},
		{name: "ready file with paths", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateReadyToPaste, LocalPaths: []string{"/tmp/one"}}, want: true},
		{name: "consumed file with paths", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateConsumed, LocalPaths: []string{"/tmp/one"}}, want: true},
		{name: "offered file", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateOffered}, want: true},
		{name: "failed file", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateFailed}, want: true},
		{name: "downloading file", item: &history.HistoryItem{Type: constants.TypeFileStub, State: history.StateDownloading}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := canReplayHistoryItem(tc.item)
			if got != tc.want {
				t.Fatalf("canReplayHistoryItem() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReplayActiveHistoryItemRequestsDownloadForOfferedAndFailedFiles(t *testing.T) {
	tests := []history.ItemState{history.StateOffered, history.StateFailed}
	for _, state := range tests {
		t.Run(string(state), func(t *testing.T) {
			manager := history.NewManager(10)
			manager.AddItem(&history.HistoryItem{
				ID:         "file-1",
				Type:       constants.TypeFileStub,
				State:      state,
				TransferID: "transfer-1",
				Payload:    "{}",
			})
			app := &Application{history: manager}

			called := false
			orig := appRequestFileTransfer
			appRequestFileTransfer = func(got *Application, item *history.HistoryItem) error {
				if got != app {
					t.Fatalf("got application %p, want %p", got, app)
				}
				if item == nil || item.State != state {
					t.Fatalf("item = %+v, want state %q", item, state)
				}
				called = true
				return nil
			}
			t.Cleanup(func() { appRequestFileTransfer = orig })

			result, err := app.ReplayActiveHistoryItem(ReplayModeClipboardImmediate)
			if err != nil {
				t.Fatalf("ReplayActiveHistoryItem() error = %v", err)
			}
			if !called {
				t.Fatal("requestFileTransfer was not called")
			}
			if result.Action != replayActionDownloadRequested {
				t.Fatalf("result.Action = %q, want %q", result.Action, replayActionDownloadRequested)
			}
		})
	}
}

func TestReplayActiveHistoryItemReturnsDownloadInProgressForDownloadingFile(t *testing.T) {
	manager := history.NewManager(10)
	manager.AddItem(&history.HistoryItem{
		ID:         "file-1",
		Type:       constants.TypeFileStub,
		State:      history.StateOffered,
		TransferID: "transfer-1",
	})
	if !manager.UpdateState("file-1", history.StateDownloading) {
		t.Fatal("UpdateState(file-1, downloading) = false")
	}
	app := &Application{history: manager}

	result, err := app.ReplayActiveHistoryItem(ReplayModeClipboardImmediate)
	if err != nil {
		t.Fatalf("ReplayActiveHistoryItem() error = %v, want nil", err)
	}
	if result.Action != replayActionDownloadInProgress {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionDownloadInProgress)
	}
}

func TestReplaySharedClipboardItemImagePathPlaceholderUsesReservedPath(t *testing.T) {
	manager := history.NewManager(10)
	// 创建真实的临时文件，让 ensureImageMaterialized 的 os.Stat 检查通过
	tmpFile := testReplayPath(t, "image-ready.png")
	if err := os.WriteFile(tmpFile, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:            "image-ready",
		Type:          constants.TypeImage,
		State:         history.StateReady,
		Payload:       "aGVsbG8=",
		LocalPaths:    []string{tmpFile},
		ReservedPaths: []string{tmpFile},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(item)
	app := &Application{history: manager, clip: &clipboard.Manager{}}
	app.setSharedClipboardHistoryItem(item.ID)

	var stagedText string
	origStageText := appStageClipboardText
	origAutoPaste := appSimulateAutoPaste
	appStageClipboardText = func(a *Application, text string) error {
		stagedText = text
		return nil
	}
	appSimulateAutoPaste = func() error { return nil }
	t.Cleanup(func() {
		appStageClipboardText = origStageText
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModePathPlaceholderPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if stagedText != tmpFile {
		t.Fatalf("stagedText = %q, want %q", stagedText, tmpFile)
	}
	if result.Mode != ReplayModePathPlaceholderPaste {
		t.Fatalf("result.Mode = %q, want %q", result.Mode, ReplayModePathPlaceholderPaste)
	}
	if stored := manager.GetByID(item.ID); stored == nil || stored.State != history.StateConsumed {
		t.Fatalf("stored state = %#v, want consumed", stored)
	}
}

func TestReplaySharedClipboardItemImagePathPlaceholderAutoPastes(t *testing.T) {
	manager := history.NewManager(10)
	tmpFile := testReplayPath(t, "image-ready.png")
	if err := os.WriteFile(tmpFile, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:            "image-ready-autopaste",
		Type:          constants.TypeImage,
		State:         history.StateReady,
		Payload:       "aGVsbG8=",
		LocalPaths:    []string{tmpFile},
		ReservedPaths: []string{tmpFile},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(item)
	app := &Application{history: manager, clip: &clipboard.Manager{}}
	app.setSharedClipboardHistoryItem(item.ID)

	origStageText := appStageClipboardText
	origAutoPaste := appSimulateAutoPaste
	stagedText := ""
	autoPasteCalls := 0
	appStageClipboardText = func(a *Application, text string) error {
		stagedText = text
		return nil
	}
	appSimulateAutoPaste = func() error {
		autoPasteCalls++
		return nil
	}
	t.Cleanup(func() {
		appStageClipboardText = origStageText
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModePathPlaceholderPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if stagedText != tmpFile {
		t.Fatalf("stagedText = %q, want %q", stagedText, tmpFile)
	}
	if autoPasteCalls != 1 {
		t.Fatalf("autoPasteCalls = %d, want 1", autoPasteCalls)
	}
	if !result.AutoPasteRequested || !result.AutoPasteAttempted {
		t.Fatalf("result = %#v, want auto paste requested and attempted", result)
	}
	if result.ManualPasteRequired {
		t.Fatalf("result.ManualPasteRequired = %v, want false", result.ManualPasteRequired)
	}
	if result.Message != "Pasted placeholder path" {
		t.Fatalf("result.Message = %q, want %q", result.Message, "Pasted placeholder path")
	}
}

func TestReplaySharedClipboardItemImagePathPlaceholderStartsBackgroundMaterialization(t *testing.T) {
	manager := history.NewManager(10)
	now := time.Date(2026, 4, 4, 10, 30, 0, 0, time.UTC)
	item := &history.HistoryItem{
		ID:        "image-path-async",
		Type:      constants.TypeImage,
		State:     history.StateReady,
		Payload:   "aGVsbG8=",
		FileName:  "1.jpg",
		CreatedAt: now,
		UpdatedAt: now,
	}
	manager.AddItem(item)
	app := &Application{history: manager, clip: &clipboard.Manager{}}
	app.setSharedClipboardHistoryItem(item.ID)

	expectedPath := filepath.Join(replayTempRootDir(), "1.jpg")
	stagedText := ""
	autoPasteCalls := 0
	backgroundCalls := 0

	origStageText := appStageClipboardText
	origAutoPaste := appSimulateAutoPaste
	origStartImageMaterialize := appStartImageMaterialize
	appStageClipboardText = func(a *Application, text string) error {
		stagedText = text
		return nil
	}
	appSimulateAutoPaste = func() error {
		autoPasteCalls++
		return nil
	}
	appStartImageMaterialize = func(a *Application, got *history.HistoryItem) {
		backgroundCalls++
		if got == nil {
			t.Fatal("background materialize item is nil")
		}
		if len(got.ReservedPaths) != 1 || got.ReservedPaths[0] != expectedPath {
			t.Fatalf("ReservedPaths = %#v, want [%q]", got.ReservedPaths, expectedPath)
		}
		if len(got.LocalPaths) != 0 {
			t.Fatalf("LocalPaths = %#v, want empty before background materialize", got.LocalPaths)
		}
	}
	t.Cleanup(func() {
		appStageClipboardText = origStageText
		appSimulateAutoPaste = origAutoPaste
		appStartImageMaterialize = origStartImageMaterialize
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModePathPlaceholderPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if stagedText != expectedPath {
		t.Fatalf("stagedText = %q, want %q", stagedText, expectedPath)
	}
	if autoPasteCalls != 1 {
		t.Fatalf("autoPasteCalls = %d, want 1", autoPasteCalls)
	}
	if backgroundCalls != 1 {
		t.Fatalf("backgroundCalls = %d, want 1", backgroundCalls)
	}
	stored := manager.GetByID(item.ID)
	if stored == nil {
		t.Fatal("stored item not found")
	}
	if stored.State != history.StateConsumed {
		t.Fatalf("stored.State = %q, want %q", stored.State, history.StateConsumed)
	}
	if len(stored.LocalPaths) != 0 {
		t.Fatalf("stored.LocalPaths = %#v, want empty before background materialize finishes", stored.LocalPaths)
	}
	if len(stored.ReservedPaths) != 1 || stored.ReservedPaths[0] != expectedPath {
		t.Fatalf("stored.ReservedPaths = %#v, want [%q]", stored.ReservedPaths, expectedPath)
	}
	if result.Message != "Pasted placeholder path" {
		t.Fatalf("result.Message = %q, want %q", result.Message, "Pasted placeholder path")
	}
}

func TestReplaySharedClipboardItemPathModeRejectsFileStub(t *testing.T) {
	manager := history.NewManager(10)
	item := &history.HistoryItem{
		ID:         "file-placeholder-blocked",
		Type:       constants.TypeFileStub,
		State:      history.StateOffered,
		TransferID: "transfer-placeholder-blocked",
		Payload:    "{}",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	manager.AddItem(item)
	app := &Application{
		history:   manager,
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}
	app.setSharedClipboardHistoryItem(item.ID)

	if _, err := app.ReplaySharedClipboardItem(ReplayModePathPlaceholderPaste); !errors.Is(err, ErrReplayModeNotApplicable) {
		t.Fatalf("ReplaySharedClipboardItem(path) error = %v, want %v", err, ErrReplayModeNotApplicable)
	}
}

func TestReplaySharedClipboardItemRealModeOfferedFileDoesNotSetPendingReplayMode(t *testing.T) {
	manager := history.NewManager(10)
	item := &history.HistoryItem{
		ID:         "file-offered-real",
		Type:       constants.TypeFileStub,
		State:      history.StateOffered,
		TransferID: "transfer-offered-real",
		Payload:    "{}",
	}
	manager.AddItem(item)
	app := &Application{
		history:   manager,
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}

	origRequest := appRequestFileTransfer
	appRequestFileTransfer = func(a *Application, got *history.HistoryItem) error {
		if got == nil || got.ID != item.ID {
			t.Fatalf("request item = %#v, want id %q", got, item.ID)
		}
		return nil
	}
	t.Cleanup(func() { appRequestFileTransfer = origRequest })

	result, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if result.Action != replayActionDownloadRequested {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionDownloadRequested)
	}
	updated := manager.GetByID(item.ID)
	if updated == nil {
		t.Fatal("updated item not found")
	}
	if updated.PendingReplayMode != string(ReplayModeNone) {
		t.Fatalf("PendingReplayMode = %q, want %q", updated.PendingReplayMode, ReplayModeNone)
	}
}

func TestReplaySharedClipboardItemRealModeSelfLoopbackFileUsesReceiveFlow(t *testing.T) {
	manager := history.NewManager(10)
	item := &history.HistoryItem{
		ID:              "file-self-real",
		Type:            constants.TypeFileStub,
		State:           history.StateOffered,
		TransferID:      "transfer-self-real",
		SourceSessionID: "session-local",
		Payload:         "{}",
	}
	manager.AddItem(item)
	app := &Application{
		sessionID: "session-local",
		history:   manager,
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}

	requestCalls := 0
	origRequest := appRequestFileTransfer
	appRequestFileTransfer = func(a *Application, got *history.HistoryItem) error {
		requestCalls++
		return nil
	}
	t.Cleanup(func() {
		appRequestFileTransfer = origRequest
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if requestCalls != 1 {
		t.Fatalf("requestCalls = %d, want 1", requestCalls)
	}
	if result.Action != replayActionDownloadRequested {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionDownloadRequested)
	}
	if result.Message != "Started transfer for real content clipboard copy" {
		t.Fatalf("result.Message = %q, want %q", result.Message, "Started transfer for real content clipboard copy")
	}
	updated := manager.GetByID(item.ID)
	if updated == nil {
		t.Fatal("updated item not found")
	}
	if updated.PendingReplayMode != string(ReplayModeNone) {
		t.Fatalf("PendingReplayMode = %q, want %q", updated.PendingReplayMode, ReplayModeNone)
	}
}

func TestReplaySharedClipboardItemImageRealClipboardUsesFilePaths(t *testing.T) {
	manager := history.NewManager(10)
	// 创建真实的临时文件
	tmpFile := testReplayPath(t, "image-real.png")
	if err := os.WriteFile(tmpFile, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:            "image-real",
		Type:          constants.TypeImage,
		State:         history.StateReady,
		Payload:       "aGVsbG8=",
		LocalPaths:    []string{tmpFile},
		ReservedPaths: []string{tmpFile},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(item)
	app := &Application{history: manager, clip: &clipboard.Manager{}}
	app.setSharedClipboardHistoryItem(item.ID)

	var stagedPaths []string
	origStageFiles := appStageClipboardFiles
	origAutoPaste := appSimulateAutoPaste
	autoPasteCalls := 0
	appStageClipboardFiles = func(a *Application, paths []string) error {
		stagedPaths = append([]string(nil), paths...)
		return nil
	}
	appSimulateAutoPaste = func() error {
		autoPasteCalls++
		return nil
	}
	t.Cleanup(func() {
		appStageClipboardFiles = origStageFiles
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if len(stagedPaths) != 1 || stagedPaths[0] != tmpFile {
		t.Fatalf("stagedPaths = %#v, want [%q]", stagedPaths, tmpFile)
	}
	if result.Mode != ReplayModeSystemClipboardPaste {
		t.Fatalf("result.Mode = %q, want %q", result.Mode, ReplayModeSystemClipboardPaste)
	}
	if autoPasteCalls != 1 {
		t.Fatalf("autoPasteCalls = %d, want 1", autoPasteCalls)
	}
	if !result.AutoPasteRequested || !result.AutoPasteAttempted {
		t.Fatalf("result = %#v, want auto paste requested and attempted", result)
	}
	if result.ManualPasteRequired {
		t.Fatalf("result.ManualPasteRequired = %v, want false", result.ManualPasteRequired)
	}
	if result.Message != "Pasted real image file" {
		t.Fatalf("result.Message = %q, want %q", result.Message, "Pasted real image file")
	}
	if stored := manager.GetByID(item.ID); stored == nil || stored.State != history.StateConsumed {
		t.Fatalf("stored state = %#v, want consumed", stored)
	}
}

func TestReplaySharedClipboardItemRealModeSelfLoopbackReadyFileUsesLocalTmpPaths(t *testing.T) {
	manager := history.NewManager(10)
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "report.pdf")
	if err := os.WriteFile(tmpFile, []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:              "file-self-ready",
		Type:            constants.TypeFileStub,
		State:           history.StateOffered,
		TransferID:      "transfer-self-ready",
		SourceSessionID: "session-local",
		LocalPaths:      []string{tmpFile},
		ReservedPaths:   []string{tmpFile},
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	manager.AddItem(item)
	if !manager.UpdateState(item.ID, history.StateDownloading) {
		t.Fatalf("UpdateState(%q, downloading) = false", item.ID)
	}
	if !manager.UpdateState(item.ID, history.StateReadyToPaste) {
		t.Fatalf("UpdateState(%q, ready_to_paste) = false", item.ID)
	}
	app := &Application{
		sessionID: "session-local",
		history:   manager,
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}
	app.setSharedClipboardHistoryItem(item.ID)

	var stagedPaths []string
	origStageFiles := appStageClipboardFiles
	origAutoPaste := appSimulateAutoPaste
	appStageClipboardFiles = func(a *Application, paths []string) error {
		stagedPaths = append([]string(nil), paths...)
		return nil
	}
	appSimulateAutoPaste = func() error { return nil }
	t.Cleanup(func() {
		appStageClipboardFiles = origStageFiles
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if len(stagedPaths) != 1 || stagedPaths[0] != tmpFile {
		t.Fatalf("stagedPaths = %#v, want [%s]", stagedPaths, tmpFile)
	}
	if result.Action != replayActionClipboardStaged {
		t.Fatalf("result.Action = %q, want %q", result.Action, replayActionClipboardStaged)
	}
	if stored := manager.GetByID(item.ID); stored == nil || stored.State != history.StateConsumed {
		t.Fatalf("stored state = %#v, want consumed", stored)
	}
}

func TestReplaySharedClipboardItemRealModeFileCopiesToClipboardWithoutAutoPaste(t *testing.T) {
	manager := history.NewManager(10)
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "report.pdf")
	if err := os.WriteFile(tmpFile, []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:            "file-manual-fallback",
		Type:          constants.TypeFileStub,
		State:         history.StateOffered,
		TransferID:    "transfer-manual-fallback",
		LocalPaths:    []string{tmpFile},
		ReservedPaths: []string{tmpFile},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(item)
	if !manager.UpdateState(item.ID, history.StateDownloading) {
		t.Fatalf("UpdateState(%q, downloading) = false", item.ID)
	}
	if !manager.UpdateState(item.ID, history.StateReadyToPaste) {
		t.Fatalf("UpdateState(%q, ready_to_paste) = false", item.ID)
	}
	app := &Application{
		history: manager,
		clip:    &clipboard.Manager{},
	}
	app.setSharedClipboardHistoryItem(item.ID)

	origStageFiles := appStageClipboardFiles
	autoPasteCalls := 0
	origAutoPaste := appSimulateAutoPaste
	appStageClipboardFiles = func(a *Application, paths []string) error {
		return nil
	}
	appSimulateAutoPaste = func() error {
		autoPasteCalls++
		return nil
	}
	t.Cleanup(func() {
		appStageClipboardFiles = origStageFiles
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
	if err != nil {
		t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
	}
	if autoPasteCalls != 0 {
		t.Fatalf("autoPasteCalls = %d, want 0", autoPasteCalls)
	}
	if result.Message != "Copied real content to clipboard" {
		t.Fatalf("result.Message = %q, want %q", result.Message, "Copied real content to clipboard")
	}
	if stored := manager.GetByID(item.ID); stored == nil || stored.State != history.StateConsumed {
		t.Fatalf("stored state = %#v, want consumed", stored)
	}
}

func TestStageRealClipboardContentWaitsForWaylandClipboardSettle(t *testing.T) {
	app := &Application{clip: &clipboard.Manager{}}
	item := &history.HistoryItem{
		ID:         "image-wayland",
		Type:       constants.TypeImage,
		State:      history.StateReady,
		LocalPaths: []string{"/tmp/image-wayland.png"},
	}

	var (
		steps []string
		slept time.Duration
	)
	origStageFiles := appStageClipboardFiles
	origIsWayland := appIsWaylandSession
	origSleep := appSleep
	origAutoPaste := appSimulateAutoPaste
	appStageClipboardFiles = func(a *Application, paths []string) error {
		steps = append(steps, "stage")
		return nil
	}
	appIsWaylandSession = func() bool { return true }
	appSleep = func(delay time.Duration) {
		slept = delay
		steps = append(steps, "sleep")
	}
	appSimulateAutoPaste = func() error {
		steps = append(steps, "autopaste")
		return nil
	}
	t.Cleanup(func() {
		appStageClipboardFiles = origStageFiles
		appIsWaylandSession = origIsWayland
		appSleep = origSleep
		appSimulateAutoPaste = origAutoPaste
	})

	result, err := app.stageRealClipboardContent(item)
	if err != nil {
		t.Fatalf("stageRealClipboardContent() error = %v", err)
	}
	if slept != waylandFileClipboardSettleWait {
		t.Fatalf("slept = %s, want %s", slept, waylandFileClipboardSettleWait)
	}
	if len(steps) != 3 || steps[0] != "stage" || steps[1] != "sleep" || steps[2] != "autopaste" {
		t.Fatalf("steps = %#v, want [stage sleep autopaste]", steps)
	}
	if !result.AutoPasteRequested || !result.AutoPasteAttempted || result.ManualPasteRequired {
		t.Fatalf("result = %#v, want successful auto paste", result)
	}
}

func TestReplaySharedClipboardItemConsumedImageRealClipboardStableAcrossConcurrentReplays(t *testing.T) {
	manager := history.NewManager(10)
	// 创建真实的临时文件
	tmpFile := testReplayPath(t, "image-consumed.png")
	if err := os.WriteFile(tmpFile, []byte("fake-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := &history.HistoryItem{
		ID:            "image-consumed",
		Type:          constants.TypeImage,
		State:         history.StateReady,
		Payload:       "aGVsbG8=",
		LocalPaths:    []string{tmpFile},
		ReservedPaths: []string{tmpFile},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(item)
	if !manager.UpdateState(item.ID, history.StateConsumed) {
		t.Fatalf("UpdateState(%q, consumed) = false", item.ID)
	}
	app := &Application{history: manager, clip: &clipboard.Manager{}}
	app.setSharedClipboardHistoryItem(item.ID)

	var (
		mu         sync.Mutex
		stageCalls int
	)
	origStageFiles := appStageClipboardFiles
	origIsWayland := appIsWaylandSession
	origAutoPaste := appSimulateAutoPaste
	appStageClipboardFiles = func(a *Application, paths []string) error {
		mu.Lock()
		stageCalls++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return nil
	}
	appIsWaylandSession = func() bool { return false }
	appSimulateAutoPaste = func() error { return nil }
	t.Cleanup(func() {
		appStageClipboardFiles = origStageFiles
		appIsWaylandSession = origIsWayland
		appSimulateAutoPaste = origAutoPaste
	})

	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := app.ReplaySharedClipboardItem(ReplayModeSystemClipboardPaste)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("ReplaySharedClipboardItem() error = %v", err)
		}
	}
	if stageCalls != 2 {
		t.Fatalf("stageCalls = %d, want 2", stageCalls)
	}
	if stored := manager.GetByID(item.ID); stored == nil || stored.State != history.StateConsumed {
		t.Fatalf("stored state = %#v, want consumed", stored)
	}
}

func TestEnsureImageMaterializedWritesFileToDisk(t *testing.T) {
	manager := history.NewManager(10)
	now := time.Date(2026, 3, 30, 15, 45, 0, 0, time.UTC)
	payload := "aGVsbG8="
	imageItem := &history.HistoryItem{
		ID:        "image-target",
		Type:      constants.TypeImage,
		State:     history.StateReady,
		Payload:   payload,
		FileName:  "shot.png",
		CreatedAt: now,
		UpdatedAt: now,
	}
	manager.AddItem(imageItem)
	textItem := &history.HistoryItem{
		ID:        "text-active",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "hello",
		CreatedAt: now.Add(time.Second),
		UpdatedAt: now.Add(time.Second),
	}
	manager.AddItem(textItem)
	manager.SetActive(textItem.ID)

	app := &Application{history: manager}
	updated, err := app.ensureImageMaterialized(manager.GetByID(imageItem.ID))
	if err != nil {
		t.Fatalf("ensureImageMaterialized() error = %v", err)
	}
	if updated == nil {
		t.Fatal("ensureImageMaterialized() returned nil")
	}
	if len(updated.LocalPaths) != 1 {
		t.Fatalf("LocalPaths = %#v, want single path", updated.LocalPaths)
	}
	if got := filepath.Base(updated.LocalPaths[0]); !strings.HasPrefix(got, "shot") || filepath.Ext(got) != ".png" {
		t.Fatalf("image path = %q, want shot*.png family", updated.LocalPaths[0])
	}
	// 确保文件真实存在
	if _, err := os.Stat(updated.LocalPaths[0]); err != nil {
		t.Fatalf("materialized file not found: %v", err)
	}
	_ = os.Remove(updated.LocalPaths[0])

	storedText := manager.GetByID(textItem.ID)
	if storedText == nil || storedText.State != history.StateReady {
		t.Fatalf("text item mutated unexpectedly: %#v", storedText)
	}
}

func TestEnsureImageMaterializedNoopsWhenAlreadyMaterialized(t *testing.T) {
	manager := history.NewManager(10)
	existingPath := filepath.Join(replayTempRootDir(), "already-done.png")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(existingPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(existingPath) })
	imageItem := &history.HistoryItem{
		ID:         "image-done",
		Type:       constants.TypeImage,
		State:      history.StateReady,
		Payload:    "aGVsbG8=",
		LocalPaths: []string{existingPath},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	manager.AddItem(imageItem)
	app := &Application{history: manager}

	updated, err := app.ensureImageMaterialized(manager.GetByID(imageItem.ID))
	if err != nil {
		t.Fatalf("ensureImageMaterialized() error = %v", err)
	}
	// 应该直接返回，不会重新写入
	if len(updated.LocalPaths) != 1 || updated.LocalPaths[0] != existingPath {
		t.Fatalf("LocalPaths = %#v, want [%q]", updated.LocalPaths, existingPath)
	}
}

func TestEnsureImageMaterializedOverwritesSameSizeDifferentContent(t *testing.T) {
	manager := history.NewManager(10)
	existingPath := filepath.Join(replayTempRootDir(), "same-size.png")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(existingPath, []byte("HELLO"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(existingPath) })

	imageItem := &history.HistoryItem{
		ID:            "image-overwrite",
		Type:          constants.TypeImage,
		State:         history.StateReady,
		Payload:       "aGVsbG8=",
		LocalPaths:    []string{existingPath},
		ReservedPaths: []string{existingPath},
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	manager.AddItem(imageItem)
	app := &Application{history: manager}

	updated, err := app.ensureImageMaterialized(manager.GetByID(imageItem.ID))
	if err != nil {
		t.Fatalf("ensureImageMaterialized() error = %v", err)
	}
	if updated == nil {
		t.Fatal("ensureImageMaterialized() returned nil")
	}
	data, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("materialized file content = %q, want %q", string(data), "hello")
	}
}

func TestEnsureImageMaterializedReturnsErrorForEmptyPayload(t *testing.T) {
	manager := history.NewManager(10)
	imageItem := &history.HistoryItem{
		ID:        "image-empty",
		Type:      constants.TypeImage,
		State:     history.StateReady,
		Payload:   "",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	manager.AddItem(imageItem)
	app := &Application{history: manager}

	_, err := app.ensureImageMaterialized(manager.GetByID(imageItem.ID))
	if err == nil {
		t.Fatal("ensureImageMaterialized() should return error for empty payload")
	}
}

func TestEnsureImageMaterializedSharesInFlightBackgroundJob(t *testing.T) {
	manager := history.NewManager(10)
	item := &history.HistoryItem{
		ID:        "image-shared-job",
		Type:      constants.TypeImage,
		State:     history.StateReady,
		Payload:   "aGVsbG8=",
		FileName:  "shared.png",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	manager.AddItem(item)
	app := &Application{history: manager}

	destPath := testReplayPath(t, "shared.png")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	callCount := 0
	var callMu sync.Mutex

	origMaterialize := appMaterializeImageNow
	appMaterializeImageNow = func(a *Application, got *history.HistoryItem) (*history.HistoryItem, error) {
		callMu.Lock()
		callCount++
		callMu.Unlock()
		once.Do(func() { close(started) })
		<-release
		return a.history.Mutate(got.ID, func(next *history.HistoryItem) error {
			next.LocalPaths = []string{destPath}
			next.ReservedPaths = []string{destPath}
			next.UpdatedAt = time.Now()
			return nil
		})
	}
	t.Cleanup(func() {
		appMaterializeImageNow = origMaterialize
	})

	app.ensureImageMaterializedAsync(manager.GetByID(item.ID))
	<-started

	type materializeResult struct {
		item *history.HistoryItem
		err  error
	}
	resultCh := make(chan materializeResult, 1)
	go func() {
		updated, err := app.ensureImageMaterialized(manager.GetByID(item.ID))
		resultCh <- materializeResult{item: updated, err: err}
	}()

	select {
	case result := <-resultCh:
		t.Fatalf("ensureImageMaterialized() returned too early: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}

	close(release)

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("ensureImageMaterialized() error = %v", result.err)
	}
	if result.item == nil {
		t.Fatal("ensureImageMaterialized() returned nil item")
	}
	if len(result.item.LocalPaths) != 1 || result.item.LocalPaths[0] != destPath {
		t.Fatalf("LocalPaths = %#v, want [%q]", result.item.LocalPaths, destPath)
	}
	callMu.Lock()
	defer callMu.Unlock()
	if callCount != 1 {
		t.Fatalf("callCount = %d, want 1", callCount)
	}
}
