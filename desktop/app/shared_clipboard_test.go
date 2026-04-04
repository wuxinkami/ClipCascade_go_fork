package app

import (
	"testing"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
)

func TestSetSharedClipboardHistoryItemAlsoUpdatesActiveHistoryItem(t *testing.T) {
	manager := history.NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manager.AddItem(&history.HistoryItem{
		ID:        "old",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "old",
		CreatedAt: base,
		UpdatedAt: base,
	})
	manager.AddItem(&history.HistoryItem{
		ID:        "new",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "new",
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(time.Minute),
	})
	if !manager.SetActive("old") {
		t.Fatal("SetActive(old) = false")
	}

	app := &Application{history: manager}
	app.setSharedClipboardHistoryItem("new")

	shared := app.sharedClipboardHistoryItem()
	if shared == nil || shared.ID != "new" {
		t.Fatalf("shared item = %#v, want id %q", shared, "new")
	}
	active := manager.GetActive()
	if active == nil || active.ID != "new" {
		t.Fatalf("active item = %#v, want id %q", active, "new")
	}
}

func TestResolveSharedReplayItemPrefersCurrentSharedItem(t *testing.T) {
	manager := history.NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	fileItem := &history.HistoryItem{
		ID:         "file-1",
		Type:       constants.TypeFileStub,
		State:      history.StateReadyToPaste,
		LocalPaths: []string{"/tmp/file-1"},
		CreatedAt:  base,
		UpdatedAt:  base,
	}
	imageItem := &history.HistoryItem{
		ID:         "image-1",
		Type:       constants.TypeImage,
		State:      history.StateReady,
		Payload:    "base64-image",
		LocalPaths: []string{"/tmp/image-1.png"},
		CreatedAt:  base.Add(time.Minute),
		UpdatedAt:  base.Add(time.Minute),
	}
	manager.AddItem(fileItem)
	manager.AddItem(imageItem)

	app := &Application{history: manager}
	app.setLastFileStubHistoryItem(fileItem.ID)
	app.setSharedClipboardHistoryItem(imageItem.ID)

	resolved := app.resolveSharedReplayItem()
	if resolved == nil || resolved.ID != imageItem.ID {
		t.Fatalf("resolved item = %#v, want id %q", resolved, imageItem.ID)
	}
}
