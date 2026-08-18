package clipboard

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/clipcascade/pkg/constants"
	"golang.org/x/image/bmp"
)

func TestNormalizeClipboardImagePNGConvertsWindowsBMP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	img.Set(1, 1, color.RGBA{B: 0xff, A: 0xff})

	var source bytes.Buffer
	if err := bmp.Encode(&source, img); err != nil {
		t.Fatalf("bmp.Encode() error = %v", err)
	}
	normalized, err := normalizeClipboardImagePNG(source.Bytes())
	if err != nil {
		t.Fatalf("normalizeClipboardImagePNG() error = %v", err)
	}
	if !bytes.HasPrefix(normalized, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("normalized image does not have PNG signature: %x", normalized[:min(len(normalized), 8)])
	}
	if _, err := png.Decode(bytes.NewReader(normalized)); err != nil {
		t.Fatalf("png.Decode(normalized) error = %v", err)
	}
}

func TestPasteInvalidImageRestoresClipboardWriteSuppression(t *testing.T) {
	m := &Manager{
		lastHash:        42,
		suppressedEdits: 2,
		suppressedAt:    time.Unix(123, 0),
	}

	err := m.Paste("not-base64", constants.TypeImage, "")
	if err == nil {
		t.Fatal("Paste() error = nil, want invalid image error")
	}

	state := m.snapshotClipboardWriteState()
	if state.lastHash != 42 || state.suppressedEdits != 2 || !state.suppressedAt.Equal(time.Unix(123, 0)) {
		t.Fatalf("clipboard write state = %#v, want original state restored", state)
	}
}

func TestPasteUnsupportedTypeRestoresClipboardWriteSuppression(t *testing.T) {
	m := &Manager{lastHash: 99}
	if err := m.Paste("payload", "unknown", ""); err == nil {
		t.Fatal("Paste() error = nil, want unsupported type error")
	}
	if state := m.snapshotClipboardWriteState(); state.lastHash != 99 || state.suppressedEdits != 0 {
		t.Fatalf("clipboard write state = %#v, want original state restored", state)
	}
}
