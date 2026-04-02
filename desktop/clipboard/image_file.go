package clipboard

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
)

func buildClipboardImagePayload(path string) (payload string, filename string, ok bool) {
	imageBytes, err := clipboardImageBytesFromFile(path)
	if err != nil || len(imageBytes) == 0 {
		return "", "", false
	}
	name := strings.TrimSpace(filepath.Base(path))
	if name == "" || filepath.Ext(name) == "" {
		name = "image.png"
	}
	return base64.StdEncoding.EncodeToString(imageBytes), name, true
}

// BuildClipboardImagePayload 尝试将单个图片文件转换为图片链路可发送的 payload。
func BuildClipboardImagePayload(path string) (payload string, filename string, ok bool) {
	return buildClipboardImagePayload(path)
}

func clipboardImageBytesFromFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
