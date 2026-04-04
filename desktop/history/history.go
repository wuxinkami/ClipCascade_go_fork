// Package history 提供跨设备历史项管理与状态流转骨架。
package history

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/clipcascade/pkg/constants"
)

// ItemState 表示历史项当前所处的状态。
type ItemState string

const (
	// StateReady 表示文本或图片已就绪，可立即重放。
	StateReady ItemState = "ready"
	// StateOffered 表示文件清单已到达，但尚未开始下载。
	StateOffered ItemState = "offered"
	// StateDownloading 表示文件正在传输中。
	StateDownloading ItemState = "downloading"
	// StateReadyToPaste 表示文件已落盘，等待第二次粘贴动作。
	StateReadyToPaste ItemState = "ready_to_paste"
	// StateFailed 表示传输或处理失败。
	StateFailed ItemState = "failed"
	// StateConsumed 表示最近一次已被用户消费，但仍可重复使用。
	StateConsumed ItemState = "consumed"
)

const defaultMaxItems = 100

var validTransitions = map[ItemState]map[ItemState]struct{}{
	StateReady: {
		StateReady:    {},
		StateConsumed: {},
		StateFailed:   {},
	},
	StateOffered: {
		StateOffered:     {},
		StateDownloading: {},
		StateFailed:      {},
	},
	StateDownloading: {
		StateDownloading:  {},
		StateReadyToPaste: {},
		StateFailed:       {},
	},
	StateReadyToPaste: {
		StateReadyToPaste: {},
		StateConsumed:     {},
		StateFailed:       {},
	},
	StateFailed: {
		StateFailed:      {},
		StateOffered:     {},
		StateDownloading: {},
	},
	StateConsumed: {
		StateConsumed:     {},
		StateReady:        {},
		StateDownloading:  {},
		StateReadyToPaste: {},
		StateFailed:       {},
	},
}

var knownStates = map[ItemState]struct{}{
	StateReady:        {},
	StateOffered:      {},
	StateDownloading:  {},
	StateReadyToPaste: {},
	StateFailed:       {},
	StateConsumed:     {},
}

// HistoryItem 表示一条历史记录。
type HistoryItem struct {
	ID                string
	Type              string
	State             ItemState
	Kind              string
	DisplayName       string
	Payload           string
	PayloadType       string
	FileName          string
	SourceSessionID   string
	SourceDevice      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	TransferID        string
	LocalPaths        []string
	ReservedPaths     []string
	PendingReplayMode string
	LastChunkIdx      int
	ErrorMessage      string
}

// Manager 管理历史项列表、活动项和状态更新。
type Manager struct {
	mu        sync.RWMutex
	items     []*HistoryItem
	activeID  string
	maxItems  int
	onChanged func()
}

// NewManager 创建一个新的 HistoryManager。
func NewManager(maxItems int) *Manager {
	if maxItems <= 0 {
		maxItems = defaultMaxItems
	}

	return &Manager{
		maxItems: maxItems,
	}
}

// AddItem 添加一条历史项，并在超出上限时淘汰最旧的 consumed/failed 项。
// 最新加入的历史项会自动成为当前活动项，保证控制中心和共享热键默认跟随最新内容。
func (m *Manager) AddItem(item *HistoryItem) {
	if item == nil {
		return
	}

	newItem := cloneItem(item)
	if !isValidInitialItem(newItem) {
		slog.Warn("历史：检测到非法初始状态", "id", newItem.ID, "type", newItem.Type, "state", newItem.State)
		return
	}

	now := time.Now()
	if newItem.ID == "" {
		newItem.ID = newUUID()
	}
	if newItem.CreatedAt.IsZero() {
		newItem.CreatedAt = now
	}
	if newItem.UpdatedAt.IsZero() {
		newItem.UpdatedAt = newItem.CreatedAt
	}

	var changed bool
	var callback func()

	m.mu.Lock()
	if existingIdx := m.findIndexByIDLocked(newItem.ID); existingIdx >= 0 {
		m.items = removeIndex(m.items, existingIdx)
	}

	insertIdx := m.insertIndexLocked(newItem.CreatedAt)
	m.items = append(m.items, nil)
	copy(m.items[insertIdx+1:], m.items[insertIdx:])
	m.items[insertIdx] = newItem
	m.activeID = newItem.ID

	evicted := m.evictOverflowLocked()
	changed = true
	callback = m.onChanged
	m.mu.Unlock()

	slog.Debug("历史：已添加历史项",
		"id", newItem.ID,
		"type", newItem.Type,
		"state", newItem.State,
		"evicted", evicted,
	)

	if changed && callback != nil {
		callback()
	}
}

// GetActive 返回当前活动项副本。
func (m *Manager) GetActive() *HistoryItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.items) == 0 {
		return nil
	}

	if m.activeID != "" {
		if item := m.getByIDLocked(m.activeID); item != nil {
			return cloneItem(item)
		}
	}

	return cloneItem(m.items[0])
}

// SetActive 将指定 ID 的历史项设为当前活动项。
// 后续若有更新的历史项加入，活动项会再次自动跟随最新项。
func (m *Manager) SetActive(id string) bool {
	if id == "" {
		return false
	}

	m.mu.Lock()
	if m.findIndexByIDLocked(id) < 0 {
		m.mu.Unlock()
		return false
	}

	if m.activeID == id {
		m.mu.Unlock()
		return true
	}

	m.activeID = id
	callback := m.onChanged
	m.mu.Unlock()

	slog.Debug("历史：当前活动项已变更", "id", id)

	if callback != nil {
		callback()
	}

	return true
}

// GetByID 返回指定 ID 的历史项副本。
func (m *Manager) GetByID(id string) *HistoryItem {
	if id == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return cloneItem(m.getByIDLocked(id))
}

// GetByTransferID 返回指定传输会话 ID 对应的历史项副本。
func (m *Manager) GetByTransferID(transferID string) *HistoryItem {
	if transferID == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, item := range m.items {
		if item.TransferID == transferID {
			return cloneItem(item)
		}
	}

	return nil
}

// UpdateState 更新指定历史项的状态并刷新 UpdatedAt。
func (m *Manager) UpdateState(id string, newState ItemState) bool {
	if id == "" || !isKnownState(newState) {
		return false
	}

	var (
		updated  *HistoryItem
		changed  bool
		callback func()
	)

	m.mu.Lock()
	for _, item := range m.items {
		if item.ID != id {
			continue
		}
		if !isValidTransition(item.State, newState) {
			m.mu.Unlock()
			slog.Warn("历史：检测到非法状态流转", "id", id, "from", item.State, "to", newState)
			return false
		}

		item.State = newState
		item.UpdatedAt = time.Now()
		updated = cloneItem(item)
		changed = true
		callback = m.onChanged
		break
	}
	m.mu.Unlock()

	if !changed {
		return false
	}

	slog.Debug("历史：状态已更新",
		"id", updated.ID,
		"state", updated.State,
		"updated_at", updated.UpdatedAt,
	)

	if callback != nil {
		callback()
	}

	return true
}

func (m *Manager) Mutate(id string, fn func(item *HistoryItem) error) (*HistoryItem, error) {
	if id == "" || fn == nil {
		return nil, fmt.Errorf("invalid mutate request")
	}

	var callback func()
	var updated *HistoryItem
	m.mu.Lock()

	item := m.getByIDLocked(id)
	if item == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("history item not found")
	}
	before := cloneItem(item)
	next := cloneItem(item)
	if err := fn(next); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if next.State != before.State {
		if !isKnownState(next.State) || !isValidTransition(before.State, next.State) {
			m.mu.Unlock()
			return nil, fmt.Errorf("invalid state transition %s -> %s", before.State, next.State)
		}
	}
	next.UpdatedAt = time.Now()
	*item = *next
	callback = m.onChanged
	updated = cloneItem(item)
	m.mu.Unlock()

	if callback != nil {
		callback()
	}
	return updated, nil
}

func (m *Manager) MutateByTransferID(transferID string, fn func(item *HistoryItem) error) (*HistoryItem, error) {
	if transferID == "" || fn == nil {
		return nil, fmt.Errorf("invalid mutate request")
	}

	m.mu.RLock()
	var id string
	for _, item := range m.items {
		if item.TransferID == transferID {
			id = item.ID
			break
		}
	}
	m.mu.RUnlock()
	if id == "" {
		return nil, fmt.Errorf("history item not found")
	}
	return m.Mutate(id, fn)
}

// List 返回全部历史项的副本切片。
func (m *Manager) List() []*HistoryItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]*HistoryItem, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, cloneItem(item))
	}

	return items
}

// OnChanged 设置历史项变更后的可选回调。
func (m *Manager) OnChanged(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onChanged = fn
}

func (m *Manager) getByIDLocked(id string) *HistoryItem {
	for _, item := range m.items {
		if item.ID == id {
			return item
		}
	}

	return nil
}

func (m *Manager) findIndexByIDLocked(id string) int {
	for idx, item := range m.items {
		if item.ID == id {
			return idx
		}
	}

	return -1
}

func (m *Manager) insertIndexLocked(createdAt time.Time) int {
	for idx, item := range m.items {
		if createdAt.After(item.CreatedAt) {
			return idx
		}
	}

	return len(m.items)
}

func (m *Manager) evictOverflowLocked() int {
	evicted := 0
	for len(m.items) > m.maxItems {
		evictIdx := -1
		for idx := len(m.items) - 1; idx >= 0; idx-- {
			if !isEvictableState(m.items[idx].State) {
				continue
			}
			evictIdx = idx
			break
		}
		if evictIdx < 0 {
			break
		}

		evictedItem := m.items[evictIdx]
		if m.activeID == evictedItem.ID {
			m.activeID = ""
		}
		m.items = removeIndex(m.items, evictIdx)
		evicted++

		slog.Debug("历史：已淘汰旧历史项",
			"id", evictedItem.ID,
			"state", evictedItem.State,
		)
	}

	return evicted
}

func cloneItem(item *HistoryItem) *HistoryItem {
	if item == nil {
		return nil
	}

	clone := *item
	if item.LocalPaths != nil {
		clone.LocalPaths = append([]string(nil), item.LocalPaths...)
	}
	if item.ReservedPaths != nil {
		clone.ReservedPaths = append([]string(nil), item.ReservedPaths...)
	}

	return &clone
}

func removeIndex(items []*HistoryItem, idx int) []*HistoryItem {
	copy(items[idx:], items[idx+1:])
	items[len(items)-1] = nil
	return items[:len(items)-1]
}

func isEvictableState(state ItemState) bool {
	return state == StateConsumed || state == StateFailed
}

func isValidTransition(from ItemState, to ItemState) bool {
	nextStates, ok := validTransitions[from]
	if !ok {
		return false
	}

	_, ok = nextStates[to]
	return ok
}

func isKnownState(state ItemState) bool {
	_, ok := knownStates[state]
	return ok
}

func isValidInitialItem(item *HistoryItem) bool {
	if item == nil {
		return false
	}

	switch item.Type {
	case constants.TypeText, constants.TypeImage:
		return item.State == StateReady
	case constants.TypeFileStub:
		return item.State == StateOffered || item.State == StateFailed
	default:
		return false
	}
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3],
		b[4], b[5],
		b[6], b[7],
		b[8], b[9],
		b[10], b[11], b[12], b[13], b[14], b[15],
	)
}
