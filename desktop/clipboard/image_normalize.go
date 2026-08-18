package clipboard

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
)

// normalizeClipboardImagePNG converts clipboard images to the PNG byte format
// required by golang.design/x/clipboard.FmtImage on every supported platform.
func normalizeClipboardImagePNG(data []byte) ([]byte, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode clipboard image: %w", err)
	}
	if format == "png" {
		return data, nil
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, fmt.Errorf("encode clipboard image as PNG: %w", err)
	}
	return out.Bytes(), nil
}
