package history

import (
	"testing"
	"time"

	"github.com/clipcascade/pkg/constants"
)

func TestAddAndRetrieve(t *testing.T) {
	mgr := NewManager(10)
	createdAt := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	item := &HistoryItem{
		ID:           "item-1",
		Type:         constants.TypeText,
		State:        StateReady,
		DisplayName:  "Greeting",
		Payload:      "hello",
		PayloadType:  "text/plain",
		SourceDevice: "desktop-a",
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}

	mgr.AddItem(item)

	got := mgr.GetByID("item-1")
	if got == nil {
		t.Fatal("GetByID returned nil")
	}
	if got.Payload != "hello" {
		t.Fatalf("payload = %q, want %q", got.Payload, "hello")
	}
	if got.DisplayName != "Greeting" {
		t.Fatalf("display name = %q, want %q", got.DisplayName, "Greeting")
	}

	got.Payload = "changed"
	again := mgr.GetByID("item-1")
	if again == nil {
		t.Fatal("GetByID after mutation returned nil")
	}
	if again.Payload != "hello" {
		t.Fatalf("payload should be isolated copy, got %q", again.Payload)
	}
}

func TestActiveDefault(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:        "old",
		Type:      constants.TypeText,
		State:     StateReady,
		CreatedAt: base,
		UpdatedAt: base,
	})
	mgr.AddItem(&HistoryItem{
		ID:        "new",
		Type:      constants.TypeImage,
		State:     StateReady,
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(time.Minute),
	})

	active := mgr.GetActive()
	if active == nil {
		t.Fatal("GetActive returned nil")
	}
	if active.ID != "new" {
		t.Fatalf("active id = %q, want %q", active.ID, "new")
	}
}

func TestActiveExplicit(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:        "first",
		Type:      constants.TypeText,
		State:     StateReady,
		CreatedAt: base,
		UpdatedAt: base,
	})
	mgr.AddItem(&HistoryItem{
		ID:        "second",
		Type:      constants.TypeText,
		State:     StateReady,
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(time.Minute),
	})

	if !mgr.SetActive("first") {
		t.Fatal("SetActive should succeed")
	}

	active := mgr.GetActive()
	if active == nil {
		t.Fatal("GetActive returned nil")
	}
	if active.ID != "first" {
		t.Fatalf("active id = %q, want %q", active.ID, "first")
	}

	if mgr.SetActive("missing") {
		t.Fatal("SetActive should fail for missing id")
	}
}

func TestAddItemAllowsV1InitialStates(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	items := []*HistoryItem{
		{
			ID:        "text-ready",
			Type:      constants.TypeText,
			State:     StateReady,
			CreatedAt: base,
			UpdatedAt: base,
		},
		{
			ID:        "image-ready",
			Type:      constants.TypeImage,
			State:     StateReady,
			CreatedAt: base.Add(time.Minute),
			UpdatedAt: base.Add(time.Minute),
		},
		{
			ID:         "file-offered",
			Type:       constants.TypeFileStub,
			State:      StateOffered,
			TransferID: "transfer-allow",
			CreatedAt:  base.Add(2 * time.Minute),
			UpdatedAt:  base.Add(2 * time.Minute),
		},
	}

	for _, item := range items {
		mgr.AddItem(item)
	}

	for _, item := range items {
		got := mgr.GetByID(item.ID)
		if got == nil {
			t.Fatalf("item %q was not added", item.ID)
		}
		if got.State != item.State {
			t.Fatalf("item %q state = %q, want %q", item.ID, got.State, item.State)
		}
	}
}

func TestAddItemRejectsInvalidInitialStates(t *testing.T) {
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		item *HistoryItem
	}{
		{
			name: "text offered",
			item: &HistoryItem{
				ID:        "text-offered",
				Type:      constants.TypeText,
				State:     StateOffered,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
		{
			name: "image consumed",
			item: &HistoryItem{
				ID:        "image-consumed",
				Type:      constants.TypeImage,
				State:     StateConsumed,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
		{
			name: "file stub ready",
			item: &HistoryItem{
				ID:        "file-ready",
				Type:      constants.TypeFileStub,
				State:     StateReady,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
		{
			name: "file stub downloading",
			item: &HistoryItem{
				ID:        "file-downloading",
				Type:      constants.TypeFileStub,
				State:     StateDownloading,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
		{
			name: "legacy eager file",
			item: &HistoryItem{
				ID:        "file-eager",
				Type:      constants.TypeFileEager,
				State:     StateReady,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
		{
			name: "unknown type",
			item: &HistoryItem{
				ID:        "unknown-ready",
				Type:      "custom",
				State:     StateReady,
				CreatedAt: base,
				UpdatedAt: base,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewManager(10)

			mgr.AddItem(tc.item)

			if got := mgr.GetByID(tc.item.ID); got != nil {
				t.Fatalf("GetByID(%q) = %#v, want nil", tc.item.ID, got)
			}
			if items := mgr.List(); len(items) != 0 {
				t.Fatalf("len(List()) = %d, want 0", len(items))
			}
		})
	}
}

func TestStateTransition(t *testing.T) {
	mgr := NewManager(10)
	createdAt := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:        "file-1",
		Type:      constants.TypeFileStub,
		State:     StateOffered,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})

	if !mgr.UpdateState("file-1", StateDownloading) {
		t.Fatal("offered -> downloading should succeed")
	}
	firstUpdate := mgr.GetByID("file-1")
	if firstUpdate == nil || !firstUpdate.UpdatedAt.After(createdAt) {
		t.Fatal("UpdatedAt should be refreshed after first transition")
	}

	if !mgr.UpdateState("file-1", StateReadyToPaste) {
		t.Fatal("downloading -> ready_to_paste should succeed")
	}
	if !mgr.UpdateState("file-1", StateConsumed) {
		t.Fatal("ready_to_paste -> consumed should succeed")
	}

	final := mgr.GetByID("file-1")
	if final == nil {
		t.Fatal("GetByID returned nil")
	}
	if final.State != StateConsumed {
		t.Fatalf("final state = %q, want %q", final.State, StateConsumed)
	}
}

func TestUpdateStateRejectsInvalidTransition(t *testing.T) {
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		item        *HistoryItem
		targetState ItemState
	}{
		{
			name: "ready cannot download",
			item: &HistoryItem{
				ID:        "text-ready",
				Type:      constants.TypeText,
				State:     StateReady,
				CreatedAt: base,
				UpdatedAt: base,
			},
			targetState: StateDownloading,
		},
		{
			name: "offered cannot consume directly",
			item: &HistoryItem{
				ID:        "file-offered",
				Type:      constants.TypeFileStub,
				State:     StateOffered,
				CreatedAt: base,
				UpdatedAt: base,
			},
			targetState: StateConsumed,
		},
		{
			name: "failed cannot jump to ready to paste",
			item: &HistoryItem{
				ID:        "file-failed",
				Type:      constants.TypeFileStub,
				State:     StateOffered,
				CreatedAt: base,
				UpdatedAt: base,
			},
			targetState: StateReadyToPaste,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewManager(10)
			mgr.AddItem(tc.item)

			if tc.item.ID == "file-failed" {
				if !mgr.UpdateState(tc.item.ID, StateFailed) {
					t.Fatal("offered -> failed should succeed")
				}
			}

			before := mgr.GetByID(tc.item.ID)
			if before == nil {
				t.Fatal("GetByID returned nil before invalid transition")
			}

			if mgr.UpdateState(tc.item.ID, tc.targetState) {
				t.Fatalf("UpdateState(%q, %q) should fail", tc.item.ID, tc.targetState)
			}

			after := mgr.GetByID(tc.item.ID)
			if after == nil {
				t.Fatal("GetByID returned nil after invalid transition")
			}
			if after.State != before.State {
				t.Fatalf("state = %q, want %q", after.State, before.State)
			}
			if !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("UpdatedAt changed on invalid transition: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
			}
		})
	}
}

func TestMaxItemsEviction(t *testing.T) {
	mgr := NewManager(2)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:        "downloading",
		Type:      constants.TypeFileStub,
		State:     StateOffered,
		CreatedAt: base,
		UpdatedAt: base,
	})
	mgr.AddItem(&HistoryItem{
		ID:        "consumed",
		Type:      constants.TypeText,
		State:     StateReady,
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(time.Minute),
	})

	if !mgr.UpdateState("downloading", StateDownloading) {
		t.Fatal("offered -> downloading should succeed")
	}
	if !mgr.UpdateState("consumed", StateConsumed) {
		t.Fatal("ready -> consumed should succeed")
	}

	mgr.AddItem(&HistoryItem{
		ID:        "latest",
		Type:      constants.TypeText,
		State:     StateReady,
		CreatedAt: base.Add(2 * time.Minute),
		UpdatedAt: base.Add(2 * time.Minute),
	})

	items := mgr.List()
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if mgr.GetByID("consumed") != nil {
		t.Fatal("consumed item should be evicted first")
	}
	if mgr.GetByID("downloading") == nil {
		t.Fatal("downloading item should not be evicted")
	}
	if mgr.GetByID("latest") == nil {
		t.Fatal("latest item should remain")
	}
}

func TestGetByTransferID(t *testing.T) {
	mgr := NewManager(10)
	createdAt := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:         "file-2",
		Type:       constants.TypeFileStub,
		State:      StateOffered,
		TransferID: "transfer-123",
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	})

	got := mgr.GetByTransferID("transfer-123")
	if got == nil {
		t.Fatal("GetByTransferID returned nil")
	}
	if got.ID != "file-2" {
		t.Fatalf("id = %q, want %q", got.ID, "file-2")
	}
}

func TestMutateUpdatesFieldsAndPreservesStoredSliceIsolation(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:           "file-mutate",
		Type:         constants.TypeFileStub,
		State:        StateOffered,
		TransferID:   "transfer-mutate",
		LocalPaths:   []string{"before"},
		LastChunkIdx: -1,
		CreatedAt:    base,
		UpdatedAt:    base,
	})

	updated, err := mgr.Mutate("file-mutate", func(item *HistoryItem) error {
		item.State = StateDownloading
		item.LocalPaths = []string{"after-a", "after-b"}
		item.LastChunkIdx = 2
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate() error = %v", err)
	}
	if updated.State != StateDownloading {
		t.Fatalf("updated.State = %q, want %q", updated.State, StateDownloading)
	}
	if updated.LastChunkIdx != 2 {
		t.Fatalf("updated.LastChunkIdx = %d, want 2", updated.LastChunkIdx)
	}
	updated.LocalPaths[0] = "changed"

	stored := mgr.GetByID("file-mutate")
	if stored == nil {
		t.Fatal("expected stored item")
	}
	if stored.LocalPaths[0] != "after-a" {
		t.Fatalf("stored.LocalPaths[0] = %q, want %q", stored.LocalPaths[0], "after-a")
	}
}

func TestMutateRejectsInvalidStateTransition(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:        "file-invalid",
		Type:      constants.TypeFileStub,
		State:     StateOffered,
		CreatedAt: base,
		UpdatedAt: base,
	})

	before := mgr.GetByID("file-invalid")
	if before == nil {
		t.Fatal("expected item before mutate")
	}

	updated, err := mgr.Mutate("file-invalid", func(item *HistoryItem) error {
		item.State = StateConsumed
		return nil
	})
	if err == nil {
		t.Fatal("Mutate() error = nil, want invalid transition")
	}
	if updated != nil {
		t.Fatalf("updated = %#v, want nil", updated)
	}

	after := mgr.GetByID("file-invalid")
	if after == nil {
		t.Fatal("expected item after mutate")
	}
	if after.State != before.State {
		t.Fatalf("after.State = %q, want %q", after.State, before.State)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("UpdatedAt changed on rejected mutate: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestMutateByTransferIDUsesTransferLookup(t *testing.T) {
	mgr := NewManager(10)
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:           "file-transfer",
		Type:         constants.TypeFileStub,
		State:        StateOffered,
		TransferID:   "transfer-lookup",
		LastChunkIdx: -1,
		CreatedAt:    base,
		UpdatedAt:    base,
	})

	updated, err := mgr.MutateByTransferID("transfer-lookup", func(item *HistoryItem) error {
		item.State = StateDownloading
		item.LastChunkIdx = 4
		return nil
	})
	if err != nil {
		t.Fatalf("MutateByTransferID() error = %v", err)
	}
	if updated.ID != "file-transfer" {
		t.Fatalf("updated.ID = %q, want %q", updated.ID, "file-transfer")
	}
	if updated.State != StateDownloading {
		t.Fatalf("updated.State = %q, want %q", updated.State, StateDownloading)
	}
	if updated.LastChunkIdx != 4 {
		t.Fatalf("updated.LastChunkIdx = %d, want 4", updated.LastChunkIdx)
	}
}

func TestMutateByTransferIDUpdatesFieldsWithTransitionValidation(t *testing.T) {
	mgr := NewManager(10)
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	mgr.AddItem(&HistoryItem{
		ID:           "file-3",
		Type:         constants.TypeFileStub,
		State:        StateOffered,
		TransferID:   "transfer-789",
		LastChunkIdx: -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})

	updated, err := mgr.MutateByTransferID("transfer-789", func(item *HistoryItem) error {
		item.State = StateDownloading
		item.LastChunkIdx = 2
		return nil
	})
	if err != nil {
		t.Fatalf("MutateByTransferID() error = %v", err)
	}
	if updated.State != StateDownloading {
		t.Fatalf("updated.State = %q", updated.State)
	}
	if updated.LastChunkIdx != 2 {
		t.Fatalf("updated.LastChunkIdx = %d", updated.LastChunkIdx)
	}
}

func TestMutateByTransferIDUpdatesFileTransferFields(t *testing.T) {
	mgr := NewManager(10)
	createdAt := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	mgr.AddItem(&HistoryItem{
		ID:           "file-3",
		Type:         constants.TypeFileStub,
		State:        StateOffered,
		TransferID:   "transfer-456",
		LastChunkIdx: -1,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	})
	if !mgr.UpdateState("file-3", StateDownloading) {
		t.Fatal("UpdateState(file-3, downloading) should succeed")
	}

	updated, err := mgr.MutateByTransferID("transfer-456", func(item *HistoryItem) error {
		item.State = StateReadyToPaste
		item.LocalPaths = []string{"/tmp/one", "/tmp/two"}
		item.LastChunkIdx = 4
		return nil
	})
	if err != nil {
		t.Fatalf("MutateByTransferID() error = %v", err)
	}
	if updated.State != StateReadyToPaste {
		t.Fatalf("state = %q, want %q", updated.State, StateReadyToPaste)
	}
	if updated.LastChunkIdx != 4 {
		t.Fatalf("LastChunkIdx = %d, want 4", updated.LastChunkIdx)
	}

	stored := mgr.GetByTransferID("transfer-456")
	if stored == nil {
		t.Fatal("GetByTransferID returned nil")
	}
	if len(stored.LocalPaths) != 2 {
		t.Fatalf("len(LocalPaths) = %d, want 2", len(stored.LocalPaths))
	}

	updated.LocalPaths[0] = "changed"
	again := mgr.GetByTransferID("transfer-456")
	if again.LocalPaths[0] != "/tmp/one" {
		t.Fatalf("LocalPaths should be cloned, got %q", again.LocalPaths[0])
	}
}
