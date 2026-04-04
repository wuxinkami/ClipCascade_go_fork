package handler

import "testing"

type mockReadLimitConn struct {
	limit int64
}

func (m *mockReadLimitConn) SetReadLimit(limit int64) {
	m.limit = limit
}

func TestResolveWebSocketReadLimitBytes(t *testing.T) {
	t.Run("prefer_explicit_bytes", func(t *testing.T) {
		got := ResolveWebSocketReadLimitBytes(9*1024*1024, 2)
		if got != 9*1024*1024 {
			t.Fatalf("limit = %d, want %d", got, 9*1024*1024)
		}
	})

	t.Run("fallback_to_mib", func(t *testing.T) {
		got := ResolveWebSocketReadLimitBytes(0, 12)
		if got != 12*1024*1024 {
			t.Fatalf("limit = %d, want %d", got, 12*1024*1024)
		}
	})

	t.Run("defaults_when_empty", func(t *testing.T) {
		got := ResolveWebSocketReadLimitBytes(0, 0)
		if got != defaultWebSocketReadLimitBytes {
			t.Fatalf("limit = %d, want %d", got, defaultWebSocketReadLimitBytes)
		}
	})
}

func TestNormalizeWebSocketReadLimitBytes(t *testing.T) {
	got := normalizeWebSocketReadLimitBytes(1)
	if got != minWebSocketReadLimitBytes {
		t.Fatalf("limit = %d, want %d", got, minWebSocketReadLimitBytes)
	}
}

func TestApplyWebSocketReadLimit(t *testing.T) {
	conn := &mockReadLimitConn{}
	applyWebSocketReadLimit(conn, 128)
	if conn.limit != minWebSocketReadLimitBytes {
		t.Fatalf("conn.limit = %d, want %d", conn.limit, minWebSocketReadLimitBytes)
	}
}

func TestNewWSHubAppliesNormalizedLimit(t *testing.T) {
	hub := NewWSHub(256)
	if hub.readLimitBytes != minWebSocketReadLimitBytes {
		t.Fatalf("readLimitBytes = %d, want %d", hub.readLimitBytes, minWebSocketReadLimitBytes)
	}
}

func TestNewP2PSignalingAppliesNormalizedLimit(t *testing.T) {
	p2p := NewP2PSignaling(256)
	if p2p.readLimitBytes != minWebSocketReadLimitBytes {
		t.Fatalf("readLimitBytes = %d, want %d", p2p.readLimitBytes, minWebSocketReadLimitBytes)
	}
}
