package clipboard

import (
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/clipcascade/pkg/constants"
)

func TestBuildAndParseFileStubPayload(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(f1, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if err := os.WriteFile(f2, []byte("123456789"), 0o644); err != nil {
		t.Fatalf("write f2: %v", err)
	}

	payload := buildFileStubPayload([]string{f1, f2})
	meta := parseFileStubPayload(payload)

	if meta.Count != 2 {
		t.Fatalf("count mismatch: got %d", meta.Count)
	}
	if meta.TotalBytes != int64(5+9) {
		t.Fatalf("total bytes mismatch: got %d", meta.TotalBytes)
	}
	if len(meta.Names) != 2 || meta.Names[0] != "a.txt" || meta.Names[1] != "b.bin" {
		t.Fatalf("unexpected names: %#v", meta.Names)
	}
}

func TestParseLegacyFileStubPayload(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(f1, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	meta := parseFileStubPayload(f1 + "\n")
	if meta.Count != 1 {
		t.Fatalf("count mismatch: got %d", meta.Count)
	}
	if meta.TotalBytes != 3 {
		t.Fatalf("total bytes mismatch: got %d", meta.TotalBytes)
	}
	if len(meta.Names) != 1 || meta.Names[0] != "legacy.txt" {
		t.Fatalf("unexpected names: %#v", meta.Names)
	}
}

func TestDeriveCaptureDataPrefersFilesOverImageAndText(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(filePath, []byte("file"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got := deriveCaptureData([]string{filePath}, []byte("image"), []byte("text"))
	if got == nil {
		t.Fatal("expected capture data")
	}
	if got.Type != constants.TypeFileStub {
		t.Fatalf("type mismatch: got %q", got.Type)
	}
	if got.Payload != buildFileStubPayload([]string{filePath}) {
		t.Fatalf("payload mismatch: got %q", got.Payload)
	}
	if got.FileName != buildFileStubMeta([]string{filePath}) {
		t.Fatalf("filename mismatch: got %q", got.FileName)
	}
}

func TestDeriveCaptureDataPrefersImageOverText(t *testing.T) {
	image := []byte("image-bytes")
	got := deriveCaptureData(nil, image, []byte("text"))
	if got == nil {
		t.Fatal("expected capture data")
	}
	if got.Type != constants.TypeImage {
		t.Fatalf("type mismatch: got %q", got.Type)
	}
	if got.Payload != base64.StdEncoding.EncodeToString(image) {
		t.Fatalf("payload mismatch: got %q", got.Payload)
	}
	if got.FileName != "" {
		t.Fatalf("expected empty filename, got %q", got.FileName)
	}
}

func TestDeriveCaptureDataFallsBackToText(t *testing.T) {
	text := []byte("plain text")
	got := deriveCaptureData(nil, nil, text)
	if got == nil {
		t.Fatal("expected capture data")
	}
	if got.Type != constants.TypeText {
		t.Fatalf("type mismatch: got %q", got.Type)
	}
	if got.Payload != string(text) {
		t.Fatalf("payload mismatch: got %q", got.Payload)
	}
	if got.FileName != "" {
		t.Fatalf("expected empty filename, got %q", got.FileName)
	}
}

func TestDeriveCaptureDataReturnsNilForEmptyClipboard(t *testing.T) {
	got := deriveCaptureData(nil, nil, nil)
	if got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestDeriveCaptureDataUsesFileStubForFiles(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(f1, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if err := os.WriteFile(f2, []byte("12345"), 0o644); err != nil {
		t.Fatalf("write f2: %v", err)
	}

	got := deriveCaptureData([]string{f1, f2}, nil, nil)
	if got == nil {
		t.Fatal("expected capture data")
	}
	if got.Type != constants.TypeFileStub {
		t.Fatalf("type mismatch: got %q", got.Type)
	}
	if got.Payload != buildFileStubPayload([]string{f1, f2}) {
		t.Fatalf("payload mismatch: got %q", got.Payload)
	}
	if got.FileName != buildFileStubMeta([]string{f1, f2}) {
		t.Fatalf("filename mismatch: got %q", got.FileName)
	}
}

func TestSelectFileContentSingleFileUsesFileStubInsteadOfLegacyEager(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "single.bin")
	if err := os.WriteFile(filePath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, ok := selectFileContent([]string{filePath}, false)
	if !ok {
		t.Fatal("selectFileContent() ok = false, want true")
	}
	if got.Type != constants.TypeFileStub {
		t.Fatalf("type mismatch: got %q, want %q", got.Type, constants.TypeFileStub)
	}
	if got.Payload != buildFileStubPayload([]string{filePath}) {
		t.Fatalf("payload mismatch: got %q", got.Payload)
	}
}

func TestSelectFileContentSingleImageFileUsesFileStub(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "sample.png")
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{G: 255, A: 255})
	out, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := png.Encode(out, img); err != nil {
		out.Close()
		t.Fatalf("Encode() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	got, ok := selectFileContent([]string{filePath}, false)
	if !ok {
		t.Fatal("selectFileContent() ok = false, want true")
	}
	// 所有文件（包括单个图片文件）统一走 TypeFileStub
	if got.Type != constants.TypeFileStub {
		t.Fatalf("type mismatch: got %q, want %q", got.Type, constants.TypeFileStub)
	}
	if got.Payload != buildFileStubPayload([]string{filePath}) {
		t.Fatalf("payload mismatch")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return data
}
