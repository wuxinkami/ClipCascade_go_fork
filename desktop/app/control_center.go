package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/clipcascade/desktop/config"
	"github.com/clipcascade/pkg/constants"
)

const controlEventLimit = 40

func (a *Application) recordControlEvent(kind, message string) {
	if a == nil {
		return
	}
	event := historyPanelEvent{
		Time:    time.Now(),
		Kind:    kind,
		Message: message,
	}
	a.controlEventMu.Lock()
	defer a.controlEventMu.Unlock()

	a.controlEvents = append([]historyPanelEvent{event}, a.controlEvents...)
	if len(a.controlEvents) > controlEventLimit {
		a.controlEvents = a.controlEvents[:controlEventLimit]
	}
}

func (a *Application) historyPanelEvents() []historyPanelEvent {
	if a == nil {
		return nil
	}
	a.controlEventMu.Lock()
	defer a.controlEventMu.Unlock()

	events := make([]historyPanelEvent, len(a.controlEvents))
	copy(events, a.controlEvents)
	return events
}

func (a *Application) historyPanelDevices() historyPanelDeviceSnapshot {
	devices := historyPanelDeviceSnapshot{
		LocalSessionID: a.appSessionID(),
	}
	if a == nil {
		return devices
	}
	if a.p2p != nil {
		snapshot := a.p2p.Snapshot()
		devices.P2PSessionID = snapshot.AssignedSessionID
		devices.PeerIDs = snapshot.PeerIDs
		devices.ReadyPeerIDs = snapshot.ReadyPeerIDs
	}
	return devices
}

func (a *Application) historyPanelSettings() historyPanelSettings {
	settings := historyPanelSettings{
		ServerURL:              "",
		Username:               "",
		PasswordConfigured:     false,
		E2EEEnabled:            true,
		P2PEnabled:             true,
		StunURL:                constants.DefaultStunURL,
		AutoReconnect:          true,
		ReconnectDelaySec:      constants.DefaultReconnectDelay,
		FileMemoryThresholdMiB: constants.DefaultFileMemoryThresholdMiB,
	}
	if a == nil || a.cfg == nil {
		return settings
	}
	settings.ServerURL = a.cfg.ServerURL
	settings.Username = a.cfg.Username
	settings.PasswordConfigured = strings.TrimSpace(a.cfg.Password) != ""
	settings.E2EEEnabled = a.cfg.E2EEEnabled
	settings.P2PEnabled = a.cfg.P2PEnabled
	settings.StunURL = a.cfg.StunURL
	if settings.StunURL == "" {
		settings.StunURL = constants.DefaultStunURL
	}
	settings.AutoReconnect = a.cfg.AutoReconnect
	settings.ReconnectDelaySec = a.cfg.ReconnectDelay
	if settings.ReconnectDelaySec <= 0 {
		settings.ReconnectDelaySec = constants.DefaultReconnectDelay
	}
	settings.FileMemoryThresholdMiB = a.cfg.FileMemoryThresholdBytes() >> 20
	return settings
}

func (a *Application) saveHistoryPanelSettings(input historyPanelSettingsInput) (historyPanelSettings, error) {
	if a == nil || a.cfg == nil {
		return historyPanelSettings{}, errors.New("settings unavailable")
	}

	cfg := *a.cfg
	if trimmed := strings.TrimSpace(input.ServerURL); trimmed != "" {
		cfg.ServerURL = config.NormalizeServerURL(trimmed)
	}
	if trimmed := strings.TrimSpace(input.Username); trimmed != "" {
		cfg.Username = trimmed
	}
	if strings.TrimSpace(input.Password) != "" {
		cfg.Password = input.Password
	}
	cfg.E2EEEnabled = input.E2EEEnabled
	cfg.P2PEnabled = input.P2PEnabled
	cfg.StunURL = strings.TrimSpace(input.StunURL)
	if cfg.StunURL == "" {
		cfg.StunURL = constants.DefaultStunURL
	}
	cfg.AutoReconnect = input.AutoReconnect
	cfg.ReconnectDelay = input.ReconnectDelaySec
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = constants.DefaultReconnectDelay
	}
	cfg.FileMemoryThresholdMiB = input.FileMemoryThresholdMiB
	normalizedThreshold := cfg.FileMemoryThresholdBytes() >> 20
	cfg.FileMemoryThresholdMiB = normalizedThreshold
	cfg.ServerURL = config.NormalizeServerURL(cfg.ServerURL)

	if strings.TrimSpace(cfg.ServerURL) == "" {
		return historyPanelSettings{}, errors.New("server_url is required")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return historyPanelSettings{}, errors.New("username is required")
	}

	if err := cfg.Save(); err != nil {
		return historyPanelSettings{}, fmt.Errorf("save settings: %w", err)
	}
	a.cfg = &cfg
	a.recordControlEvent("settings", "Saved control center settings")
	return a.historyPanelSettings(), nil
}

func (a *Application) historyPanelFileTransfers() []historyPanelFileTransfer {
	if a == nil || a.history == nil {
		return nil
	}

	items := a.history.List()
	transfers := make([]historyPanelFileTransfer, 0)
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.Type != constants.TypeFileStub && item.Type != constants.TypeFileEager {
			continue
		}

		task := historyPanelFileTransfer{
			ID:             item.ID,
			TransferID:     item.TransferID,
			DisplayName:    summarizeHistoryItem(item),
			State:          string(item.State),
			SourceDevice:   item.SourceDevice,
			SizeBytes:      historyItemSizeBytes(item),
			SizeHuman:      historyItemSizeHuman(item),
			LastChunkIdx:   item.LastChunkIdx,
			LocalPathCount: len(item.LocalPaths),
			ErrorMessage:   item.ErrorMessage,
			UpdatedAt:      item.UpdatedAt,
		}

		if incoming := a.transfers.GetIncoming(item.TransferID); incoming != nil {
			task.TotalChunks = incoming.TotalChunks
			if incoming.TotalChunks > 0 && incoming.LastChunkIdx >= 0 {
				task.ProgressPercent = ((incoming.LastChunkIdx + 1) * 100) / incoming.TotalChunks
				if task.ProgressPercent > 100 {
					task.ProgressPercent = 100
				}
			}
		}
		transfers = append(transfers, task)
	}
	return transfers
}
