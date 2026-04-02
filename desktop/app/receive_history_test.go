package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/clipcascade/desktop/clipboard"
	"github.com/clipcascade/desktop/config"
	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

func TestOnReceiveDeduplicatesIdenticalPayloadWithin500ms(t *testing.T) {
	app := &Application{cfg: &config.Config{}, history: history.NewManager(10)}
	bodyBytes, err := (&protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: "loopback-text",
	}).Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	app.onReceive(string(bodyBytes))
	app.onReceive(string(bodyBytes))

	items := app.history.List()
	if len(items) != 1 {
		t.Fatalf("history item count = %d, want 1", len(items))
	}
	if items[0].Payload != "loopback-text" {
		t.Fatalf("payload = %q, want %q", items[0].Payload, "loopback-text")
	}
}

func TestOnReceiveAllowsIdenticalPayloadAfter500ms(t *testing.T) {
	app := &Application{cfg: &config.Config{}, history: history.NewManager(10)}
	bodyBytes, err := (&protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: "loopback-text",
	}).Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	app.onReceive(string(bodyBytes))

	app.lastRecvMu.Lock()
	app.lastRecvTime = app.lastRecvTime.Add(-501 * time.Millisecond)
	app.lastRecvMu.Unlock()

	app.onReceive(string(bodyBytes))

	items := app.history.List()
	if len(items) != 2 {
		t.Fatalf("history item count = %d, want 2", len(items))
	}
}

func TestClassifyReceivedClipboardData_TextImageAndFileStub(t *testing.T) {
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifestPayload, err := protocol.EncodePayload(protocol.FileStubManifest{
		ProtocolVersion:     protocol.FileProtocolVersion,
		EntryID:             "entry-1",
		TransferID:          "transfer-1",
		SourceSessionID:     "session-1",
		SourceDevice:        "desktop-a",
		Kind:                "multi_file",
		ArchiveFormat:       "zip",
		DisplayName:         "a.txt and 1 more",
		EntryCount:          2,
		TopLevelNames:       []string{"a.txt", "b.txt"},
		EstimatedTotalBytes: 12,
	})
	if err != nil {
		t.Fatalf("EncodePayload() error = %v", err)
	}
	tests := []struct {
		name            string
		clipData        *protocol.ClipboardData
		wantType        string
		wantState       history.ItemState
		wantPayloadType string
	}{
		{
			name: "text",
			clipData: &protocol.ClipboardData{
				Type:     constants.TypeText,
				Payload:  "hello",
				FileName: "",
			},
			wantType:        constants.TypeText,
			wantState:       history.StateReady,
			wantPayloadType: constants.TypeText,
		},
		{
			name: "image",
			clipData: &protocol.ClipboardData{
				Type:     constants.TypeImage,
				Payload:  "base64-image",
				FileName: "",
			},
			wantType:        constants.TypeImage,
			wantState:       history.StateReady,
			wantPayloadType: constants.TypeImage,
		},
		{
			name: "file stub",
			clipData: &protocol.ClipboardData{
				Type:     constants.TypeFileStub,
				Payload:  manifestPayload,
				FileName: "{\"count\":2}",
			},
			wantType:        constants.TypeFileStub,
			wantState:       history.StateOffered,
			wantPayloadType: "",
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := base.Add(time.Duration(i) * time.Minute)
			decision := classifyReceivedClipboardData(tc.clipData, now)

			if decision.action != receiveActionAdmitHistory {
				t.Fatalf("action = %v, want %v", decision.action, receiveActionAdmitHistory)
			}
			if decision.item == nil {
				t.Fatal("item is nil")
			}
			if decision.item.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", decision.item.Type, tc.wantType)
			}
			if decision.item.State != tc.wantState {
				t.Fatalf("state = %q, want %q", decision.item.State, tc.wantState)
			}
			if decision.item.Payload != tc.clipData.Payload {
				t.Fatalf("payload = %q, want %q", decision.item.Payload, tc.clipData.Payload)
			}
			if decision.item.PayloadType != tc.wantPayloadType {
				t.Fatalf("payloadType = %q, want %q", decision.item.PayloadType, tc.wantPayloadType)
			}
			if decision.item.FileName != tc.clipData.FileName {
				t.Fatalf("fileName = %q, want %q", decision.item.FileName, tc.clipData.FileName)
			}
			if tc.name == "file stub" {
				if decision.item.DisplayName != "a.txt and 1 more" {
					t.Fatalf("displayName = %q", decision.item.DisplayName)
				}
				if decision.item.TransferID != "transfer-1" {
					t.Fatalf("transferID = %q", decision.item.TransferID)
				}
				if decision.item.SourceDevice != "desktop-a" {
					t.Fatalf("sourceDevice = %q", decision.item.SourceDevice)
				}
				if decision.item.LastChunkIdx != -1 {
					t.Fatalf("lastChunkIdx = %d, want -1", decision.item.LastChunkIdx)
				}
			} else if decision.item.SourceDevice != "remote" {
				t.Fatalf("sourceDevice = %q, want %q", decision.item.SourceDevice, "remote")
			}
			if !decision.item.CreatedAt.Equal(now) || !decision.item.UpdatedAt.Equal(now) {
				t.Fatalf("timestamps = %v/%v, want %v", decision.item.CreatedAt, decision.item.UpdatedAt, now)
			}
		})
	}
}

func TestClassifyReceivedClipboardData_FileStubManifestPopulatesTransferMetadata(t *testing.T) {
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-1",
		TransferID:      "transfer-1",
		SourceSessionID: "session-a",
		SourceDevice:    "desktop-a",
		DisplayName:     "report.pdf",
		EntryCount:      1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:    constants.TypeFileStub,
		Payload: string(payload),
	}, now)

	if decision.action != receiveActionAdmitHistory {
		t.Fatalf("action = %v, want %v", decision.action, receiveActionAdmitHistory)
	}
	if decision.item == nil {
		t.Fatal("item is nil")
	}
	if decision.item.DisplayName != "report.pdf" {
		t.Fatalf("display name = %q, want %q", decision.item.DisplayName, "report.pdf")
	}
	if decision.item.TransferID != "transfer-1" {
		t.Fatalf("transferID = %q, want %q", decision.item.TransferID, "transfer-1")
	}
	if decision.item.SourceDevice != "desktop-a" {
		t.Fatalf("source device = %q, want %q", decision.item.SourceDevice, "desktop-a")
	}
	if decision.item.LastChunkIdx != -1 {
		t.Fatalf("LastChunkIdx = %d, want -1", decision.item.LastChunkIdx)
	}
}

func TestClassifyReceivedClipboardData_FileStubManifestFallsBackToSourceSessionID(t *testing.T) {
	payload, err := json.Marshal(protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-1",
		TransferID:      "transfer-1",
		SourceSessionID: "session-a",
		DisplayName:     "report.pdf",
		EntryCount:      1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:    constants.TypeFileStub,
		Payload: string(payload),
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))

	if decision.item == nil {
		t.Fatal("item is nil")
	}
	if decision.item.SourceDevice != "session-a" {
		t.Fatalf("source device = %q, want %q", decision.item.SourceDevice, "session-a")
	}
}

func TestClassifyReceivedClipboardData_FileEagerUsesLegacyPaste(t *testing.T) {
	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:     constants.TypeFileEager,
		Payload:  "legacy-base64",
		FileName: "legacy.txt",
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))

	if decision.action != receiveActionLegacyPaste {
		t.Fatalf("action = %v, want %v", decision.action, receiveActionLegacyPaste)
	}
	if decision.item != nil {
		t.Fatalf("item = %#v, want nil", decision.item)
	}
}

func TestClassifyReceivedClipboardDataTextCarriesSourceSessionID(t *testing.T) {
	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:            constants.TypeText,
		Payload:         "hello",
		SourceSessionID: "session-local",
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))

	if decision.action != receiveActionAdmitHistory {
		t.Fatalf("action = %v, want %v", decision.action, receiveActionAdmitHistory)
	}
	if decision.item == nil {
		t.Fatal("item is nil")
	}
	if decision.item.SourceSessionID != "session-local" {
		t.Fatalf("SourceSessionID = %q, want %q", decision.item.SourceSessionID, "session-local")
	}
}

func TestClassifyReceivedClipboardDataTextCarriesSourceSessionIDFromLegacyMetadata(t *testing.T) {
	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: "hello",
		Metadata: &protocol.FragmentMetadata{
			ID: "session-legacy",
		},
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))

	if decision.action != receiveActionAdmitHistory {
		t.Fatalf("action = %v, want %v", decision.action, receiveActionAdmitHistory)
	}
	if decision.item == nil {
		t.Fatal("item is nil")
	}
	if decision.item.SourceSessionID != "session-legacy" {
		t.Fatalf("SourceSessionID = %q, want %q", decision.item.SourceSessionID, "session-legacy")
	}
}

func TestClassifyReceivedClipboardData_UnsupportedIgnored(t *testing.T) {
	decision := classifyReceivedClipboardData(&protocol.ClipboardData{
		Type:    "custom",
		Payload: "payload",
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))

	if decision.action != receiveActionIgnore {
		t.Fatalf("action = %v, want %v", decision.action, receiveActionIgnore)
	}
	if decision.item != nil {
		t.Fatalf("item = %#v, want nil", decision.item)
	}
}

func TestApplicationAdmitReceivedClipboardDataAddsSupportedItemsToHistory(t *testing.T) {
	app := &Application{history: history.NewManager(10), transfers: newTransferManager("session-local")}
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	manifestPayload, err := protocol.EncodePayload(protocol.FileStubManifest{
		ProtocolVersion:     protocol.FileProtocolVersion,
		EntryID:             "entry-1",
		TransferID:          "transfer-1",
		SourceSessionID:     "session-1",
		Kind:                "multi_file",
		ArchiveFormat:       "zip",
		DisplayName:         "a.txt and 1 more",
		EntryCount:          2,
		TopLevelNames:       []string{"a.txt", "b.txt"},
		EstimatedTotalBytes: 12,
	})
	if err != nil {
		t.Fatalf("EncodePayload() error = %v", err)
	}
	app.transfers = newTransferManager()

	clips := []struct {
		clipData        *protocol.ClipboardData
		now             time.Time
		wantState       history.ItemState
		wantPayloadType string
	}{
		{
			clipData:        &protocol.ClipboardData{Type: constants.TypeText, Payload: "hello"},
			now:             base,
			wantState:       history.StateReady,
			wantPayloadType: constants.TypeText,
		},
		{
			clipData:        &protocol.ClipboardData{Type: constants.TypeImage, Payload: "img"},
			now:             base.Add(time.Minute),
			wantState:       history.StateReady,
			wantPayloadType: constants.TypeImage,
		},
		{
			clipData:        &protocol.ClipboardData{Type: constants.TypeFileStub, Payload: manifestPayload, FileName: "meta"},
			now:             base.Add(2 * time.Minute),
			wantState:       history.StateOffered,
			wantPayloadType: "",
		},
	}

	for _, tc := range clips {
		action := app.admitReceivedClipboardData(tc.clipData, tc.now)
		if action != receiveActionAdmitHistory {
			t.Fatalf("action = %v, want %v", action, receiveActionAdmitHistory)
		}
	}

	items := app.history.List()
	if len(items) != len(clips) {
		t.Fatalf("len(items) = %d, want %d", len(items), len(clips))
	}

	for _, tc := range clips {
		var found *history.HistoryItem
		for _, item := range items {
			if item.Type == tc.clipData.Type && item.Payload == tc.clipData.Payload {
				found = item
				break
			}
		}
		if found == nil {
			t.Fatalf("no history item found for type=%q payload=%q", tc.clipData.Type, tc.clipData.Payload)
		}
		if found.State != tc.wantState {
			t.Fatalf("state = %q, want %q", found.State, tc.wantState)
		}
		if found.PayloadType != tc.wantPayloadType {
			t.Fatalf("payloadType = %q, want %q", found.PayloadType, tc.wantPayloadType)
		}
	}
	if app.transfers.GetIncoming("transfer-1") == nil {
		t.Fatal("expected incoming transfer registration")
	}
}

func TestApplicationAdmitReceivedClipboardDataRegistersIncomingTransferForManifest(t *testing.T) {
	app := &Application{history: history.NewManager(10), transfers: newTransferManager("session-local")}
	payload, err := json.Marshal(protocol.FileStubManifest{
		ProtocolVersion: 1,
		EntryID:         "entry-1",
		TransferID:      "transfer-1",
		SourceSessionID: "session-remote",
		SourceDevice:    "desktop-remote",
		DisplayName:     "report.pdf",
		EntryCount:      1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	action := app.admitReceivedClipboardData(&protocol.ClipboardData{
		Type:    constants.TypeFileStub,
		Payload: string(payload),
	}, time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC))
	if action != receiveActionAdmitHistory {
		t.Fatalf("action = %v, want %v", action, receiveActionAdmitHistory)
	}

	item := app.history.GetByTransferID("transfer-1")
	if item == nil {
		t.Fatal("history item not registered")
	}
	if item.DisplayName != "report.pdf" {
		t.Fatalf("display name = %q, want %q", item.DisplayName, "report.pdf")
	}
	incoming := app.transfers.GetIncoming("transfer-1")
	if incoming == nil {
		t.Fatal("incoming transfer not registered")
	}
	if incoming.HistoryItemID == "" {
		t.Fatal("incoming transfer history item id should be persisted history id")
	}
	if incoming.HistoryItemID != item.ID {
		t.Fatalf("history item id = %q, want %q", incoming.HistoryItemID, item.ID)
	}
	if incoming.LastChunkIdx != -1 {
		t.Fatalf("LastChunkIdx = %d, want -1", incoming.LastChunkIdx)
	}
}

func TestApplicationAdmitReceivedClipboardDataDoesNotAddLegacyOrUnknownTypes(t *testing.T) {
	app := &Application{history: history.NewManager(10)}
	now := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)

	legacyAction := app.admitReceivedClipboardData(&protocol.ClipboardData{
		Type:     constants.TypeFileEager,
		Payload:  "legacy",
		FileName: "legacy.txt",
	}, now)
	if legacyAction != receiveActionLegacyPaste {
		t.Fatalf("legacy action = %v, want %v", legacyAction, receiveActionLegacyPaste)
	}

	unknownAction := app.admitReceivedClipboardData(&protocol.ClipboardData{
		Type:    "custom",
		Payload: "payload",
	}, now.Add(time.Minute))
	if unknownAction != receiveActionIgnore {
		t.Fatalf("unknown action = %v, want %v", unknownAction, receiveActionIgnore)
	}

	if items := app.history.List(); len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

func TestShouldWriteReceivedClipboardToSystemDistinguishesSelfAndRemote(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}

	selfText := &protocol.ClipboardData{
		Type:            constants.TypeText,
		Payload:         "self",
		SourceSessionID: "session-local",
	}
	if app.shouldWriteReceivedClipboardToSystem(selfText) {
		t.Fatal("self text should not be written to system clipboard")
	}

	remoteImage := &protocol.ClipboardData{
		Type:            constants.TypeImage,
		Payload:         "image",
		SourceSessionID: "session-remote",
	}
	if !app.shouldWriteReceivedClipboardToSystem(remoteImage) {
		t.Fatal("remote image should be written to system clipboard")
	}

	legacyRemoteText := &protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: "legacy",
	}
	if !app.shouldWriteReceivedClipboardToSystem(legacyRemoteText) {
		t.Fatal("metadata-less text should be treated as remote for compatibility")
	}
}

func TestOnReceiveSkipsSelfTextWritebackAndWritesRemoteImageImmediately(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		cfg:       &config.Config{},
		history:   history.NewManager(10),
		clip:      &clipboard.Manager{},
		transfers: newTransferManager("session-local"),
	}

	var pasted []string
	origPaste := appPasteClipboardPayload
	appPasteClipboardPayload = func(a *Application, payload string, payloadType string, fileName string) {
		pasted = append(pasted, payloadType+":"+payload)
	}
	t.Cleanup(func() { appPasteClipboardPayload = origPaste })

	selfTextBody, err := (&protocol.ClipboardData{
		Type:            constants.TypeText,
		Payload:         "self-text",
		SourceSessionID: "session-local",
	}).Encode()
	if err != nil {
		t.Fatalf("Encode(selfText) error = %v", err)
	}
	app.onReceive(string(selfTextBody))
	if len(pasted) != 0 {
		t.Fatalf("pasted = %#v, want empty for self text", pasted)
	}

	remoteImageBody, err := (&protocol.ClipboardData{
		Type:            constants.TypeImage,
		Payload:         "remote-image",
		SourceSessionID: "session-remote",
	}).Encode()
	if err != nil {
		t.Fatalf("Encode(remoteImage) error = %v", err)
	}
	app.onReceive(string(remoteImageBody))
	if len(pasted) != 1 {
		t.Fatalf("len(pasted) = %d, want 1", len(pasted))
	}
	if pasted[0] != constants.TypeImage+":remote-image" {
		t.Fatalf("pasted[0] = %q, want %q", pasted[0], constants.TypeImage+":remote-image")
	}
}
