package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/clipcascade/desktop/clipboard"
	"github.com/clipcascade/desktop/config"
	"github.com/clipcascade/pkg/constants"
	"github.com/clipcascade/pkg/protocol"
)

func TestSendCurrentClipboardNilCaptureReturnsErrNoCurrentClipboardData(t *testing.T) {
	err := sendCurrentClipboard(func() *clipboard.CaptureData {
		return nil
	}, buildClipboardDataFromCapture, func(*protocol.ClipboardData) error {
		t.Fatal("sender should not be called")
		return nil
	})
	if !errors.Is(err, ErrNoCurrentClipboardData) {
		t.Fatalf("err = %v, want %v", err, ErrNoCurrentClipboardData)
	}
}

func TestSendCurrentClipboardTextCaptureBuildsClipboardDataAndCallsSender(t *testing.T) {
	var got *protocol.ClipboardData

	err := sendCurrentClipboard(func() *clipboard.CaptureData {
		return &clipboard.CaptureData{
			Payload: "hello",
			Type:    constants.TypeText,
		}
	}, buildClipboardDataFromCapture, func(data *protocol.ClipboardData) error {
		got = data
		return nil
	})
	if err != nil {
		t.Fatalf("sendCurrentClipboard() error = %v", err)
	}
	if got == nil {
		t.Fatal("sender did not receive clipboard data")
	}
	if got.Type != constants.TypeText {
		t.Fatalf("got.Type = %q, want %q", got.Type, constants.TypeText)
	}
	if got.Payload != "hello" {
		t.Fatalf("got.Payload = %q, want %q", got.Payload, "hello")
	}
}

func TestSendCurrentClipboardReturnsSenderError(t *testing.T) {
	wantErr := errors.New("send failed")

	err := sendCurrentClipboard(func() *clipboard.CaptureData {
		return &clipboard.CaptureData{
			Payload: "hello",
			Type:    constants.TypeText,
		}
	}, buildClipboardDataFromCapture, func(*protocol.ClipboardData) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestDispatchClipboardBodySendsBothP2PAndStomp(t *testing.T) {
	stompCalls := 0
	p2pCalls := 0

	err := dispatchClipboardBody(
		"payload",
		func() bool { return true },
		func(payload string) error {
			stompCalls++
			return nil
		},
		func(payload string) int {
			p2pCalls++
			return 2
		},
	)
	if err != nil {
		t.Fatalf("dispatchClipboardBody() error = %v", err)
	}
	if p2pCalls != 1 {
		t.Fatalf("p2pCalls = %d, want 1", p2pCalls)
	}
	// P2P + STOMP 双发确保可靠送达
	if stompCalls != 1 {
		t.Fatalf("stompCalls = %d, want 1 (dual-send for reliability)", stompCalls)
	}
}

func TestDispatchClipboardBodyFallsBackToStompWhenNoP2PPeerReady(t *testing.T) {
	stompCalls := 0
	p2pCalls := 0

	err := dispatchClipboardBody(
		"payload",
		func() bool { return true },
		func(payload string) error {
			stompCalls++
			return nil
		},
		func(payload string) int {
			p2pCalls++
			return 0
		},
	)
	if err != nil {
		t.Fatalf("dispatchClipboardBody() error = %v", err)
	}
	if p2pCalls != 1 {
		t.Fatalf("p2pCalls = %d, want 1", p2pCalls)
	}
	if stompCalls != 1 {
		t.Fatalf("stompCalls = %d, want 1", stompCalls)
	}
}

func TestDispatchClipboardBodyReturnsTransportUnavailableWhenNoPathReady(t *testing.T) {
	err := dispatchClipboardBody(
		"payload",
		func() bool { return false },
		func(payload string) error { return nil },
		func(payload string) int { return 0 },
	)
	if !errors.Is(err, ErrClipboardTransportUnavailable) {
		t.Fatalf("err = %v, want %v", err, ErrClipboardTransportUnavailable)
	}
}

func TestApplicationBuildClipboardDataFromCaptureUsesManifestForFileStub(t *testing.T) {
	app := &Application{
		cfg:       &config.Config{Username: "desktop-a"},
		transfers: newTransferManager(),
	}
	data, err := app.buildClipboardDataFromCapture(&clipboard.CaptureData{
		Type:  constants.TypeFileStub,
		Paths: []string{"/tmp/a.txt"},
	})
	if err != nil {
		t.Fatalf("buildClipboardDataFromCapture() error = %v", err)
	}
	if data.Type != constants.TypeFileStub {
		t.Fatalf("data.Type = %q, want %q", data.Type, constants.TypeFileStub)
	}
	manifest, err := protocol.DecodePayload[protocol.FileStubManifest](data.Payload)
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if manifest.SourceSessionID != app.appSessionID() {
		t.Fatalf("manifest.SourceSessionID = %q, want %q", manifest.SourceSessionID, app.appSessionID())
	}
	if manifest.EntryCount != 1 {
		t.Fatalf("manifest.EntryCount = %d, want 1", manifest.EntryCount)
	}
}

func TestApplicationBuildClipboardDataFromCaptureKeepsImageDirect(t *testing.T) {
	app := &Application{
		cfg:       &config.Config{Username: "desktop-a"},
		transfers: newTransferManager(),
	}
	data, err := app.buildClipboardDataFromCapture(&clipboard.CaptureData{
		Type:     constants.TypeImage,
		Payload:  "base64-image",
		FileName: "sample.png",
		Paths:    []string{"/tmp/sample.png"},
	})
	if err != nil {
		t.Fatalf("buildClipboardDataFromCapture() error = %v", err)
	}
	if data.Type != constants.TypeImage {
		t.Fatalf("data.Type = %q, want %q", data.Type, constants.TypeImage)
	}
	if data.Payload != "base64-image" {
		t.Fatalf("data.Payload = %q, want %q", data.Payload, "base64-image")
	}
	if data.FileName != "sample.png" {
		t.Fatalf("data.FileName = %q, want %q", data.FileName, "sample.png")
	}
}

func TestApplicationBuildClipboardDataFromCaptureConvertsSingleImageFileStubToImage(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		cfg:       &config.Config{Username: "desktop-a"},
		transfers: newTransferManager("session-local"),
	}
	imgPath := filepath.Join(t.TempDir(), "capture.png")
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	if err := os.WriteFile(imgPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	data, err := app.buildClipboardDataFromCapture(&clipboard.CaptureData{
		Type:  constants.TypeFileStub,
		Paths: []string{imgPath},
	})
	if err != nil {
		t.Fatalf("buildClipboardDataFromCapture() error = %v", err)
	}
	if data.Type != constants.TypeImage {
		t.Fatalf("data.Type = %q, want %q", data.Type, constants.TypeImage)
	}
	if data.Payload == "" {
		t.Fatal("data.Payload is empty, want image payload")
	}
	if data.FileName != "capture.png" {
		t.Fatalf("data.FileName = %q, want %q", data.FileName, "capture.png")
	}
}

func TestAnnotateClipboardSourceSetsSourceSessionID(t *testing.T) {
	app := &Application{
		sessionID: "session-local",
		transfers: newTransferManager("session-local"),
	}
	data := &protocol.ClipboardData{
		Type:    constants.TypeText,
		Payload: "hello",
	}
	app.annotateClipboardSource(data)
	if data.SourceSessionID != "session-local" {
		t.Fatalf("SourceSessionID = %q, want %q", data.SourceSessionID, "session-local")
	}
	if data.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil", data.Metadata)
	}
}
