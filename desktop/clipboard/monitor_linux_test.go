//go:build linux

package clipboard

import (
	"encoding/base64"
	"testing"

	"github.com/clipcascade/pkg/constants"
)

func TestSelectWaylandPreferredTypePrefersUtf8PlainText(t *testing.T) {
	got := selectWaylandPreferredType(
		[]string{"text/html", "text/plain;charset=utf-8", "text/plain"},
		[]string{"text/plain;charset=utf-8", "text/plain"},
		[]string{"text/"},
		map[string]bool{"text/html": true},
	)
	if got != "text/plain;charset=utf-8" {
		t.Fatalf("selectWaylandPreferredType() = %q, want %q", got, "text/plain;charset=utf-8")
	}
}

func TestSelectWaylandPreferredTypeFallsBackToAvailableImageMime(t *testing.T) {
	got := selectWaylandPreferredType(
		[]string{"image/webp", "text/plain"},
		[]string{"image/png", "image/jpeg"},
		[]string{"image/"},
		nil,
	)
	if got != "image/webp" {
		t.Fatalf("selectWaylandPreferredType() = %q, want %q", got, "image/webp")
	}
}

func TestHandleNativeClipboardSnapshotSeedOnlyDoesNotBroadcast(t *testing.T) {
	manager := NewManager()
	calls := 0
	manager.OnCopy(func(*CaptureData) {
		calls++
	})

	orig := monitorCaptureCurrent
	monitorCaptureCurrent = func(m *Manager) *CaptureData {
		return &CaptureData{
			Type:    constants.TypeText,
			Payload: "hello",
		}
	}
	t.Cleanup(func() { monitorCaptureCurrent = orig })

	if got := manager.handleNativeClipboardSnapshot(true); got {
		t.Fatal("handleNativeClipboardSnapshot(seedOnly) = true, want false")
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
	if got := manager.handleNativeClipboardSnapshot(false); got != true {
		t.Fatalf("handleNativeClipboardSnapshot(false) = %v, want true", got)
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want 0 for unchanged snapshot", calls)
	}
}

func TestHandleNativeClipboardSnapshotBroadcastsWaylandTextChange(t *testing.T) {
	manager := NewManager()
	var captures []*CaptureData
	manager.OnCopy(func(capture *CaptureData) {
		captures = append(captures, capture)
	})

	snapshots := []*CaptureData{
		{Type: constants.TypeText, Payload: "hello"},
		{Type: constants.TypeText, Payload: "world"},
	}
	index := 0
	orig := monitorCaptureCurrent
	monitorCaptureCurrent = func(m *Manager) *CaptureData {
		if index >= len(snapshots) {
			return snapshots[len(snapshots)-1]
		}
		capture := snapshots[index]
		index++
		return capture
	}
	t.Cleanup(func() { monitorCaptureCurrent = orig })

	manager.handleNativeClipboardSnapshot(true)
	manager.handleNativeClipboardSnapshot(false)

	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Type != constants.TypeText || captures[0].Payload != "world" {
		t.Fatalf("capture = %#v, want text/world", captures[0])
	}
}

func TestHandleNativeClipboardSnapshotBroadcastsWaylandImageChange(t *testing.T) {
	manager := NewManager()
	var captures []*CaptureData
	manager.OnCopy(func(capture *CaptureData) {
		captures = append(captures, capture)
	})

	first := base64.StdEncoding.EncodeToString([]byte("img-1"))
	second := base64.StdEncoding.EncodeToString([]byte("img-2"))
	snapshots := []*CaptureData{
		{Type: constants.TypeImage, Payload: first},
		{Type: constants.TypeImage, Payload: second},
	}
	index := 0
	orig := monitorCaptureCurrent
	monitorCaptureCurrent = func(m *Manager) *CaptureData {
		if index >= len(snapshots) {
			return snapshots[len(snapshots)-1]
		}
		capture := snapshots[index]
		index++
		return capture
	}
	t.Cleanup(func() { monitorCaptureCurrent = orig })

	manager.handleNativeClipboardSnapshot(true)
	manager.handleNativeClipboardSnapshot(false)

	if len(captures) != 1 {
		t.Fatalf("len(captures) = %d, want 1", len(captures))
	}
	if captures[0].Type != constants.TypeImage || captures[0].Payload != second {
		t.Fatalf("capture = %#v, want image/%q", captures[0], second)
	}
}
