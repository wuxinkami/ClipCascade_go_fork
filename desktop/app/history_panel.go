package app

import (
	"context"
	_ "embed"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/clipcascade/desktop/history"
	"github.com/clipcascade/desktop/ui"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
	"github.com/clipcascade/pkg/sizefmt"
)

var ErrHistoryItemNotFound = errors.New("history item not found")

var appOpenHistoryPanel = func(a *Application) {
	a.openHistoryPanel()
}

type historyPanelServer struct {
	history    *history.Manager
	replay     func(id string, mode ReplayMode) error
	send       func() error
	connect    func()
	disconnect func()
	overview   func() historyPanelOverview
	devices    func() historyPanelDeviceSnapshot
	settings   func() historyPanelSettings
	save       func(input historyPanelSettingsInput) (historyPanelSettings, error)
	transfers  func() []historyPanelFileTransfer
	events     func() []historyPanelEvent
	needsSetup func() bool // 判断是否需要首次配置引导

	mu     sync.Mutex
	server *http.Server
	url    string
}

func newHistoryPanelServer(m *history.Manager, replay func(id string, mode ReplayMode) error) *historyPanelServer {
	return &historyPanelServer{
		history: m,
		replay:  replay,
	}
}

func (s *historyPanelServer) SetSendCurrent(fn func() error) {
	if s == nil {
		return
	}
	s.send = fn
}

func (s *historyPanelServer) SetConnect(fn func()) {
	if s == nil {
		return
	}
	s.connect = fn
}

func (s *historyPanelServer) SetDisconnect(fn func()) {
	if s == nil {
		return
	}
	s.disconnect = fn
}

func (s *historyPanelServer) SetOverviewProvider(fn func() historyPanelOverview) {
	if s == nil {
		return
	}
	s.overview = fn
}

func (s *historyPanelServer) SetDevicesProvider(fn func() historyPanelDeviceSnapshot) {
	if s == nil {
		return
	}
	s.devices = fn
}

func (s *historyPanelServer) SetSettingsProvider(fn func() historyPanelSettings) {
	if s == nil {
		return
	}
	s.settings = fn
}

func (s *historyPanelServer) SetSettingsSaver(fn func(input historyPanelSettingsInput) (historyPanelSettings, error)) {
	if s == nil {
		return
	}
	s.save = fn
}

func (s *historyPanelServer) SetFileTransfersProvider(fn func() []historyPanelFileTransfer) {
	if s == nil {
		return
	}
	s.transfers = fn
}

func (s *historyPanelServer) SetEventsProvider(fn func() []historyPanelEvent) {
	if s == nil {
		return
	}
	s.events = fn
}

// SetNeedsSetup 设置首次配置引导判断回调。
func (s *historyPanelServer) SetNeedsSetup(fn func() bool) {
	if s == nil {
		return
	}
	s.needsSetup = fn
}

func (s *historyPanelServer) EnsureStarted(webPort int) (string, error) {
	if s == nil {
		return "", errors.New("history panel server is nil")
	}

	s.mu.Lock()
	if s.server != nil && s.url != "" {
		url := s.url
		s.mu.Unlock()
		return url, nil
	}

	token, err := historyPanelToken()
	if err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("generate history panel token: %w", err)
	}

	// 使用配置的固定端口（默认 6666），如果被占用回退到随机端口
	if webPort <= 0 || webPort > 65535 {
		webPort = 6666
	}
	listenAddr := fmt.Sprintf("127.0.0.1:%d", webPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Warn("history panel: 固定端口被占用，回退到随机端口", "端口", webPort, "error", err)
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			s.mu.Unlock()
			return "", fmt.Errorf("listen for history panel: %w", err)
		}
	}

	basePath := "/" + token + "/"
	mux := http.NewServeMux()
	mux.HandleFunc("/"+token, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, basePath, http.StatusTemporaryRedirect)
	})
	mux.HandleFunc(basePath+"api/list", s.handleList)
	mux.HandleFunc(basePath+"api/set-active", s.handleSetActive)
	mux.HandleFunc(basePath+"api/replay", s.handleReplay)
	mux.HandleFunc(basePath+"api/send-current", s.handleSendCurrent)
	mux.HandleFunc(basePath+"api/connect", s.handleConnect)
	mux.HandleFunc(basePath+"api/disconnect", s.handleDisconnect)
	mux.HandleFunc(basePath+"api/settings", s.handleSettings)
	mux.HandleFunc(basePath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != basePath {
			http.NotFound(w, r)
			return
		}
		s.handleIndex(w, r, basePath)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	url := "http://" + ln.Addr().String() + basePath
	s.server = server
	s.url = url
	s.mu.Unlock()

	slog.Info("history panel: 控制面板已启动", "地址", ln.Addr().String())

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("history panel HTTP server stopped unexpectedly", "error", err)
		}
	}()

	return url, nil
}

func (s *historyPanelServer) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	server := s.server
	s.server = nil
	s.url = ""
	s.mu.Unlock()

	if server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return server.Shutdown(ctx)
}

func (a *Application) openHistoryPanel() {
	if a == nil || a.historyPanel == nil {
		ui.Notify("ClipCascade", "Control center is unavailable / 控制中心不可用")
		slog.Warn("application: control center service unavailable")
		return
	}

	webPort := 6666
	if a.cfg != nil && a.cfg.WebPort > 0 {
		webPort = a.cfg.WebPort
	}
	panelURL, err := a.historyPanel.EnsureStarted(webPort)
	if err != nil {
		ui.Notify("ClipCascade", "Failed to start control center / 启动控制中心服务失败")
		slog.Error("application: failed to start control center", "error", err)
		return
	}

	if err := openHistoryPanelBrowser(panelURL); err != nil {
		ui.Notify("ClipCascade", "Failed to open control center. URL logged. / 打开控制中心页面失败，请查看日志")
		slog.Error("application: failed to open control center; open URL manually", "error", err, "url", panelURL)
		return
	}

	slog.Info("application: opened control center", "url", panelURL)
}

func (a *Application) historyPanelOverview() historyPanelOverview {
	overview := historyPanelOverview{
		ConnectionStatus: "Disconnected",
	}
	if a == nil {
		return overview
	}

	a.connMu.Lock()
	connecting := a.connecting
	reconnecting := a.reconnecting
	a.connMu.Unlock()

	switch {
	case a.stomp != nil && a.stomp.IsConnected():
		overview.ConnectionStatus = "Connected ✓"
	case reconnecting:
		overview.ConnectionStatus = "Reconnecting..."
	case connecting:
		overview.ConnectionStatus = "Connecting..."
	}

	if a.cfg != nil {
		overview.ServerURL = a.cfg.ServerURL
		overview.Username = a.cfg.Username
		overview.E2EEEnabled = a.cfg.E2EEEnabled
		overview.P2PEnabled = a.cfg.P2PEnabled
		overview.FileMemoryThresholdMiB = a.cfg.FileMemoryThresholdBytes() >> 20
	}
	if a.p2p != nil {
		overview.P2PReadyPeers = a.p2p.ReadyPeerCount()
	}

	return overview
}

func (s *historyPanelServer) handleIndex(w http.ResponseWriter, r *http.Request, basePath string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	page := strings.ReplaceAll(historyPanelHTML, "__BASE_PATH__", basePath)
	// 注入首次配置引导标记
	needsSetup := "false"
	if s.needsSetup != nil && s.needsSetup() {
		needsSetup = "true"
	}
	page = strings.ReplaceAll(page, "__NEEDS_SETUP__", needsSetup)
	if _, err := io.WriteString(w, page); err != nil {
		slog.Warn("history panel: failed to write index page", "error", err)
	}
}

func (s *historyPanelServer) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}

	writeHistoryPanelJSON(w, http.StatusOK, s.snapshot())
}

func (s *historyPanelServer) handleSetActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.history == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("history manager unavailable"))
		return
	}

	id, _, err := historyPanelRequestInput(r)
	if err != nil {
		writeHistoryPanelJSONError(w, http.StatusBadRequest, err)
		return
	}
	if id == "" {
		writeHistoryPanelJSONError(w, http.StatusBadRequest, errors.New("missing history item id"))
		return
	}
	if !s.history.SetActive(id) {
		writeHistoryPanelJSONError(w, http.StatusNotFound, ErrHistoryItemNotFound)
		return
	}

	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"active_id": id,
	})
}

func (s *historyPanelServer) handleReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.history == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("history manager unavailable"))
		return
	}
	if s.replay == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("history replay unavailable"))
		return
	}

	id, mode, err := historyPanelRequestInput(r)
	if err != nil {
		writeHistoryPanelJSONError(w, http.StatusBadRequest, err)
		return
	}

	item := s.history.GetActive()
	if id != "" {
		item = s.history.GetByID(id)
		if item == nil {
			writeHistoryPanelJSONError(w, http.StatusNotFound, ErrHistoryItemNotFound)
			return
		}
	}

	if err := validateReplayableHistoryItem(item); err != nil {
		writeHistoryPanelJSONError(w, historyPanelErrorStatus(err), err)
		return
	}

	replayID := historyPanelItemID(item)
	if err := s.replay(replayID, mode); err != nil {
		writeHistoryPanelJSONError(w, historyPanelErrorStatus(err), err)
		return
	}

	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"active_id": historyPanelItemID(item),
	})
}

func (s *historyPanelServer) handleSendCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.send == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("send current clipboard unavailable"))
		return
	}
	if err := s.send(); err != nil {
		writeHistoryPanelJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *historyPanelServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.connect == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("connect action unavailable"))
		return
	}
	s.connect()
	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *historyPanelServer) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.disconnect == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("disconnect action unavailable"))
		return
	}
	s.disconnect()
	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *historyPanelServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeHistoryPanelJSONError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.save == nil {
		writeHistoryPanelJSONError(w, http.StatusServiceUnavailable, errors.New("settings save unavailable"))
		return
	}
	if r.Body == nil {
		writeHistoryPanelJSONError(w, http.StatusBadRequest, errors.New("missing JSON body"))
		return
	}
	defer r.Body.Close()

	var input historyPanelSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		if errors.Is(err, io.EOF) {
			writeHistoryPanelJSONError(w, http.StatusBadRequest, errors.New("missing JSON body"))
			return
		}
		writeHistoryPanelJSONError(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}

	settings, err := s.save(input)
	if err != nil {
		writeHistoryPanelJSONError(w, http.StatusBadRequest, err)
		return
	}

	writeHistoryPanelJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"settings": settings,
	})
}

func (s *historyPanelServer) snapshot() historyPanelListResponse {
	response := historyPanelListResponse{
		Items:         make([]historyPanelListItem, 0),
		FileTransfers: make([]historyPanelFileTransfer, 0),
		Events:        make([]historyPanelEvent, 0),
	}
	if s == nil || s.history == nil {
		return response
	}

	active := s.history.GetActive()
	activeID := historyPanelItemID(active)
	items := s.history.List()
	if s.overview != nil {
		response.Overview = s.overview()
	}
	if s.devices != nil {
		response.Devices = s.devices()
	}
	if s.settings != nil {
		response.Settings = s.settings()
	}
	if s.transfers != nil {
		response.FileTransfers = s.transfers()
	}
	if s.events != nil {
		response.Events = s.events()
	}
	if response.Overview.ConnectionStatus == "" {
		response.Overview.ConnectionStatus = "Disconnected"
	}
	response.ActiveID = activeID
	response.Items = make([]historyPanelListItem, 0, len(items))

	replayableCount := 0
	for _, item := range items {
		if item == nil {
			continue
		}
		if canReplayHistoryItem(item) {
			replayableCount++
		}
		response.Items = append(response.Items, historyPanelListItem{
			ID:           item.ID,
			Type:         item.Type,
			State:        string(item.State),
			DisplayName:  item.DisplayName,
			FileName:     item.FileName,
			SourceDevice: item.SourceDevice,
			CreatedAt:    item.CreatedAt,
			UpdatedAt:    item.UpdatedAt,
			ErrorMessage: item.ErrorMessage,
			Summary:      summarizeHistoryItem(item),
			SizeBytes:    historyItemSizeBytes(item),
			SizeHuman:    historyItemSizeHuman(item),
			Active:       item.ID != "" && item.ID == activeID,
			Replayable:   canReplayHistoryItem(item),
		})
	}

	response.Overview.TotalItems = len(response.Items)
	response.Overview.ReplayableItems = replayableCount
	if active != nil {
		response.Overview.ActiveSummary = summarizeHistoryItem(active)
		response.Overview.ActiveType = active.Type
		response.Overview.ActiveState = string(active.State)
	}
	status := response.Overview.ConnectionStatus
	response.Actions = historyPanelActionState{
		CanConnect:      s.connect != nil && status != "Connected ✓" && status != "Connecting..." && status != "Reconnecting...",
		CanDisconnect:   s.disconnect != nil && status == "Connected ✓",
		CanSendCurrent:  s.send != nil,
		CanReplayActive: s.replay != nil && canReplayHistoryItem(active),
		CanSaveSettings: s.save != nil,
	}

	return response
}

func summarizeHistoryItem(item *history.HistoryItem) string {
	if item == nil {
		return ""
	}

	if item.DisplayName != "" {
		return item.DisplayName
	}

	switch item.Type {
	case constants.TypeText:
		text := strings.Join(strings.Fields(item.Payload), " ")
		if text == "" {
			return "(empty text)"
		}
		const maxLen = 160
		if len(text) > maxLen {
			return text[:maxLen-3] + "..."
		}
		return text
	case constants.TypeImage:
		if item.FileName != "" {
			return item.FileName
		}
		return "Image clipboard item"
	case constants.TypeFileStub, constants.TypeFileEager:
		if item.FileName != "" {
			return item.FileName
		}
		return "File item"
	default:
		if item.FileName != "" {
			return item.FileName
		}
		return item.Type
	}
}

func historyItemSizeBytes(item *history.HistoryItem) int64 {
	if item == nil {
		return 0
	}

	switch item.Type {
	case constants.TypeText, constants.TypeImage, constants.TypeFileEager:
		if item.Type == constants.TypeImage || item.Type == constants.TypeFileEager {
			return int64(sizefmt.EstimatedBase64DecodedSize(item.Payload))
		}
		return int64(len(item.Payload))
	case constants.TypeFileStub:
		manifest, err := protocol.DecodePayload[protocol.FileStubManifest](item.Payload)
		if err != nil || manifest == nil {
			return 0
		}
		return manifest.EstimatedTotalBytes
	default:
		return int64(len(item.Payload))
	}
}

func historyItemSizeHuman(item *history.HistoryItem) string {
	size := historyItemSizeBytes(item)
	if size <= 0 {
		return ""
	}
	return sizefmt.FormatBytes(size)
}

func validateReplayableHistoryItem(item *history.HistoryItem) error {
	if item == nil {
		return ErrNoActiveHistoryItem
	}
	if canReplayHistoryItem(item) {
		return nil
	}

	switch item.Type {
	case constants.TypeText, constants.TypeImage, constants.TypeFileStub, constants.TypeFileEager:
		return fmt.Errorf("%w: %s", ErrUnsupportedReplayState, item.State)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedReplayType, item.Type)
	}
}

func historyPanelErrorStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrHistoryItemNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNoActiveHistoryItem):
		return http.StatusNotFound
	case errors.Is(err, ErrUnsupportedReplayState), errors.Is(err, ErrUnsupportedReplayType):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func historyPanelRequestInput(r *http.Request) (string, ReplayMode, error) {
	if r == nil {
		return "", ReplayModeClipboardImmediate, errors.New("request is nil")
	}

	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		mode := ReplayMode(strings.TrimSpace(r.URL.Query().Get("mode")))
		if mode == ReplayModeNone {
			mode = ReplayModeClipboardImmediate
		}
		return id, mode, nil
	}

	if r.Body == nil {
		return "", ReplayModeClipboardImmediate, nil
	}
	defer r.Body.Close()

	var payload struct {
		ID   string     `json:"id"`
		Mode ReplayMode `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return "", ReplayModeClipboardImmediate, nil
		}
		return "", ReplayModeClipboardImmediate, errors.New("invalid JSON body")
	}
	if payload.Mode == ReplayModeNone {
		payload.Mode = ReplayModeClipboardImmediate
	}
	return strings.TrimSpace(payload.ID), payload.Mode, nil
}

func writeHistoryPanelJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("history panel: failed to write JSON response", "error", err)
	}
}

func writeHistoryPanelJSONError(w http.ResponseWriter, status int, err error) {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	writeHistoryPanelJSON(w, status, map[string]any{
		"ok":    false,
		"error": message,
	})
}

func historyPanelItemID(item *history.HistoryItem) string {
	if item == nil {
		return ""
	}
	return item.ID
}

func historyPanelToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func openHistoryPanelBrowser(targetURL string) error {
	if strings.TrimSpace(targetURL) == "" {
		return errors.New("empty history panel URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", targetURL)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", targetURL)
	default:
		opener, args, err := historyPanelOpenCommand()
		if err != nil {
			return err
		}
		args = append(args, targetURL)
		cmd = exec.CommandContext(ctx, opener, args...)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}

func historyPanelOpenCommand() (string, []string, error) {
	type openerSpec struct {
		name string
		args []string
	}
	for _, spec := range []openerSpec{
		{name: "xdg-open"},
		{name: "gio", args: []string{"open"}},
		{name: "gnome-open"},
		{name: "kde-open"},
	} {
		if _, err := exec.LookPath(spec.name); err == nil {
			return spec.name, spec.args, nil
		}
	}
	return "", nil, errors.New("no browser opener command available")
}

type historyPanelListResponse struct {
	Overview      historyPanelOverview       `json:"overview"`
	Devices       historyPanelDeviceSnapshot `json:"devices"`
	Settings      historyPanelSettings       `json:"settings"`
	Actions       historyPanelActionState    `json:"actions"`
	ActiveID      string                     `json:"active_id,omitempty"`
	Items         []historyPanelListItem     `json:"items"`
	FileTransfers []historyPanelFileTransfer `json:"file_transfers"`
	Events        []historyPanelEvent        `json:"events"`
}

type historyPanelOverview struct {
	ConnectionStatus       string `json:"connection_status,omitempty"`
	ServerURL              string `json:"server_url,omitempty"`
	Username               string `json:"username,omitempty"`
	E2EEEnabled            bool   `json:"e2ee_enabled"`
	P2PEnabled             bool   `json:"p2p_enabled"`
	P2PReadyPeers          int    `json:"p2p_ready_peers,omitempty"`
	FileMemoryThresholdMiB int64  `json:"file_memory_threshold_mib,omitempty"`
	ActiveSummary          string `json:"active_summary,omitempty"`
	ActiveType             string `json:"active_type,omitempty"`
	ActiveState            string `json:"active_state,omitempty"`
	TotalItems             int    `json:"total_items"`
	ReplayableItems        int    `json:"replayable_items"`
}

type historyPanelActionState struct {
	CanConnect      bool `json:"can_connect"`
	CanDisconnect   bool `json:"can_disconnect"`
	CanSendCurrent  bool `json:"can_send_current"`
	CanReplayActive bool `json:"can_replay_active"`
	CanSaveSettings bool `json:"can_save_settings"`
}

type historyPanelDeviceSnapshot struct {
	LocalSessionID string   `json:"local_session_id,omitempty"`
	P2PSessionID   string   `json:"p2p_session_id,omitempty"`
	PeerIDs        []string `json:"peer_ids"`
	ReadyPeerIDs   []string `json:"ready_peer_ids"`
}

type historyPanelSettings struct {
	ServerURL              string `json:"server_url,omitempty"`
	Username               string `json:"username,omitempty"`
	PasswordConfigured     bool   `json:"password_configured"`
	E2EEEnabled            bool   `json:"e2ee_enabled"`
	P2PEnabled             bool   `json:"p2p_enabled"`
	StunURL                string `json:"stun_url,omitempty"`
	AutoReconnect          bool   `json:"auto_reconnect"`
	ReconnectDelaySec      int    `json:"reconnect_delay_sec"`
	FileMemoryThresholdMiB int64  `json:"file_memory_threshold_mib"`
}

type historyPanelSettingsInput struct {
	ServerURL              string `json:"server_url"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	E2EEEnabled            bool   `json:"e2ee_enabled"`
	P2PEnabled             bool   `json:"p2p_enabled"`
	StunURL                string `json:"stun_url"`
	AutoReconnect          bool   `json:"auto_reconnect"`
	ReconnectDelaySec      int    `json:"reconnect_delay_sec"`
	FileMemoryThresholdMiB int64  `json:"file_memory_threshold_mib"`
}

type historyPanelFileTransfer struct {
	ID              string    `json:"id,omitempty"`
	TransferID      string    `json:"transfer_id,omitempty"`
	DisplayName     string    `json:"display_name,omitempty"`
	State           string    `json:"state,omitempty"`
	SourceDevice    string    `json:"source_device,omitempty"`
	SizeBytes       int64     `json:"size_bytes,omitempty"`
	SizeHuman       string    `json:"size_human,omitempty"`
	ProgressPercent int       `json:"progress_percent,omitempty"`
	TotalChunks     int       `json:"total_chunks,omitempty"`
	LastChunkIdx    int       `json:"last_chunk_idx,omitempty"`
	LocalPathCount  int       `json:"local_path_count,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type historyPanelEvent struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind,omitempty"`
	Message string    `json:"message,omitempty"`
}

type historyPanelListItem struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	State        string    `json:"state"`
	DisplayName  string    `json:"display_name,omitempty"`
	FileName     string    `json:"file_name,omitempty"`
	SourceDevice string    `json:"source_device,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	SizeHuman    string    `json:"size_human,omitempty"`
	Active       bool      `json:"active"`
	Replayable   bool      `json:"replayable"`
}

//go:embed history_panel.html
var historyPanelHTML string

