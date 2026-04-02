//go:build windows

package app

import "golang.org/x/sys/windows"

func availableDiskSpace(path string) (uint64, error) {
	var freeBytesAvailable uint64
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		&freeBytesAvailable, nil, nil,
	)
	return freeBytesAvailable, err
}
