//go:build windows

package clipboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	user32                         = syscall.NewLazyDLL("user32.dll")
	shell32                        = syscall.NewLazyDLL("shell32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procDragQueryFileW             = shell32.NewProc("DragQueryFileW")
)

const (
	CF_HDROP = 15

	gmemMoveable = 0x0002
	gmemZeroInit = 0x0040

	dropEffectCopy = 0x0001
)

type dropFiles struct {
	PFiles uint32
	PtX    int32
	PtY    int32
	FNC    uint32
	FWide  uint32
}

// getPlatformChangeCount 返回 Windows 剪贴板的序列号。
func getPlatformChangeCount() int64 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return int64(r)
}

// getPlatformFilePaths 查询 Windows CF_HDROP 剪贴板。
func getPlatformFilePaths() ([]string, error) {
	if err := openClipboardWithRetry(); err != nil {
		return nil, nil
	}
	defer procCloseClipboard.Call()

	avail, _, _ := procIsClipboardFormatAvailable.Call(CF_HDROP)
	if avail == 0 {
		return nil, nil
	}

	hDrop, _, _ := procGetClipboardData.Call(CF_HDROP)
	if hDrop == 0 {
		return nil, nil
	}

	ptr, _, _ := procGlobalLock.Call(hDrop)
	if ptr == 0 {
		return nil, nil
	}
	defer procGlobalUnlock.Call(hDrop)

	count, _, _ := procDragQueryFileW.Call(hDrop, 0xFFFFFFFF, 0, 0)
	if count == 0 || count > 1000 {
		return nil, nil
	}

	var paths []string
	for i := uintptr(0); i < count; i++ {
		size, _, _ := procDragQueryFileW.Call(hDrop, i, 0, 0)
		if size == 0 {
			continue
		}

		buf := make([]uint16, size+1)
		procDragQueryFileW.Call(hDrop, i, uintptr(unsafe.Pointer(&buf[0])), size+1)

		path := windows.UTF16ToString(buf)
		if path != "" {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// getPlatformImageData 通过 Win32 API 读取 CF_DIB 格式的剪贴板图片数据。
// 截图工具通常只设置 CF_BITMAP/CF_DIB，不设置文件路径，
// 因此不能依赖 golang.design/x/clipboard.Read(FmtImage)，需要原生实现。
func getPlatformImageData() []byte {
	if err := openClipboardWithRetry(); err != nil {
		return nil
	}
	defer procCloseClipboard.Call()

	// CF_DIB = 8
	const cfDIB = 8
	avail, _, _ := procIsClipboardFormatAvailable.Call(cfDIB)
	if avail == 0 {
		return nil
	}

	hData, _, _ := procGetClipboardData.Call(cfDIB)
	if hData == 0 {
		return nil
	}

	ptr, _, _ := procGlobalLock.Call(hData)
	if ptr == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(hData)

	rawSize, _, _ := procGlobalSize.Call(hData)
	if rawSize < 40 || rawSize > uintptr(^uint(0)>>1) {
		return nil
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(rawSize))

	totalSize, pixelOffset, ok := windowsDIBLayout(raw)
	if !ok {
		return nil
	}
	data := append([]byte(nil), raw[:totalSize]...)

	// 构建完整的 BMP 文件（添加 BITMAPFILEHEADER）
	fileHeaderSize := 14
	bmpSize := fileHeaderSize + totalSize
	bmp := make([]byte, bmpSize)
	// BM 签名
	bmp[0] = 'B'
	bmp[1] = 'M'
	// 文件大小
	binary.LittleEndian.PutUint32(bmp[2:6], uint32(bmpSize))
	// 像素数据偏移
	binary.LittleEndian.PutUint32(bmp[10:14], pixelOffset)
	// 复制 DIB 数据
	copy(bmp[fileHeaderSize:], data)

	return bmp
}

// getPlatformTextData Windows 直接使用 golang.design/x/clipboard
func getPlatformTextData() []byte { return nil }

// isWayland Windows 平台不存在 Wayland，始终返回 false。
func isWayland() bool { return false }

// setPlatformText Windows 平台不需要特殊处理，返回降级错误。
func setPlatformText(_ string) error { return errNotWayland }

func setPlatformImage(_ []byte) error { return errNotWayland }

var errNotWayland = errors.New("not wayland")

func setPlatformFilePaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	slog.Info("剪贴板：通过 Windows CF_HDROP 将文件路径写入剪贴板...")
	return setWindowsFileDropList(paths)
}

func setWindowsFileDropList(paths []string) error {
	if err := openClipboardWithRetry(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()

	hDrop, err := newDropFilesHandle(paths)
	if err != nil {
		return err
	}
	ownedByClipboard := false
	defer func() {
		if !ownedByClipboard {
			procGlobalFree.Call(hDrop)
		}
	}()

	if r, _, callErr := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard: %w", callErr)
	}

	if r, _, callErr := procSetClipboardData.Call(CF_HDROP, hDrop); r == 0 {
		return fmt.Errorf("SetClipboardData(CF_HDROP): %w", callErr)
	}
	ownedByClipboard = true

	if err := setPreferredDropEffect(dropEffectCopy); err != nil {
		slog.Warn("剪贴板：设置 Preferred DropEffect 失败", "错误", err)
	}
	return nil
}

func openClipboardWithRetry() error {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		r, _, callErr := procOpenClipboard.Call(0)
		if r != 0 {
			return nil
		}
		lastErr = callErr
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = syscall.EINVAL
	}
	return fmt.Errorf("OpenClipboard: %w", lastErr)
}

func windowsDIBLayout(raw []byte) (int, uint32, bool) {
	if len(raw) < 40 {
		return 0, 0, false
	}

	biSize := binary.LittleEndian.Uint32(raw[0:4])
	if biSize < 40 || biSize > uint32(len(raw)) {
		return 0, 0, false
	}
	biWidth := windowsAbsInt32(int32(binary.LittleEndian.Uint32(raw[4:8])))
	biHeight := windowsAbsInt32(int32(binary.LittleEndian.Uint32(raw[8:12])))
	if biWidth == 0 || biHeight == 0 {
		return 0, 0, false
	}
	biBitCount := binary.LittleEndian.Uint16(raw[14:16])
	if biBitCount == 0 || biBitCount > 64 {
		return 0, 0, false
	}
	biSizeImage := binary.LittleEndian.Uint32(raw[20:24])
	biClrUsed := binary.LittleEndian.Uint32(raw[32:36])

	var colorTableSize uint64
	if biBitCount <= 8 {
		colors := uint64(biClrUsed)
		if colors == 0 {
			colors = uint64(1) << biBitCount
		}
		colorTableSize = colors * 4
	}

	var pixelDataSize uint64
	if biSizeImage != 0 {
		pixelDataSize = uint64(biSizeImage)
	} else {
		stride := ((uint64(biBitCount)*biWidth + 31) / 32) * 4
		pixelDataSize = stride * biHeight
	}

	totalSize := uint64(biSize)
	if colorTableSize > uint64(len(raw))-totalSize {
		return 0, 0, false
	}
	totalSize += colorTableSize
	if pixelDataSize > uint64(len(raw))-totalSize {
		return 0, 0, false
	}
	totalSize += pixelDataSize
	if totalSize > uint64(int(^uint(0)>>1)) {
		return 0, 0, false
	}

	pixelOffset := uint64(14) + uint64(biSize) + colorTableSize
	if pixelOffset > totalSize || pixelOffset > uint64(^uint32(0)) {
		return 0, 0, false
	}

	return int(totalSize), uint32(pixelOffset), true
}

func windowsAbsInt32(v int32) uint64 {
	if v < 0 {
		return uint64(-(int64(v)))
	}
	return uint64(v)
}

func newDropFilesHandle(paths []string) (uintptr, error) {
	utf16Paths := make([]uint16, 0, len(paths)*32)
	for _, path := range paths {
		if path == "" {
			continue
		}
		encoded, err := windows.UTF16FromString(path)
		if err != nil {
			return 0, err
		}
		utf16Paths = append(utf16Paths, encoded...)
	}
	utf16Paths = append(utf16Paths, 0)

	headerSize := unsafe.Sizeof(dropFiles{})
	totalBytes := headerSize + uintptr(len(utf16Paths))*unsafe.Sizeof(uint16(0))
	handle, _, callErr := procGlobalAlloc.Call(gmemMoveable|gmemZeroInit, totalBytes)
	if handle == 0 {
		return 0, fmt.Errorf("GlobalAlloc: %w", callErr)
	}

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		procGlobalFree.Call(handle)
		return 0, fmt.Errorf("GlobalLock failed")
	}

	header := (*dropFiles)(unsafe.Pointer(ptr))
	header.PFiles = uint32(headerSize)
	header.FWide = 1
	if len(utf16Paths) > 0 {
		dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr+uintptr(header.PFiles))), len(utf16Paths))
		copy(dst, utf16Paths)
	}
	procGlobalUnlock.Call(handle)
	return handle, nil
}

func setPreferredDropEffect(effect uint32) error {
	name, err := windows.UTF16PtrFromString("Preferred DropEffect")
	if err != nil {
		return err
	}
	format, _, callErr := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
	if format == 0 {
		return fmt.Errorf("RegisterClipboardFormatW: %w", callErr)
	}

	handle, _, allocErr := procGlobalAlloc.Call(gmemMoveable|gmemZeroInit, unsafe.Sizeof(effect))
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc Preferred DropEffect: %w", allocErr)
	}
	ownedByClipboard := false
	defer func() {
		if !ownedByClipboard {
			procGlobalFree.Call(handle)
		}
	}()

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock Preferred DropEffect failed")
	}
	*(*uint32)(unsafe.Pointer(ptr)) = effect
	procGlobalUnlock.Call(handle)

	if r, _, setErr := procSetClipboardData.Call(format, handle); r == 0 {
		return fmt.Errorf("SetClipboardData(Preferred DropEffect): %w", setErr)
	}
	ownedByClipboard = true
	return nil
}
