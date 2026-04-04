package handler

import (
	"testing"

	"github.com/gofiber/contrib/websocket"
)

func TestBroadcastTargetsIncludesSenderConnection(t *testing.T) {
	hub := NewWSHub()
	sender := &websocket.Conn{}
	peer := &websocket.Conn{}

	hub.connections["alice"] = map[*websocket.Conn]bool{
		sender: true,
		peer:   true,
	}
	hub.writeLocks[sender] = nil
	hub.writeLocks[peer] = nil

	targets := hub.broadcastTargets("alice")
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}

	foundSender := false
	for _, target := range targets {
		if target.conn == sender {
			foundSender = true
			break
		}
	}
	if !foundSender {
		t.Fatal("sender connection missing from broadcast targets")
	}
}
