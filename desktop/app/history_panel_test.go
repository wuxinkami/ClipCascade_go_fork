package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

func TestHistoryPanelServerServesHTMLAndListJSON(t *testing.T) {
	manager := history.NewManager(10)
	base := time.Date(2026, 3, 15, 16, 0, 0, 0, time.UTC)
	filePayload, err := protocol.EncodePayload(protocol.FileStubManifest{
		ProtocolVersion:     protocol.FileProtocolVersion,
		EntryID:             "entry-1",
		TransferID:          "transfer-1",
		SourceSessionID:     "session-a",
		SourceDevice:        "remote-b",
		Kind:                "single_file",
		ArchiveFormat:       "zip",
		DisplayName:         "archive.zip",
		EntryCount:          1,
		TopLevelNames:       []string{"archive.zip"},
		EstimatedTotalBytes: 5 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("EncodePayload(file stub) error = %v", err)
	}

	manager.AddItem(&history.HistoryItem{
		ID:           "text-1",
		Type:         constants.TypeText,
		State:        history.StateReady,
		Payload:      "hello from history panel",
		SourceDevice: "remote-a",
		CreatedAt:    base,
		UpdatedAt:    base,
	})
	manager.AddItem(&history.HistoryItem{
		ID:           "file-1",
		Type:         constants.TypeFileStub,
		State:        history.StateOffered,
		Payload:      filePayload,
		DisplayName:  "archive.zip",
		FileName:     "archive.zip",
		SourceDevice: "remote-b",
		CreatedAt:    base.Add(time.Minute),
		UpdatedAt:    base.Add(time.Minute),
	})
	manager.AddItem(&history.HistoryItem{
		ID:           "file-2",
		Type:         constants.TypeFileStub,
		State:        history.StateFailed,
		Payload:      filePayload,
		DisplayName:  "archive.zip",
		FileName:     "archive-failed.zip",
		SourceDevice: "remote-c",
		CreatedAt:    base.Add(2 * time.Minute),
		UpdatedAt:    base.Add(2 * time.Minute),
	})

	panel := newHistoryPanelServer(manager, func(id string, mode ReplayMode) error { return nil })
	panelURL, err := panel.EnsureStarted(0)
	if err != nil {
		t.Fatalf("EnsureStarted() error = %v", err)
	}
	t.Cleanup(func() {
		if err := panel.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	resp, err := http.Get(panelURL)
	if err != nil {
		t.Fatalf("GET panel root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel root status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(panelURL + "api/list")
	if err != nil {
		t.Fatalf("GET list api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list api status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var payload historyPanelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode list api: %v", err)
	}
	if len(payload.Items) != 3 {
		t.Fatalf("list api returned %d items, want 3", len(payload.Items))
	}
	if payload.Overview.ConnectionStatus != "Disconnected" {
		t.Fatalf("connection status = %q, want %q", payload.Overview.ConnectionStatus, "Disconnected")
	}
	if payload.ActiveID != "file-2" {
		t.Fatalf("active id = %q, want %q", payload.ActiveID, "file-2")
	}
	if !payload.Items[0].Active {
		t.Fatalf("newest item should be active by default")
	}
	if !payload.Items[0].Replayable {
		t.Fatalf("failed file stub item should be replayable")
	}
	if payload.Items[1].ID != "file-1" || !payload.Items[1].Replayable {
		t.Fatalf("offered file stub replayability mismatch: %+v", payload.Items[1])
	}
	if payload.Items[1].SizeHuman != "5.00 MB" {
		t.Fatalf("file size = %q, want %q", payload.Items[1].SizeHuman, "5.00 MB")
	}
	if payload.Items[2].ID != "text-1" || !payload.Items[2].Replayable {
		t.Fatalf("text item replayability mismatch: %+v", payload.Items[2])
	}
	if payload.Items[2].SizeHuman != "24 B" {
		t.Fatalf("text size = %q, want %q", payload.Items[2].SizeHuman, "24 B")
	}
	if payload.Actions.CanReplayActive != payload.Items[0].Replayable {
		t.Fatalf("active replayability mismatch: actions=%v active=%v", payload.Actions.CanReplayActive, payload.Items[0].Replayable)
	}
}

func TestHistoryPanelServerSetActiveEndpointUpdatesHistory(t *testing.T) {
	manager := history.NewManager(10)
	base := time.Date(2026, 3, 15, 16, 30, 0, 0, time.UTC)

	manager.AddItem(&history.HistoryItem{
		ID:        "text-1",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "first",
		CreatedAt: base,
		UpdatedAt: base,
	})
	manager.AddItem(&history.HistoryItem{
		ID:        "text-2",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "second",
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(time.Minute),
	})

	panel := newHistoryPanelServer(manager, func(id string, mode ReplayMode) error { return nil })
	panelURL, err := panel.EnsureStarted(0)
	if err != nil {
		t.Fatalf("EnsureStarted() error = %v", err)
	}
	t.Cleanup(func() {
		if err := panel.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	resp, err := http.Post(panelURL+"api/set-active", "application/json", bytes.NewBufferString(`{"id":"text-1"}`))
	if err != nil {
		t.Fatalf("POST set-active api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set-active api status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	active := manager.GetActive()
	if active == nil || active.ID != "text-1" {
		t.Fatalf("active item = %+v, want text-1", active)
	}
}

func TestHistoryPanelServerReplayEndpointUsesInjectedCallback(t *testing.T) {
	manager := history.NewManager(10)
	now := time.Date(2026, 3, 15, 17, 0, 0, 0, time.UTC)
	manager.AddItem(&history.HistoryItem{
		ID:        "text-1",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "replay me",
		CreatedAt: now,
		UpdatedAt: now,
	})

	var replayedID string
	panel := newHistoryPanelServer(manager, func(id string, mode ReplayMode) error {
		replayedID = id
		return nil
	})
	panelURL, err := panel.EnsureStarted(0)
	if err != nil {
		t.Fatalf("EnsureStarted() error = %v", err)
	}
	t.Cleanup(func() {
		if err := panel.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	resp, err := http.Post(panelURL+"api/replay", "application/json", bytes.NewBufferString(`{"id":"text-1"}`))
	if err != nil {
		t.Fatalf("POST replay api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay api status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if replayedID != "text-1" {
		t.Fatalf("replay callback id = %q, want %q", replayedID, "text-1")
	}
}

func TestHistoryPanelServerActionEndpointsAndOverviewUseInjectedCallbacks(t *testing.T) {
	manager := history.NewManager(10)
	now := time.Date(2026, 3, 15, 17, 10, 0, 0, time.UTC)
	manager.AddItem(&history.HistoryItem{
		ID:        "text-1",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "console action",
		CreatedAt: now,
		UpdatedAt: now,
	})

	var replayedID string
	sendCalls := 0
	connectCalls := 0
	disconnectCalls := 0
	saveCalls := 0
	var savedInput historyPanelSettingsInput

	panel := newHistoryPanelServer(manager, func(id string, mode ReplayMode) error {
		replayedID = id
		return nil
	})
	panel.SetSendCurrent(func() error {
		sendCalls++
		return nil
	})
	panel.SetConnect(func() { connectCalls++ })
	panel.SetDisconnect(func() { disconnectCalls++ })
	panel.SetOverviewProvider(func() historyPanelOverview {
		return historyPanelOverview{
			ConnectionStatus:       "Connected ✓",
			ServerURL:              "http://127.0.0.1:8080",
			Username:               "admin",
			E2EEEnabled:            true,
			P2PEnabled:             true,
			P2PReadyPeers:          2,
			FileMemoryThresholdMiB: 1024,
		}
	})
	panel.SetDevicesProvider(func() historyPanelDeviceSnapshot {
		return historyPanelDeviceSnapshot{
			LocalSessionID: "session-local",
			P2PSessionID:   "p2p-local",
			PeerIDs:        []string{"peer-a", "peer-b"},
			ReadyPeerIDs:   []string{"peer-b"},
		}
	})
	panel.SetSettingsProvider(func() historyPanelSettings {
		return historyPanelSettings{
			ServerURL:              "http://127.0.0.1:8080",
			Username:               "admin",
			PasswordConfigured:     true,
			E2EEEnabled:            true,
			P2PEnabled:             true,
			StunURL:                constants.DefaultStunURL,
			AutoReconnect:          true,
			ReconnectDelaySec:      5,
			FileMemoryThresholdMiB: 1024,
		}
	})
	panel.SetSettingsSaver(func(input historyPanelSettingsInput) (historyPanelSettings, error) {
		saveCalls++
		savedInput = input
		return historyPanelSettings{
			ServerURL:              input.ServerURL,
			Username:               input.Username,
			PasswordConfigured:     true,
			E2EEEnabled:            input.E2EEEnabled,
			P2PEnabled:             input.P2PEnabled,
			StunURL:                input.StunURL,
			AutoReconnect:          input.AutoReconnect,
			ReconnectDelaySec:      input.ReconnectDelaySec,
			FileMemoryThresholdMiB: input.FileMemoryThresholdMiB,
		}, nil
	})
	panel.SetFileTransfersProvider(func() []historyPanelFileTransfer {
		return []historyPanelFileTransfer{
			{
				ID:              "file-1",
				TransferID:      "transfer-1",
				DisplayName:     "archive.zip",
				State:           "downloading",
				SourceDevice:    "remote-b",
				SizeBytes:       10 * 1024 * 1024,
				SizeHuman:       "10.00 MB",
				ProgressPercent: 75,
				TotalChunks:     8,
				LastChunkIdx:    5,
				UpdatedAt:       now,
			},
		}
	})
	panel.SetEventsProvider(func() []historyPanelEvent {
		return []historyPanelEvent{
			{
				Time:    now,
				Kind:    "connect",
				Message: "Connected to server",
			},
		}
	})

	panelURL, err := panel.EnsureStarted(0)
	if err != nil {
		t.Fatalf("EnsureStarted() error = %v", err)
	}
	t.Cleanup(func() {
		if err := panel.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	resp, err := http.Get(panelURL + "api/list")
	if err != nil {
		t.Fatalf("GET list api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list api status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var payload historyPanelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode list api: %v", err)
	}
	if payload.Overview.ConnectionStatus != "Connected ✓" {
		t.Fatalf("connection status = %q, want %q", payload.Overview.ConnectionStatus, "Connected ✓")
	}
	if payload.Overview.FileMemoryThresholdMiB != 1024 {
		t.Fatalf("file memory threshold = %d, want 1024", payload.Overview.FileMemoryThresholdMiB)
	}
	if !payload.Actions.CanDisconnect || payload.Actions.CanConnect {
		t.Fatalf("unexpected actions state: %+v", payload.Actions)
	}
	if !payload.Actions.CanSendCurrent || !payload.Actions.CanReplayActive {
		t.Fatalf("missing action availability: %+v", payload.Actions)
	}
	if !payload.Actions.CanSaveSettings {
		t.Fatalf("expected save settings action to be available")
	}
	if payload.Devices.LocalSessionID != "session-local" || len(payload.Devices.ReadyPeerIDs) != 1 {
		t.Fatalf("devices payload mismatch: %+v", payload.Devices)
	}
	if payload.Settings.ServerURL != "http://127.0.0.1:8080" || !payload.Settings.PasswordConfigured {
		t.Fatalf("settings payload mismatch: %+v", payload.Settings)
	}
	if len(payload.FileTransfers) != 1 || payload.FileTransfers[0].ProgressPercent != 75 {
		t.Fatalf("file transfer payload mismatch: %+v", payload.FileTransfers)
	}
	if len(payload.Events) != 1 || payload.Events[0].Kind != "connect" {
		t.Fatalf("event payload mismatch: %+v", payload.Events)
	}

	for _, tc := range []struct {
		name string
		url  string
		body string
	}{
		{name: "send current", url: panelURL + "api/send-current"},
		{name: "connect", url: panelURL + "api/connect"},
		{name: "disconnect", url: panelURL + "api/disconnect"},
		{name: "replay active", url: panelURL + "api/replay"},
	} {
		resp, err := http.Post(tc.url, "application/json", bytes.NewBufferString(tc.body))
		if err != nil {
			t.Fatalf("POST %s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", tc.name, resp.StatusCode, http.StatusOK)
		}
	}

	if sendCalls != 1 {
		t.Fatalf("sendCalls = %d, want 1", sendCalls)
	}
	if connectCalls != 1 {
		t.Fatalf("connectCalls = %d, want 1", connectCalls)
	}
	if disconnectCalls != 1 {
		t.Fatalf("disconnectCalls = %d, want 1", disconnectCalls)
	}
	if replayedID != "text-1" {
		t.Fatalf("replayedID = %q, want %q", replayedID, "text-1")
	}

	resp, err = http.Post(panelURL+"api/settings", "application/json", bytes.NewBufferString(`{"server_url":"http://10.0.0.8:8080","username":"operator","password":"secret","e2ee_enabled":true,"p2p_enabled":false,"stun_url":"stun:example.org:3478","auto_reconnect":true,"reconnect_delay_sec":9,"file_memory_threshold_mib":2048}`))
	if err != nil {
		t.Fatalf("POST settings api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings api status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if saveCalls != 1 {
		t.Fatalf("saveCalls = %d, want 1", saveCalls)
	}
	if savedInput.Username != "operator" || savedInput.FileMemoryThresholdMiB != 2048 || savedInput.P2PEnabled {
		t.Fatalf("savedInput mismatch: %+v", savedInput)
	}
}

func TestHistoryPanelServerRejectsUntokenizedAndInvalidRequests(t *testing.T) {
	manager := history.NewManager(10)
	now := time.Date(2026, 3, 15, 17, 30, 0, 0, time.UTC)
	manager.AddItem(&history.HistoryItem{
		ID:        "text-1",
		Type:      constants.TypeText,
		State:     history.StateReady,
		Payload:   "hello",
		CreatedAt: now,
		UpdatedAt: now,
	})

	panel := newHistoryPanelServer(manager, func(id string, mode ReplayMode) error { return nil })
	panelURL, err := panel.EnsureStarted(0)
	if err != nil {
		t.Fatalf("EnsureStarted() error = %v", err)
	}
	t.Cleanup(func() {
		if err := panel.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	parsedURL, err := url.Parse(panelURL)
	if err != nil {
		t.Fatalf("url.Parse(panelURL) error = %v", err)
	}
	baseHost := parsedURL.Scheme + "://" + parsedURL.Host
	resp, err := http.Get(baseHost + "/api/list")
	if err != nil {
		t.Fatalf("GET untokenized list api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("untokenized list api status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp, err = http.Post(panelURL+"api/set-active", "application/json", bytes.NewBufferString(`{"id":"missing"}`))
	if err != nil {
		t.Fatalf("POST missing set-active api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing set-active status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	resp, err = http.Post(panelURL+"api/set-active", "application/json", bytes.NewBufferString(`{"id":`))
	if err != nil {
		t.Fatalf("POST invalid set-active api: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid set-active status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode invalid request response: %v", err)
	}
	if !strings.Contains(payload["error"].(string), "invalid JSON body") {
		t.Fatalf("error message = %q, want invalid JSON body", payload["error"])
	}
}
