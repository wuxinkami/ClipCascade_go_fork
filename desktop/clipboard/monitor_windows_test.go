//go:build windows

package clipboard

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"unsafe"

	"golang.org/x/image/bmp"
	"golang.org/x/sys/windows"
)

func TestWindowsDIBToPNGProducesClipboardCompatiblePNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	img.Set(2, 1, color.RGBA{G: 0xff, A: 0xff})

	var source bytes.Buffer
	if err := bmp.Encode(&source, img); err != nil {
		t.Fatalf("bmp.Encode() error = %v", err)
	}
	bmpBytes := source.Bytes()
	if len(bmpBytes) <= 14 {
		t.Fatalf("encoded BMP too short: %d", len(bmpBytes))
	}

	pngBytes, err := windowsDIBToPNG(bmpBytes[14:])
	if err != nil {
		t.Fatalf("windowsDIBToPNG() error = %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	if decoded.Bounds().Dx() != 3 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("decoded bounds = %v, want 3x2", decoded.Bounds())
	}
}

func TestNewUnicodeTextHandleWritesNullTerminatedUTF16(t *testing.T) {
	handle, err := newUnicodeTextHandle(`C:\Temp\ClipCascade\image.png`)
	if err != nil {
		t.Fatalf("newUnicodeTextHandle() error = %v", err)
	}
	defer procGlobalFree.Call(handle)

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		t.Fatal("GlobalLock returned 0")
	}
	defer procGlobalUnlock.Call(handle)

	size, _, _ := procGlobalSize.Call(handle)
	if size == 0 {
		t.Fatal("GlobalSize returned 0")
	}
	got := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), int(size)/int(unsafe.Sizeof(uint16(0))))
	if decoded := windows.UTF16ToString(got); decoded != `C:\Temp\ClipCascade\image.png` {
		t.Fatalf("decoded text = %q", decoded)
	}
	if got[len(got)-1] != 0 {
		t.Fatal("UTF-16 clipboard text is not null terminated")
	}
}
